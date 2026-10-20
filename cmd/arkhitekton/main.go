// Command arkhitekton: a single binary, three roles. See docs/02-architecture.md.
//
// `api` takes the user traffic and replicates. `worker` carries the background loops —
// queue scheduler, log collection, synchronisations — and stays single: a duplicated
// scheduler launches the same pipeline twice. `migrate` applies the schema, from an
// initialisation Job.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/discovery"

	"k8s.io/client-go/dynamic"

	"k8s.io/client-go/restmapper"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/deploykeys"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/portaldata"
	msestorage "forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
	pipelinelib "forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/gitea"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/registry"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/webhook"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/worker"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// Filled in at compile time through -ldflags (see Dockerfile and Makefile).
//
// The version is logged at every start-up: diagnosing unexpected behaviour begins with
// knowing which binary is running, which an image tag does not tell reliably — a tag can be
// republished.
var (
	version  = "dev"
	revision = "unknown"
)

const usage = `arkhitekton: release management

Usage:
  arkhitekton api       HTTP server (replicable)
  arkhitekton worker    Background loops (single instance, leader election)
  arkhitekton migrate   Applies the schema migrations
  arkhitekton reprocess Replays the results from the raw artifacts

Diagnosis:
  arkhitekton check-tracker  Checks access to the tracker and stops (read-only)

Options for reprocess:
  --release <tag>  Limits the replay to one release (default: the whole history)
  --dry-run        Lists what would be replayed, erasing nothing
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no role supplied")
	}

	role := config.Role(os.Args[1])
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("version", version, "revision", revision)

	cfg, err := config.Load(role)
	if err != nil {
		// The configuration is validated before a single port is opened: a half-configured start-up
		// postpones the failure to the first user, often during the rarest operation.
		return fmt.Errorf("invalid configuration:\n%w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch role {
	case config.RoleAPI:
		return runAPI(ctx, cfg, logger)
	case config.RoleWorker:
		return runWorker(ctx, cfg, logger)
	case config.RoleMigrate:
		return runMigrate(ctx, cfg, logger)
	case config.RoleReprocess:
		return runReprocess(ctx, cfg, logger, os.Args[2:])
	case config.RoleCheckTracker:
		return runCheckTracker(ctx, cfg, logger)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown role %q", role)
	}
}

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("starting", "role", cfg.Role, "addr", cfg.HTTP.Addr, "issuer", cfg.OIDC.IssuerURL)

	// A bounded wait: the database often comes back at the same time as the pod. Beyond the
	// bound, the startup probe takes over and restarts the pod — the fallback behaviour stays
	// Kubernetes's.
	if err := store.WaitForDatabase(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}
	// Then the **schema**, and not only the connection: the migration Job starts at the same time
	// as this pod, and serving requests against an incomplete schema would return 500s on tables
	// that are about to exist.
	if err := store.WaitForSchema(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}
	// Then the MSE plugin's, migrated just after the core by the same Job.
	if err := msestorage.WaitForSchema(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	// Storage access is checked at start-up: without that, a wrong credential would show only at
	// the first artifact publication, that is, at the end of a pipeline.
	storage, err := blob.Open(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	logger.Info("object storage", "bucket", storage.Bucket(), "region", cfg.Storage.Region)

	sessions := auth.NewSessionStore(db.Pool(), cfg.OIDC.SessionTTL)

	// The bootstrap no longer happens here: it is applied once, with the schema migration, and
	// the database is then its sole master. What remains at start-up is the observation of what
	// is still waiting for its holder — a misspelled identifier has no other symptom than "this
	// user has no permission".
	if pending, err := db.PendingGrants(ctx); err != nil {
		logger.Error("bootstrap roles pending", "error", err)
	} else if len(pending) > 0 {
		identifiers := make([]string, 0, len(pending))
		for _, grant := range pending {
			identifiers = append(identifiers, grant.Identifier+":"+grant.Role)
		}
		logger.Info("bootstrap roles waiting for a first sign-in",
			"count", len(pending), "declarations", identifiers)
	}

	// OIDC discovery happens here: an unreachable provider must prevent start-up rather than
	// produce opaque errors for the first user.
	provider, err := auth.NewProvider(ctx, cfg.OIDC)
	if err != nil {
		return err
	}

	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	// The missing rows of the embedded plugins, at every start-up. The bootstrap sets them only
	// once: a plugin appearing in a later version would never have a row, and the screen would
	// display it with no state.
	pluginRegistry, err := builtin.Registry()
	if err != nil {
		return fmt.Errorf("built-in plugins: %w", err)
	}
	if err := db.EnsurePlugins(ctx, pluginIDs(pluginRegistry)); err != nil {
		logger.Error("seeding the plugins", "error", err)
	}
	seedMSESettings(ctx, db, logger)

	// In the database and not in memory: two api replicas with no session affinity serve
	// `/auth/login` and `/auth/callback` through different instances, and the transition state
	// would be missing half the time.
	flows := auth.NewDBFlowStore(db.Pool(), 10*time.Minute)
	// The catalogue is loaded by the `api` too: it is what says what is launchable and which
	// parameters are required. It is not validated against the cluster here — the api has no
	// access to it, and the `worker` does it at start-up.
	pipelines, err := loadCatalogue(cfg)
	if err != nil {
		return err
	}
	logger.Info("catalogue loaded", "kinds", pipelines.Kinds(), "cap", pipelines.MaxConcurrentRuns)
	// The sensitivity of the environments is declared in the catalogue and read by the MSE
	// plugin. Logged: it is what decides who may promote where.
	policies := mse.Policies(pipelines)
	declaredProjects := make([]string, 0, len(policies))
	for slug := range policies {
		declaredProjects = append(declaredProjects, slug)
	}
	sort.Strings(declaredProjects)
	for _, slug := range declaredProjects {
		sensitive := make([]string, 0, len(policies[slug]))
		for id, policy := range policies[slug] {
			if policy.Sensitive {
				sensitive = append(sensitive, id)
			}
		}
		sort.Strings(sensitive)
		logger.Info("sensitive environments", "project", slug, "environments", sensitive)
	}
	authorizer := auth.NewAuthorizer(policies)
	// The modules are read at each request: the switch is flipped from the administration screen,
	// and reading it at start-up would make it lie until the next deployment.
	modules := dashhttp.NewModules(pluginRegistry, db, logger)
	authHandler := dashhttp.NewAuthHandler(
		provider, flows, sessions, logger, secureCookies(cfg.HTTP.PublicURL),
		cfg.OIDC.SessionTTL).
		// `/me` returns the current project: the interface needs it to compose its URLs, and the list
		// of projects requires `admin`, which it does not necessarily have.
		WithProjects(db).
		WithModules(modules)
	// The checks attached to a project need to translate the name the URL carries: it is what
	// says in which scope the role is checked.
	middleware := dashhttp.NewMiddleware(sessions, authorizer, logger).WithProjects(db).WithModules(modules)
	runsHandler := dashhttp.NewRunsHandler(db).
		WithTektonDashboard(cfg.Tekton.DashboardURL).
		// The directory translates the requester's OIDC subject into a name: it is "who launched"
		// that one reads on a run screen, not an identifier.
		WithDirectory(db, logger)
	// The log reader lives on the `api` side and reads storage: the collector writes from the
	// `worker`, and the two processes have no meeting point other than MinIO and PostgreSQL.
	logsHandler := dashhttp.NewLogsHandler(logs.NewReader(storage))

	queueHandler := dashhttp.NewQueueHandler(db, pipelines).
		// The launch form's lists: release tags and published images. The registry's prefix comes
		// from the same configuration as the one used to read the tags — deducing it elsewhere would
		// make what is offered diverge from what is pulled.
		WithSuggestions(db, cfg.Registry.Project, logger).
		WithEnvironmentSuggestions(mse.EnvironmentSuggestions(pipelines, modules, db))
	// The name of the promoted repository comes from the registry's configuration: it is the same
	// prefix as the one used to read the tags, and deducing it elsewhere would make reading and
	// promotion diverge.
	// The registry is optional: with no credentials, the release page displays the tags without
	// their digests and says so. Failing the start-up would deprive the dashboard of everything
	// else over a secondary piece of information.
	var images dashhttp.Images
	if client, err := registry.Open(cfg.Registry); err != nil {
		if !errors.Is(err, registry.ErrNotConfigured) {
			return fmt.Errorf("registry access: %w", err)
		}
		logger.Warn("registry not configured", "reason", "image digests will not be readable")
	} else {
		images = client
	}

	// The MSE plugin's tables, read by its routes and supplied to the core as functions.
	mseBackend := mse.NewBackend(db)

	releasesHandler := dashhttp.NewReleasesHandler(
		db, pipelines, authorizer, images, storage, cfg.Registry.Image, cfg.Registry.Project).
		// The same weighting as the test manager: the list's badge and a release's screen display the
		// same figure.
		WithScoreCatalog(pipelines).
		WithArtifactReader(db).
		// The "deployed on" badge and the requested promotions: the core no longer reads the plugin's
		// tables, which return nothing when it is switched off.
		WithDeployments(mse.Deployments(mseBackend.Store, modules)).
		WithPromotions(mse.Promotions(mseBackend.Store, modules)).
		WithTrackerURL(cfg.Tracker.URL).
		// Opening a release's plan, when it is created.
		//
		// An engine on the `api` side, and it is not a second scheduler: opening a plan writes a run
		// and queues jobs, which this handler already did. The worker keeps exclusive charge of what
		// **leaves** — creating the PipelineRuns — and of advancing concluded steps.
		WithPlans(worker.NewReleasePlanner(
			worker.NewEngine(db, worker.NewReleaseRunner(pipelines, db, logger), logger).
				WithArtifacts(db, worker.ProductOf(pipelines)),
			pipelines, db, cfg.Registry.Project, logger))

	// The MSE plugin's routes: targets, promotion, environments. Always mounted, and refused per
	// route when the module is switched off. The deployer has no client: planning a promotion
	// does not need the cluster.
	mseHandler := mse.NewHandler(mseBackend, pipelines, authorizer,
		flux.NewDeployer(nil, flux.DefaultPromotionKind, ""), logger)

	// The forge is optional: with no token, the release creation form falls back to free-text
	// entry of the git_ref and says so. The client is read-only by transport, like Redmine's.
	var forgeReader dashhttp.ForgeReader
	if cfg.Forge.Configured() {
		forgeClient, err := gitea.New(gitea.Options{
			URL: cfg.Forge.URL, Token: cfg.Forge.Token,
			Repository: cfg.Forge.Repository, Timeout: cfg.Forge.Timeout,
		})
		if err != nil {
			return fmt.Errorf("client Gitea: %w", err)
		}
		forgeReader = forgeClient

		// The writing client sets a release's Git tag. Distinct from the reading client, whose
		// transport refuses anything that is not GET: relaxing it would have let a badly written read
		// call write.
		//
		// Its construction does not check the token's scope — only a real write would say. A
		// read-only token therefore produces a reservation at the first release, not a failure at
		// start-up.
		writer, err := gitea.NewWriter(gitea.Options{
			URL: cfg.Forge.URL, Token: cfg.Forge.Token,
			Repository: cfg.Forge.Repository, Timeout: cfg.Forge.Timeout,
		})
		if err != nil {
			return fmt.Errorf("Gitea writing client: %w", err)
		}
		releasesHandler = releasesHandler.WithForge(forgeClient, writer)

		logger.Info("forge configured", "url", cfg.Forge.URL, "repo", cfg.Forge.Repository)
	} else {
		logger.Warn("forge not configured",
			"reason", "FORGE_URL, FORGE_TOKEN or FORGE_REPO missing",
			"consequence", "the git_ref of releases will be typed by hand")
	}

	server := dashhttp.NewServer(cfg.HTTP, logger,
		dashhttp.NewAPIRouter(authHandler, runsHandler, logsHandler, queueHandler,
			releasesHandler, dashhttp.NewBenchHandler(db),
			// The score's weighting comes from the catalogue: it was accepted by the client and stays
			// adjustable without a redeployment.
			dashhttp.NewTestsHandler(db, pipelines),
			// Redmine's URL serves to build the outgoing links. Absent, the links are omitted rather than
			// rendered broken.
			dashhttp.NewVersionsHandler(db, cfg.Tracker.URL),
			dashhttp.NewSyncAdminHandler(db),
			dashhttp.NewForgeHandler(forgeReader),
			dashhttp.NewAdminHandler(db),
			// The access test uses the forge's token, with one client per repository: the configuration's
			// is bound to its own, and the test bears precisely on another. With no token, the screen
			// says so rather than returning a failure one would take for a permission problem.
			projectsHandler(cfg, db, modules),
			// The cipher may be nil: with no key, the settings are read and enabled, only writing a
			// sensitive field is refused — with a message that names the variable, in the place where one
			// types it.
			dashhttp.NewPluginsHandler(pluginRegistry, db, cipher, logger),
			// The same catalogue as the queue: the screen shows what the instance can launch, and a
			// second load could show something else.
			dashhttp.NewProjectPipelinesHandler(db, pipelines, logger),
			// The fact feed, read-only. Nothing reacts to it yet: it serves to answer "why did nothing
			// happen?", a question that only makes sense once one can see what was actually observed.
			dashhttp.NewEventsHandler(db),
			// The same catalogue as the queue: the editor offers what the instance can launch, and a
			// second load could offer something else.
			dashhttp.NewAutomationsHandler(db, pipelines, pluginRegistry, logger),
			// What a reaction actually did. An engine one cannot debug is worse than frozen behaviour.
			dashhttp.NewPlanRunsHandler(db),
			middleware, mseHandler.Routes, mse.NewSignoffsHandler(mseBackend, logger).Routes),
		dashhttp.ReadinessCheck{Name: "postgres", Check: db.Ping},
		// Distinct from the ping: PostgreSQL may answer although the migration Job has not run yet.
		// Without this check, the pod would declare itself ready then answer 500 on every missing
		// table.
		dashhttp.ReadinessCheck{Name: "schema", Check: db.CheckSchema},
		dashhttp.ReadinessCheck{Name: "schema-mse", Check: msestorage.CheckSchema(db.Pool())},
		dashhttp.ReadinessCheck{Name: "minio", Check: storage.Ping})

	return server.Run(ctx)
}

// exportRegistry opens the registry for generating deliverables.
//
// Nil when it is not configured: a delivery note stays producible without digests, provided
// it names what it could not check — which the document does. Refusing to produce it would be
// worse.
func exportRegistry(cfg config.Config, logger *slog.Logger) worker.Registry {
	client, err := registry.Open(cfg.Registry)
	if err != nil {
		if !errors.Is(err, registry.ErrNotConfigured) {
			logger.Error("registry access", "error", err)
		}
		logger.Warn("registry not configured",
			"consequence", "the delivery notes will carry no image digest")
		return nil
	}
	return client
}

// secureCookies allows insecure cookies only over http, that is, in local development.
// Deducing it from the scheme avoids a flag one forgets to set in production.
func secureCookies(publicURL string) bool {
	return !strings.HasPrefix(publicURL, "http://")
}

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("starting", "role", cfg.Role, "namespace", cfg.Tekton.Namespace)

	if err := store.WaitForDatabase(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}
	// Then the schema. The worker reconciles at start-up — it catches up the runs concluded
	// during its absence — and that reconciliation touches tables the migration Job may not have
	// created yet.
	if err := store.WaitForSchema(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}
	// Then the MSE plugin's, migrated just after the core by the same Job.
	if err := msestorage.WaitForSchema(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	// The worker **opens** the settings' sensitive fields, it seals none: it is what presents an
	// API key to the tracker at every pass.
	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	pluginRegistry, err := builtin.Registry()
	if err != nil {
		return fmt.Errorf("built-in plugins: %w", err)
	}
	if err := db.EnsurePlugins(ctx, pluginIDs(pluginRegistry)); err != nil {
		logger.Error("seeding the plugins", "error", err)
	}
	seedMSESettings(ctx, db, logger)

	// `versions.normalized_name` is a derived column: changing the normalisation rule makes it
	// stale, and the matching would then compare a form computed with the new rule to a form
	// written with the old one. The worker alone does it — the api runs with several replicas,
	// and three concurrent renormalisations would write the same thing three times.
	corrected, err := db.RenormalizeVersions(ctx)
	if err != nil {
		return err
	}
	if corrected > 0 {
		logger.Info("version names renormalised", "corrigees", corrected)
	}

	storage, err := blob.Open(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	logger.Info("object storage", "bucket", storage.Bucket())

	// Cluster access is checked at start-up, like the database and storage: a worker starting
	// without being able to read the PipelineRuns observes nothing and does not say so.
	cluster, err := tekton.Open(cfg.Tekton)
	if err != nil {
		return err
	}

	pipelines, err := loadCatalogue(cfg)
	if err != nil {
		return err
	}

	// Checked against the cluster under the lease, once the capabilities' definitions are installed:
	// checking here refused to start on a fresh cluster, before the dashboard could install them.
	logger.Info("catalogue loaded", "kinds", pipelines.Kinds(), "cap", pipelines.MaxConcurrentRuns)

	// The projects that never declared their automation receive it, composed from their targets.
	// Without it, all of them would stop building on the deployment of this version.
	//
	// **After the catalogue's validation**, and not before: the catch-up composes steps naming
	// kinds, and freezing them from a catalogue at odds with the cluster would write into the
	// database plans that will never start. A catch-up does not come round again.
	//
	// Here and not in the `api`, for the same reason as the renormalisation: the api runs with
	// several replicas, and three concurrent catch-ups would write the same thing three times.
	// The `api` reads the declaration, it does not compose it.
	if err := worker.NewAutomationCatchUp(db, pipelines, logger).Run(ctx); err != nil {
		return err
	}

	// The MSE plugin decides everything that follows: Flux, PortalData, observing the
	// environments. Switched off, the worker starts on a cluster carrying neither Flux's CRDs nor
	// the portal's.
	//
	// **Read at start-up**, unlike in the `api`: opening a Flux client or closing it along the
	// way would touch the lease and the observation loop. Flipping the switch therefore needs a
	// worker restart to take effect here.
	pluginSettings, err := db.PluginSettingsAll(ctx)
	if err != nil {
		return err
	}
	mseSettings := pluginSettings[plugin.ModuleMSE]
	var environments *mse.Observer
	if !mseSettings.Enabled {
		logger.Info("module MSE inactif",
			"consequence", "neither Flux, nor PortalData, nor environment observation")
	} else {
		// Two distinct dependencies, and two distinct clients: one says what the deployment applied,
		// the other what the application reports about itself. It is their gap that gives the
		// environments screen its value, and merging them would make it disappear.
		//
		// The attestation is **optional**: a cluster without the portal's CRD is a valid
		// configuration, and the dashboard falls back there to what the deployment declares.
		var attester mse.Attester
		switch portalClient, err := portaldata.Open(cluster.RESTConfig); {
		case errors.Is(err, portaldata.ErrAbsent):
			logger.Info("no attestation source in this cluster",
				"consequence", "the environments' state will be the one the deployer declares")
		case err != nil:
			return err
		default:
			attester = portalClient
			// `api_version` and not `version`: the logger already carries a `version` attribute — the
			// binary's — and two keys of the same name in one record make it unreadable.
			logger.Info("attestation source", "api_version", portalClient.Version())
		}
		environments = mse.NewObserver(msestorage.New(db.Pool()), attester, logger)

		// The environments are written for each project that has the module, read at start-up like
		// the instance's switch. Each is observed by the Flux of its namespace: the project's,
		// failing that the instance's, failing that the configuration's.
		projects, err := db.ListProjects(ctx)
		if err != nil {
			return err
		}
		projectModules := dashhttp.NewModules(pluginRegistry, db, logger)
		// One client per distinct namespace, shared by the projects that share it: the client
		// discovers the API versions served, and doing it once per project would multiply the calls
		// for the same answer.
		deployers := map[string]*flux.Deployer{}
		var declared []mse.ProjectEnvironments
		enabled := map[string]bool{}
		var slugs []string
		for _, project := range projects {
			if !projectModules.EnabledIn(ctx, project.ID, plugin.ModuleMSE) {
				continue
			}
			enabled[project.Slug] = true
			slugs = append(slugs, project.Slug)

			settings, err := db.ResolvedPlugin(ctx, project.ID, plugin.ModuleMSE)
			if err != nil {
				return err
			}
			namespace := strings.TrimSpace(settings.Config["namespace"])
			if namespace == "" {
				namespace = cfg.Flux.Namespace
			}
			deployer, opened := deployers[namespace]
			if !opened {
				// The Flux client discovers the API versions served instead of hard-coding them: one
				// installation may serve `kustomize` and `image` in different versions.
				fluxClient, err := flux.Open(cluster.RESTConfig, namespace)
				if err != nil {
					return fmt.Errorf("Flux client of project %s: %w", project.Slug, err)
				}
				logger.Info("Flux client", "namespace", namespace, "versions", fluxClient.Versions())
				deployer = flux.NewDeployer(fluxClient, flux.DefaultPromotionKind, "")
				deployers[namespace] = deployer
			}

			own := pipelines.EnvironmentsOf(project.Slug)
			if len(own) == 0 {
				logger.Warn("project of the MSE module with no declared environment",
					"project", project.Slug, "consequence", "neither promotion nor observation for it")
			}
			declared = append(declared, mse.ProjectEnvironments{
				ProjectID: project.ID, Slug: project.Slug, Environments: own, Deployer: deployer,
			})
			logger.Info("project of the MSE module", "project", project.Slug, "flux_namespace", namespace,
				"environments", len(own),
				// What its pipelines will clone: a repository that does not match what one believes one is
				// driving is otherwise visible only when the clone fails.
				"kustom_repo", settings.Config["kustomRepoUrl"],
				"kustom_revision", settings.Config["kustomRevision"],
				"kustom_overlay", settings.Config["overlayLocation"])
		}
		for slug := range pipelines.Projects {
			if !enabled[slug] {
				// A section for a project that is absent or has switched the module off: without effect, and
				// saying so avoids hunting for why nothing appears.
				logger.Warn("environments declared for a project without the MSE module",
					"project", slug, "consequence", "ignored")
			}
		}
		logger.Info("projects of the MSE module", "projects", slugs)

		// The guard rail: a promotion suffix matching no ImagePolicy would produce a valid tag,
		// pushed with no error, which Flux would ignore. Better not to start.
		syncCtx, cancelSync := context.WithTimeout(ctx, 30*time.Second)
		err = environments.SyncEnvironments(syncCtx, declared)
		cancelSync()
		if err != nil {
			return err
		}
	}

	identity := worker.LeaderIdentity()
	logger.Info("standing for the lease", "identity", identity, "lease_namespace", cfg.Tekton.LeaseNamespace)

	// Everything that follows runs in a single copy. The Deployment's `replicas: 1` is not enough
	// for that: during a rolling update two pods coexist, and that is the instant when a
	// duplicated scheduler would launch the same pipeline twice.
	return worker.RunAsLeader(ctx, cluster.Kube,
		worker.LeaderConfig{Namespace: cfg.Tekton.LeaseNamespace, Identity: identity},
		logger,
		func(leadingCtx context.Context) error {
			// The collector is stopped before the function returns: the captures in progress must stop
			// writing before another worker takes the lease, otherwise two processes write the same
			// chunks.
			collector := logs.New(tekton.NewPodLogs(cluster), storage, db, nil, logger)
			defer collector.Stop()

			// The ingestion of the load campaigns is wired to the end of the runs: the k6 runner lives
			// outside the cluster and has no return path to the dashboard.
			// What the pipelines will receive, logged before any run. A repository or a Dockerfile that
			// does not match what one believes one is driving is otherwise visible only when the build
			// fails, where the message names a path without saying where it comes from.
			resolved := schedulerConfig(cfg)
			logger.Info("pipeline target",
				// The repository of a job with no project: those that have one clone their own, logged per
				// project above.
				"default_repo", resolved["project.repoUrl"],
				"registry_project", resolved["registry.project"],
				// The kustomization, its revision and the overlay are logged per project, with the Flux
				// namespace: they come from the MSE plugin's settings.
				"git_secret", resolved["pipeline.gitSecret"],
				"registry_secret", resolved["pipeline.registrySecret"],
				// The pipelines' storage is not the dashboard's, and it shows only at the last step of an
				// audit: an hour of completed scans, then a credentials refusal.
				"pipeline_minio", resolved["minio.host"])

			// The database's identity enters the runs' names: without it, a fresh database reassigns job
			// identifiers already carried by PipelineRuns left in the cluster, and the scheduler reads
			// the conflict as "it was already done" without launching anything.
			instance, err := db.InstanceID(ctx)
			if err != nil {
				return err
			}
			logger.Info("database identity", "instance", instance)

			objects := worker.NewObjectStore(storage)
			sink := worker.NewRunSink(db).
				// Finished runs are read by the interpreter of the capability that launched them. A tool
				// that did not conclude is told apart from a clean one by the run's state, which Tekton
				// keeps even when the tool wrote nothing.
				WithFactIngester(worker.NewFactIngester(db, objects, pluginRegistry, logger)).
				// The catalogue makes it possible to chain the verifications of a successful build: without
				// it, observation continues but a built release stays waiting for a test nothing launches.
				WithCatalog(pipelines, resolved["registry.project"]).
				// Without the database's identity, a run left in the cluster after a reset concludes the
				// namesake job of the current database.
				WithInstance(instance).
				// The emission of facts.
				WithEvents(db)
			// A promotion is observed only if the plugin that promotes is there.
			if mseSettings.Enabled {
				sink.WithPromotionFacts(mse.Facts{})
			}

			// The plan engine, which chains the verifications of a successful build and concludes their
			// steps at the end of their job.
			//
			// Assembled after the sink because it depends on it: the runner needs the catalogue and the
			// queue, which the sink already carries.
			// The outgoing calls, with their target-controlled transport. The permissions come from the
			// deployment and are false by default: a URL typed into the interface is issued from the
			// cluster.
			actions := worker.NewActionRunner(
				action.NewCatalog(pluginRegistry.Plugins()),
				webhook.New(webhook.Options{
					AllowPrivate:  cfg.Webhooks.AllowPrivate,
					AllowInsecure: cfg.Webhooks.AllowInsecure,
					Timeout:       cfg.Webhooks.Timeout,
				}),
				secretResolver{registry: pluginRegistry, store: db, cipher: cipher}, logger).
				// The steps a plugin supplies. They do not use the transport above: their target is not typed
				// into a form but read from the plugin's configuration, and the address check exists
				// precisely because elsewhere it is. The token is the plugin's, opened for the duration of
				// the call.
				WithPlugins(worker.NewPluginActionRunner(
					pluginRegistry, db, cipher, cfg.Forge.Timeout, logger))
			if cfg.Webhooks.AllowPrivate || cfg.Webhooks.AllowInsecure {
				logger.Warn("outgoing calls widened",
					"private_targets", cfg.Webhooks.AllowPrivate,
					"clair", cfg.Webhooks.AllowInsecure,
					"consequence", "a badly set reaction can reach the internal network")
			}

			// The engine, and the anchoring of the life cycle on the stages with a role: it is the
			// settling of a stage that advances the release, where the observer counted the builds job by
			// job.
			engine := worker.NewVerificationEngine(sink, db, actions, logger).
				WithStages(worker.NewReleaseLifecycle(db, logger)).
				// What the steps produce, collected when they conclude and offered to the following stages
				// under `{artifacts.<step>.<name>}`.
				WithArtifacts(db, worker.ProductOf(pipelines))
			// The routing: what links an observed fact to the reactions a project declared. Without it,
			// facts are written and nobody reacts to them.
			dispatcher := worker.NewDispatcher(db, engine, logger)
			sink = sink.WithEngine(engine).WithDispatcher(dispatcher)
			observer := tekton.NewObserver(cluster, sink, sink, worker.NewLogAdapter(collector), logger)

			// The artifacts' write URLs are minted here: the storage layout belongs to the dashboard, and
			// that is what takes the MinIO secret out of the test Tasks.

			// The settings the MSE plugin adds to a project's pipelines.
			scheduler := worker.NewSchedulerFromCatalog(
				db, cluster, pipelines, resolved, logger).
				WithUploads(storage).
				WithInstance(instance).
				WithPluginVersions(pluginVersions()).
				// The project's deploy key replaces the shared key when the run is composed. A project with
				// no key of its own falls back to the shared one.
				WithProjectKeys(db).
				// The repository this project clones, read from the database: a second project does not build
				// the first one's.
				WithProjectRepositories(db, os.Getenv("FORGE_SSH_PORT")).
				// And the settings the MSE plugin adds for this project: its kustomization, its overlay.
				// Nothing when the module is switched off for it.
				WithProjectSettings(mse.PipelineSettings(db)).
				// A plugin switched off by an administrator stops its pipelines.
				WithPluginGate(pluginGate(db))

			// The deliverables go through the queue like the rest, but are produced here: generating a
			// delivery note reads the database and writes an object. Making it cross Tekton would cost a
			// pod and a workspace to produce two pages.
			scheduler.Handle(worker.NewExporter(db, storage, exportRegistry(cfg, logger), cfg.Registry.Image, logger))

			// The release note, for the same reason: it reads Redmine, the forge and the database. With
			// no forge configured, the task is not registered and the note jobs fail saying so — rather
			// than composing empty notes without explaining why.
			if cfg.Forge.Configured() {
				forgeClient, err := gitea.New(gitea.Options{
					URL: cfg.Forge.URL, Token: cfg.Forge.Token,
					Repository: cfg.Forge.Repository, Timeout: cfg.Forge.Timeout,
				})
				if err != nil {
					return fmt.Errorf("worker's Gitea client: %w", err)
				}
				scheduler.Handle(worker.NewNoteBuilder(
					forgeClient, db, cfg.Tracker.ReviewStatuses, logger))
				logger.Info("release note",
					"review_statuses", cfg.Tracker.ReviewStatuses)
			} else {
				logger.Warn("forge not configured: the release notes will not be composed")
			}

			// Observation and scheduling run in parallel: the second depends on the first to learn that a
			// run is finished, and serialising them would block the queue behind the cache's
			// synchronisation.
			// The purge of lapsed sessions and abandoned authentication transactions: its loop was
			// missing, and `PurgeExpired` was documented as called periodically with nobody calling it.
			purger := worker.NewPurger(
				auth.NewSessionStore(db.Pool(), cfg.OIDC.SessionTTL),
				auth.NewDBFlowStore(db.Pool(), 10*time.Minute),
				logger)

			// One slot per loop, and no fewer: only the **first** error is read — a dead loop must make
			// the lease be lost rather than let the other run half-way — and the following ones are
			// therefore never taken off.
			//
			// On a saturated channel, they would block their goroutine forever, including at shutdown
			// where each returns `ctx.Err()`. The process would still exit, but leaving behind loops that
			// were never able to conclude — and a goroutine profile that accuses a leak where there is
			// only a channel that is too short.
			errs := make(chan error, 8)
			// The bound capabilities' definitions, installed and checked before the first launch. Under the
			// lease like the rest: two workers putting the same definition in place would rewrite each
			// other's.
			reconciler := capabilityReconciler(cluster, cfg, pipelines, scheduler, logger)
			reconciler.Settle(leadingCtx)
			go func() { errs <- reconciler.Run(leadingCtx) }()

			go func() { errs <- observer.Run(leadingCtx) }()
			go func() { errs <- scheduler.Run(leadingCtx) }()
			if environments != nil {
				go func() { errs <- environments.Run(leadingCtx) }()
			}
			go func() { errs <- purger.Run(leadingCtx) }()

			// Setting the deploy keys. Under the lease like the rest: two workers setting the same key
			// would replace each other's, and the public half recorded would no longer be that of the
			// Secret deposited.
			keys := projectKeys(cluster, cfg, pipelines, logger)
			go func() {
				errs <- worker.NewKeyReconciler(db, keys, logger).Run(leadingCtx)
			}()

			// The probes, which produce a plugin's facts. Under the lease like the rest: two workers
			// querying the same instance would double the load on a third-party service for an identical
			// result.
			//
			// A probe is queried only for the projects whose plugin is active, each at its own cadence,
			// and its first pass triggers nothing — enabling something does not wake the past.
			probes := worker.NewProbeRunner(pluginRegistry, db, cipher, cfg.Probes.Interval, logger,
				// The tracker synchronisation is a probe too, one per tracker plugin: the common loop resolves
				// its settings, unseals its key, keeps its cursor and isolates its failures.
				append(worker.PluginProbes(pluginRegistry, cfg.Forge.Timeout),
					worker.TrackerProbes(pluginRegistry, db, cfg.Tracker.Timeout, logger)...)...).
				WithDispatcher(dispatcher)
			go func() { errs <- probes.Run(leadingCtx) }()

			// The first error wins: a dead loop must make the lease be lost rather than let the other run
			// half-way.
			return <-errs
		})
}

// schedulerConfig exposes to the scheduler the values the catalogue references by key.
//
// The table is explicit rather than derived by reflection: what a catalogue can inject into a
// PipelineRun must be a list one can read, not the whole of the dashboard's configuration.
// seedMSESettings sets the MSE plugin's settings that the environment supplies.
//
// Fills in only what is missing: the database is authoritative from the first value written,
// and a setting changed from the administration screen must not be taken back at the next
// restart. Without this seeding, an updated instance would see its pipelines refuse to start
// — the core no longer exposes these keys.
//
// Logged and not fatal: the dashboard starts, and the administration screen allows setting
// these values by hand.
func seedMSESettings(ctx context.Context, db *store.DB, logger *slog.Logger) {
	settings, err := db.PluginSettingsAll(ctx)
	if err != nil {
		logger.Error("seeding the MSE plugin's settings", "error", err)
		return
	}
	entry, declared := settings[plugin.ModuleMSE]
	if !declared {
		// The row does not exist yet: writing it here would set it switched off, whereas it is
		// `EnsurePlugins` that decides its initial state.
		return
	}

	config := make(map[string]string, len(entry.Config))
	maps.Copy(config, entry.Config)
	var posed []string
	for field, value := range mseSeedSettings() {
		if strings.TrimSpace(config[field]) != "" {
			continue
		}
		config[field] = value
		posed = append(posed, field)
	}
	if len(posed) == 0 {
		return
	}
	sort.Strings(posed)

	entry.PluginID = plugin.ModuleMSE
	entry.Config = config
	if err := db.SetPluginSettings(ctx, entry, "seeding"); err != nil {
		logger.Error("seeding the MSE plugin's settings", "error", err)
		return
	}
	logger.Info("MSE plugin settings seeded", "champs", posed)
}

// appRepositoryURL returns the application repository the configuration names.
//
// Derived from `GITEA_REPO`: it is the same repository whose branches the form offers, and
// naming it twice let one move without the other. `FORGE_MSE_REPO_URL` stays an explicit
// exception, not the current setting.
func appRepositoryURL(cfg config.Config) string {
	if explicit := strings.TrimSpace(os.Getenv("FORGE_MSE_REPO_URL")); explicit != "" {
		return explicit
	}
	if derived := cfg.Forge.CloneURL(os.Getenv("FORGE_SSH_PORT")); derived != "" {
		return derived
	}
	return "ssh://git@forge.cadoles.com:2222/CNOUS/mse.git"
}

// projectSeedDefaults returns what a project receives when it is seeded: its repository, its
// Dockerfile, its build context.
//
// A seeding and not a pipeline configuration: the database is authoritative from the first
// start-up, and a release's plan reads these values **on the project**.
func projectSeedDefaults(cfg config.Config) map[string]string {
	return map[string]string{
		"repoUrl":    appRepositoryURL(cfg),
		"dockerfile": envOrDefault("MSE_DOCKERFILE", "./misc/k8s/images/portal/Dockerfile"),
		// Build context, distinct from the Dockerfile: `mse` puts its own in a subtree while building
		// from the root.
		"buildContext": envOrDefault("MSE_BUILD_CONTEXT", "."),
	}
}

// mseSeedSettings returns the MSE plugin's settings that the environment supplies.
//
// A **seeding**, and not a read: these values set the first setting, then the database is
// authoritative. Re-reading them at every start-up would take back what an administrator has
// changed since.
func mseSeedSettings() map[string]string {
	return mse.PipelineDefaults(
		envOrDefault("FORGE_MSE_KUSTOM_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-kustom.git"),
		// The branch of the deployment infrastructure to audit. Without it, the audit fell back to
		// the pipeline's default — `unstable`, `CNOUS/mse-kustom`'s convention — and failed at the
		// clone on any repository that does not carry it: "couldn't find remote ref unstable".
		envOrDefault("FORGE_MSE_KUSTOM_REVISION", "unstable"),
		envOrDefault("FORGE_E2E_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-tests-e2e.git"),
		envOrDefault("MSE_OVERLAY_LOCATION", "overlays/pipeline"),
	)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func schedulerConfig(cfg config.Config) map[string]string {
	envOr := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}

	return map[string]string{
		"tekton.namespace": cfg.Tekton.Namespace,
		// Names of the secrets mounted on the workspaces. The dashboard **names** the credentials, it
		// never handles them: `secrets` is deliberately absent from its RBAC
		// (docs/09-security.md), and a key passed as a parameter would land in the PipelineRun's
		// spec. The same principle as the presigned URLs.
		//
		// The secrets live in the PipelineRuns' namespace, not in the dashboard's: that is where
		// Tekton mounts them.
		"pipeline.gitSecret":      envOr("PIPELINE_GIT_SECRET", "git-credentials"),
		"pipeline.registrySecret": envOr("PIPELINE_REGISTRY_SECRET", "regcred"),
		// Secret for pulling the tooling images, distinct from the one used to push: the pod pulls
		// from `cnous-mse`, `crane` pushes to the release's project. Two scopes, potentially two
		// accounts.
		"pipeline.imagePullSecret": envOr("PIPELINE_IMAGE_PULL_SECRET", "regcred"),
		// The repository of a job that names no project. Those that name one clone **their own**,
		// read from the database by the scheduler.
		"project.repoUrl": appRepositoryURL(cfg),
		// The old key, which named MSE in the core. Served for as long as a deployed catalogue can
		// still name it.
		"forge.mse.repoUrl": appRepositoryURL(cfg),
		// The kustomization, its revision, the E2E tests and the overlay are no longer here: they
		// name MSE, and come from its plugin's settings, where a project overrides them.
		// The security audit reads its Semgrep rules and its policies in `mse-tekton`: it is a
		// repository it clones, just like the application and the deployment infrastructure.
		"forge.tekton.repoUrl": envOr("FORGE_TEKTON_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-tekton.git"),
		"registry.project":     envOr("REGISTRY_PROJECT", "reg.cadoles.com/cnous-mse"),
		// The Dockerfile and the build context are no longer here: they describe a repository, each
		// project carries its own in the database, and a release's plan already reads them there.
		// They now serve only to **seed** a project, and `projectSeedDefaults` returns them for that.
		// The pipelines' bucket is not the dashboard's: `mse-pipeline` carries the artifacts the
		// Tasks publish, `arkhitekton` what the dashboard writes itself.
		"minio.host":           envOr("PIPELINE_MINIO_HOST", "https://nuo-s3.nuonet.fr"),
		"minio.pipelineBucket": envOr("PIPELINE_MINIO_BUCKET", "mse-pipeline"),
	}
}

func runMigrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("schema migration", "dsn", cfg.Redacted().Database.DSN)

	// The migration Job starts at the same time as the database in a fresh deployment: without
	// this wait, it fails, Kubernetes restarts it, and every deployment leaves a pod in `Error`
	// behind it.
	if err := store.WaitForDatabase(ctx, cfg.Database, databaseWait, logger); err != nil {
		return err
	}

	if err := store.Migrate(ctx, cfg.Database, logger); err != nil {
		return err
	}
	// The MSE plugin after the core: its tables reference `releases` and `jobs`. Applied whether
	// the module is switched on or not: the switch is flipped without a redeployment, and the
	// schema must already be there.
	if err := msestorage.Migrate(ctx, cfg.Database, logger); err != nil {
		return err
	}

	state, err := store.MigrationState(ctx, cfg.Database)
	if err != nil {
		return err
	}
	for _, migration := range state {
		logger.Info("migration", "file", migration.Name, "applied", migration.Applied)
	}

	logger.Info("schema up to date", "count", len(state))

	// The bootstrap follows the migration, in the same Job: the schema it fills has just been
	// applied, and giving it its own trigger would leave a window in which the api starts on a
	// database with no administrator — nobody could then grant anything from the interface.
	//
	// A step already applied is never replayed, even if the configuration has changed since. That
	// is what makes the database sole master after the first pass: a repository removed from the
	// interface does not come back, and a role taken from someone is not granted again by virtue
	// of a redeployment.
	return bootstrap(ctx, cfg, logger)
}

// bootstrap applies the seed data, once.
//
// It opens its own connection rather than receiving one: `runMigrate` works up to here with
// no pool, each operation opening its own, and making it carry one for the single step that
// needs it would extend its lifetime to the whole Job.
func bootstrap(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	registry, err := builtin.Registry()
	if err != nil {
		return fmt.Errorf("built-in plugins: %w", err)
	}
	plugins, err := configuredPlugins(cfg, cipher, registry, logger)
	if err != nil {
		return err
	}
	trackerPluginID, _ := providers(registry)

	report, err := db.Bootstrap(ctx, store.BootstrapSpec{
		Grants:          configuredGrants(cfg),
		Projects:        configuredProjects(cfg),
		Plugins:         plugins,
		TrackerPluginID: trackerPluginID,
	})

	// The report is logged before the error: one step may have completed while another failed,
	// and keeping quiet about it would suggest the whole bootstrap failed.
	if report.GrantsApplied > 0 || report.ProjectsApplied > 0 ||
		report.TrackersApplied > 0 || report.PluginsApplied > 0 {
		logger.Info("bootstrap applied",
			"roles", report.GrantsApplied, "repositories", report.ProjectsApplied,
			"suivis", report.TrackersApplied, "plugins", report.PluginsApplied)
	}
	if len(report.Skipped) > 0 {
		logger.Info("bootstrap already done, steps skipped",
			"steps", report.Skipped, "source", "the database is authoritative")
	}
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	return nil
}

// configuredGrants returns the roles declared in the configuration.
//
// Two variables, and the second is no duplicate: `BOOTSTRAP_ADMINS` can only say
// "administrator", which is enough to unblock a fresh database but forces everything else
// through the interface. `BOOTSTRAP_USERS` carries the role with the account —
// `alice:releaser` — and allows describing a whole team before its first sign-in.
//
// An account named in both takes the more explicit role: a declaration that states `viewer`
// must not be overwritten by a shortcut that says "administrator".
func configuredGrants(cfg config.Config) []store.Grant {
	grants := make([]store.Grant, 0, len(cfg.OIDC.BootstrapAdmins)+len(cfg.OIDC.BootstrapUsers))

	for identifier, role := range cfg.OIDC.BootstrapUsers {
		grants = append(grants, store.Grant{Identifier: identifier, Role: role})
	}
	for _, identifier := range cfg.OIDC.BootstrapAdmins {
		if _, declared := cfg.OIDC.BootstrapUsers[identifier]; declared {
			continue
		}
		grants = append(grants, store.Grant{Identifier: identifier, Role: string(auth.RoleAdmin)})
	}

	// A map's order is not stable, and a bootstrap must produce the same thing at every run — if
	// only so that its logs can be compared.
	sort.Slice(grants, func(i, j int) bool { return grants[i].Identifier < grants[j].Identifier })
	return grants
}

// databaseWait bounds the wait for the database at a migration's start-up.
//
// Two minutes: enough to cover the initialisation of a fresh PostgreSQL — creating the
// cluster, first start-up — without letting a Job run indefinitely against a database that
// does not exist.
const databaseWait = 2 * time.Minute

// startupWait bounds the wait for the database at the start-up of the `api` and the `worker`.
//
// Deliberately **shorter** than the startup probe's budget — 20 attempts every 3 seconds,
// that is 60 s. That is what preserves the property that counts: if the database is really
// absent, the probe kills the pod and Kubernetes restarts it, as before.
//
// The wait serves only the frequent and benign case: the database comes back at the same time
// as the pod. Without it, the process exited with an error, was restarted, and succeeded on
// the second attempt — a `RESTARTS 1` on every fresh deployment, and a `skaffold run`
// returning 1 on a healthy deployment. The same defect as the migration Job's, in another
// form.
const startupWait = 45 * time.Second

// runReprocess replays the results from the raw artifacts.
//
// A one-off role and not a background loop: it is launched after fixing a parser, so that the
// history reflects the current code rather than the code that ran on the day of the run. The
// raw data kept is what makes this rebuild possible without re-running a single pipeline.
func runReprocess(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("reprocess", flag.ContinueOnError)
	releaseTag := flags.String("release", "", "limit the replay to one release")
	dryRun := flags.Bool("dry-run", false, "list without erasing or rebuilding")
	acceptDrift := flags.Bool("accept-drift", false, "replay runs another plugin version read")
	if err := flags.Parse(args); err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	storage, err := blob.Open(ctx, cfg.Storage)
	if err != nil {
		return err
	}

	var releaseID int64
	if *releaseTag != "" {
		// The tag is unique only within a project: the replay therefore targets the one being built,
		// like the rest of the dashboard as long as the routes do not carry the project.
		project, err := db.BuildableProject(ctx)
		if err != nil {
			return err
		}
		release, err := db.GetRelease(ctx, project.ID, *releaseTag)
		if err != nil {
			// Refuse rather than replay everything: a misspelled tag would otherwise trigger a replay of
			// the whole history, that is, the erasure of everything projected.
			return fmt.Errorf("release %q: %w", *releaseTag, err)
		}
		releaseID = release.ID
	}

	capabilities, err := builtin.Registry()
	if err != nil {
		return fmt.Errorf("built-in plugins: %w", err)
	}
	reader := worker.NewFactIngester(db, worker.NewObjectStore(storage), capabilities, logger)
	reprocessor := worker.NewReprocessor(db, reader, pluginVersions(), logger)

	logger.Info("replay started",
		"release", *releaseTag, "simulation", *dryRun, "scope",
		scope(*releaseTag))

	report, err := reprocessor.Run(ctx, worker.ReprocessOptions{
		ReleaseID: releaseID, DryRun: *dryRun, AcceptDrift: *acceptDrift,
	})
	if err != nil {
		return err
	}

	logger.Info("replay finished",
		"runs examined", report.Examined, "runs replayed", report.Replayed,
		"reports erased", report.Deleted, "failures", report.Failed, "refused", report.Refused)

	if len(report.Failed) > 0 {
		// A non-zero exit: a partial replay must not pass for a success in a pipeline or an
		// operations script.
		return fmt.Errorf("%d run(s) not replayed: %s",
			len(report.Failed), strings.Join(report.Failed, ", "))
	}
	return nil
}

func scope(releaseTag string) string {
	if releaseTag == "" {
		return "the whole history"
	}
	return "one release"
}

// runCheckTracker checks access to the tracker the deployment's variables name, then stops.
//
// A diagnostic role, to be sure a deposited key works before a screen depends on it. It does
// one read only, and the Redmine plugin's transport structurally forbids any method other than
// GET and HEAD, which makes this check risk-free on the production instance.
//
// The key is never logged. What is: the URL, the account it belongs to, and its `admin` flag.
func runCheckTracker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	registry, err := builtin.Registry()
	if err != nil {
		return fmt.Errorf("built-in plugins: %w", err)
	}
	trackerID, _ := providers(registry)
	tracker, err := registry.Plugin(trackerID)
	if err != nil || tracker.Tracker == nil {
		return fmt.Errorf("no tracker plugin in this binary")
	}
	client, err := tracker.Tracker.Connect(capability.Target{
		Settings: capability.Settings{"url": cfg.Tracker.URL, "api_key": cfg.Tracker.APIKey},
		Timeout:  cfg.Tracker.Timeout,
	})
	if err != nil {
		return fmt.Errorf("tracker client: %w", err)
	}

	account, err := client.CheckAccess(ctx)
	if err != nil {
		return fmt.Errorf("checking tracker access: %w", err)
	}

	logger.Info("tracker access checked",
		"plugin", tracker.ID,
		"url", cfg.Tracker.URL,
		"project", cfg.Tracker.Project,
		"login", account.Login,
		"user_id", account.ID,
		// Reported because it deserves to be seen: an administrator key gives far more than the
		// dashboard needs. The transport's read-only nature protects against a coding mistake, not
		// against an over-powerful account.
		"admin", account.Admin)

	if account.Admin {
		logger.Warn("this key belongs to an administrator account; " +
			"a read-only account on the project would be enough")
	}
	return nil
}

// projectsHandler assembles the declaration of the driven repositories.
//
// A single key now, the project's, set by the worker: the API runs with no cluster token,
// deliberately, because it is the exposed surface (docs/09-security.md). The **shared** key
// that coexisted with it has been removed — a single key added to several repositories makes
// its revocation an outage for all of them at once, and says nothing about who cloned what.
func projectsHandler(cfg config.Config, db *store.DB, modules *dashhttp.Modules) *dashhttp.ProjectsHandler {
	handler := dashhttp.NewProjectsHandler(db).
		WithLibrary(referenceLibrary()).
		WithForgeURL(cfg.Forge.URL).
		WithImageRegistry(cfg.Registry.Project).
		// The same deployer as the one that promotes, and with no client: the question asked — "can
		// you route this scheme?" — does not need the cluster. Asked only when the MSE plugin is
		// switched on: without it, nothing promotes, and no scheme has to be refused.
		WithSchemeCheck(func(ctx context.Context, scheme versionpkg.Scheme) error {
			if !modules.Enabled(ctx, plugin.ModuleMSE) {
				return nil
			}
			return flux.NewDeployer(nil, flux.DefaultPromotionKind, "").CheckScheme(scheme)
		})

	if cfg.Forge.URL == "" || cfg.Forge.Token == "" {
		return handler
	}
	return handler.WithProbe(gitea.Prober{
		URL: cfg.Forge.URL, Token: cfg.Forge.Token, Timeout: cfg.Forge.Timeout,
	})
}

// configuredProjects returns the repositories the configuration names.
//
// Four, and they do not play the same role: the application is built, the other three are
// cloned — the kustomization by the integration tests, the pipelines by the audit that reads
// its rules there, the E2E campaign by itself. Only the first therefore carries a Dockerfile.
//
// The application's stages are named here, and nowhere else. They lived in Go constants —
// `portal`, `portal-cli`, `portal-dev` — that the dashboard walked to know what to build.
// They describe a given repository, not the product: their place is in the seeding, which is
// precisely what installs a truth specific to a deployment before the database takes over.
func configuredProjects(cfg config.Config) []store.Project {
	resolved := schedulerConfig(cfg)
	// The repositories the MSE plugin sets: the seeding of the projects names them just like the
	// others, and it happens before the database carries them.
	seeds := mseSeedSettings()
	project := projectSeedDefaults(cfg)

	promoted, held := true, false
	targets := []store.Target{
		// The HTTP runtime: front office, back office, web services.
		{Stage: "web", Image: "portal", Promote: &promoted},
		// The Messenger consumers, the cronjobs and the jobs. Named `portal-consumer` until the
		// FrankenPHP migration, where it stopped serving the consumers only.
		{Stage: "cli", Image: "portal-cli", Promote: &promoted},
		// The "fat" image: xdebug, xhprof, PostgreSQL client. It runs the bootstrap job of the
		// fake-data overlays and allows reproducing a defect on exactly the version delivered. Built
		// but not promoted: no environment of the flow deploys it.
		{Stage: "dev", Image: "portal-dev", Promote: &held},
	}

	declarations := []struct {
		clone      string
		label      string
		tracker    string
		dockerfile string
		context    string
		targets    []store.Target
	}{
		{
			clone: project["repoUrl"],
			label: "Application",
			// The only one of the four to have a tracker. The other three are repositories cloned by
			// pipelines: they carry neither version nor release, so there is nothing to match.
			tracker:    cfg.Tracker.Project,
			dockerfile: project["dockerfile"],
			context:    project["buildContext"],
			targets:    targets,
		},
		{clone: seeds["kustomRepoUrl"], label: "Kustomizations"},
		{clone: resolved["forge.tekton.repoUrl"], label: "Pipelines Tekton"},
		{clone: seeds["e2eRepoUrl"], label: "Campagne E2E"},
	}

	projects := make([]store.Project, 0, len(declarations))
	for _, declaration := range declarations {
		repository, forgeURL := config.RepositoryFromCloneURL(declaration.clone)
		if repository == "" {
			continue
		}

		buildContext := declaration.context
		if buildContext == "" {
			buildContext = "."
		}

		// The registry is filled in only for what is built: a cloned repository with no image
		// publishes none, and giving it one would suggest the opposite.
		registry := ""
		if declaration.dockerfile != "" {
			registry = resolved["registry.project"]
		}

		projects = append(projects, store.Project{
			Repository: repository, Label: declaration.label, ForgeURL: forgeURL,
			TrackerProject: declaration.tracker,
			Dockerfile:     declaration.dockerfile, BuildContext: buildContext,
			ImageRegistry: registry,
			// Several targets only where there are any: a simple Dockerfile ticked "multi-stage" would
			// make one expect a list that does not exist.
			MultiStage: len(declaration.targets) > 0,
			Targets:    declaration.targets,
			// The author is the configuration itself, and saying so avoids attributing to someone a
			// declaration nobody made.
			CreatedBy: "configuration",
		})
	}
	return projects
}

// projectKeys opens what is needed to set one deploy key per project.
//
// Called from the `api` role, because it is what declares the projects — and not the worker,
// which sees only their jobs. Both share the same service account, therefore the same Role:
// the right to write a Secret without being able to read any is already granted.
//
// **Returns nil rather than an error.** A dashboard with no cluster access stays perfectly
// usable: projects are declared in it, and use the deployment's shared key. Failing the
// start-up on a dependency that serves only one feature would be the same defect as the one
// fixed earlier, where a missing attestation prevented the deployer from existing.
func projectKeys(
	cluster *tekton.Client, cfg config.Config, pipelines *catalog.Catalog, logger *slog.Logger,
) *deploykeys.Issuer {
	namespace := cfg.Tekton.Namespace
	owner := deploykeys.NewOwner(cluster.Kube, namespace)
	// Every account the catalogue uses: keeping only one would make the key mountable for some of
	// the project's pipelines and not for the others.
	accounts := pipelines.ServiceAccounts()
	mounter := deploykeys.NewMounter(cluster.Kube, namespace, accounts...)
	logger.Info("per-project deploy keys",
		"namespace", namespace, "service_accounts", accounts)
	return deploykeys.NewIssuer(owner, mounter)
}

// libraryInstaller opens what is needed to put the reference libraries in place.
//
// The discovery of the versions served is done again here rather than borrowed from the
// Tekton client: that one is typed, whereas putting libraries in place handles arbitrary
// manifests through the dynamic client. Both speak to the same cluster, with the same
// resolution.
func libraryInstaller(
	cluster *tekton.Client, cfg config.Config,
) (*pipelinelib.Installer, error) {
	dynamicClient, err := dynamic.NewForConfig(cluster.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("client dynamique: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cluster.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}

	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil && len(resources) == 0 {
		return nil, fmt.Errorf("discovering the cluster's APIs: %w", err)
	}

	return pipelinelib.NewInstaller(referenceLibrary(), dynamicClient,
		restmapper.NewDiscoveryRESTMapper(resources), cfg.Tekton.Namespace), nil
}
