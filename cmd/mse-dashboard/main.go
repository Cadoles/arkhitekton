// Commande mse-dashboard : un binaire unique, trois rôles.
//
// `api` encaisse le trafic utilisateur et se réplique. `worker` porte les boucles
// de fond — ordonnanceur de file, collecte de logs, synchronisations — et reste
// unique : un ordonnanceur dupliqué lance deux fois le même pipeline. `migrate`
// applique le schéma, depuis un Job d'initialisation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/redmine"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/registry"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/worker"
)

// Renseignés à la compilation via -ldflags (voir Dockerfile et Makefile).
//
// La version est journalisée à chaque démarrage : diagnostiquer un comportement
// inattendu commence par savoir quel binaire tourne, information qu'un tag
// d'image ne donne pas de façon fiable — un tag peut être republié.
var (
	version  = "dev"
	revision = "inconnue"
)

const usage = `mse-dashboard: pilotage des releases MSE

Usage:
  mse-dashboard api       Serveur HTTP (réplicable)
  mse-dashboard worker    Boucles de fond (instance unique, élection de leader)
  mse-dashboard migrate   Applique les migrations de schéma
  mse-dashboard reprocess Rejoue les résultats depuis les artefacts bruts

Diagnostic:
  mse-dashboard check-redmine  Vérifie l'accès Redmine et s'arrête (lecture seule)

Options de reprocess:
  --release <tag>  Limite le rejeu à une release (défaut: tout l'historique)
  --dry-run        Énumère ce qui serait rejoué, sans rien effacer
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "erreur: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("aucun rôle fourni")
	}

	role := config.Role(os.Args[1])
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("version", version, "revision", revision)

	cfg, err := config.Load(role)
	if err != nil {
		// La configuration est validée avant l'ouverture du moindre port : un
		// démarrage à moitié configuré reporte la panne sur le premier
		// utilisateur, souvent lors de l'opération la plus rare.
		return fmt.Errorf("configuration invalide:\n%w", err)
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
	case config.RoleCheckRedmine:
		return runCheckRedmine(ctx, cfg, logger)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("rôle inconnu %q", role)
	}
}

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("démarrage", "role", cfg.Role, "addr", cfg.HTTP.Addr, "issuer", cfg.OIDC.IssuerURL)

	// Attente bornée : la base repart souvent en même temps que le pod. Au-delà
	// de la borne, la sonde de démarrage prend le relais et fait redémarrer le
	// pod — le comportement de repli reste celui de Kubernetes.
	if err := store.WaitForDatabase(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	// L'accès au stockage est vérifié au démarrage : sans cela, un identifiant
	// erroné ne se manifesterait qu'à la première publication d'artefact,
	// c'est-à-dire à la fin d'un pipeline.
	storage, err := blob.Open(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	logger.Info("stockage objet", "bucket", storage.Bucket(), "region", cfg.Storage.Region)

	sessions := auth.NewSessionStore(db.Pool(), cfg.OIDC.SessionTTL)

	// L'amorçage n'a plus lieu au démarrage : il prenait chaque identifiant de la
	// configuration pour un `subject`, alors que Gitea met son identifiant
	// numérique dans le claim `sub`. La ligne créée ne correspondait donc à aucun
	// utilisateur réel. La promotion se fait désormais à la connexion, seul moment
	// où le sujet est connu.
	logger.Info("administrateurs d'amorçage déclarés",
		"count", len(cfg.OIDC.BootstrapAdmins), "resolution", "à la première connexion")

	// La découverte OIDC a lieu ici : un fournisseur injoignable doit empêcher le
	// démarrage plutôt que de produire des erreurs opaques au premier utilisateur.
	provider, err := auth.NewProvider(ctx, cfg.OIDC)
	if err != nil {
		return err
	}

	// En base et non en mémoire : deux répliques d'api sans affinité de session
	// servent `/auth/login` et `/auth/callback` par des instances différentes, et
	// l'état de transition serait introuvable une fois sur deux.
	flows := auth.NewDBFlowStore(db.Pool(), 10*time.Minute)
	authorizer := auth.NewAuthorizer(environmentPolicies())
	authHandler := dashhttp.NewAuthHandler(
		provider, flows, sessions, logger, secureCookies(cfg.HTTP.PublicURL),
		cfg.OIDC.BootstrapAdmins, cfg.OIDC.SessionTTL)
	middleware := dashhttp.NewMiddleware(sessions, authorizer, logger)
	runsHandler := dashhttp.NewRunsHandler(db)
	// Le lecteur de journaux vit côté `api` et lit le stockage : le collecteur
	// écrit depuis le `worker`, et les deux processus n'ont pas d'autre point de
	// rencontre que MinIO et PostgreSQL.
	logsHandler := dashhttp.NewLogsHandler(logs.NewReader(storage))

	// Le catalogue est chargé par l'`api` aussi : c'est lui qui dit ce qui est
	// lançable et quels paramètres sont requis. Il n'est pas validé contre le
	// cluster ici — l'api n'y a pas accès, et le `worker` le fait au démarrage.
	pipelines, err := catalog.Load(cfg.Tekton.CatalogPath)
	if err != nil {
		return err
	}
	logger.Info("catalogue chargé", "kinds", pipelines.Kinds(), "plafond", pipelines.MaxConcurrentRuns)
	queueHandler := dashhttp.NewQueueHandler(db, pipelines)
	// Le nom du dépôt promu vient de la configuration du registre : c'est le même
	// préfixe que celui employé pour lire les tags, et le déduire ailleurs ferait
	// diverger lecture et promotion.
	// Le registre est optionnel : sans identifiants, la page de release affiche
	// les tags sans leurs digests et le dit. Faire échouer le démarrage priverait
	// le dashboard de tout le reste pour une information secondaire.
	var images dashhttp.Images
	if client, err := registry.Open(cfg.Registry); err != nil {
		if !errors.Is(err, registry.ErrNotConfigured) {
			return fmt.Errorf("accès au registre: %w", err)
		}
		logger.Warn("registre non configuré", "raison", "les digests d'images ne seront pas lisibles")
	} else {
		images = client
	}

	releasesHandler := dashhttp.NewReleasesHandler(db, pipelines, authorizer, images, storage, cfg.Registry.Image)

	// La forge est optionnelle : sans jeton, le formulaire de création de release
	// retombe sur la saisie libre du git_ref et le dit. Le client est en lecture
	// seule par transport, comme celui de Redmine.
	var forgeReader dashhttp.ForgeReader
	if cfg.Gitea.Configured() {
		forgeClient, err := gitea.New(gitea.Options{
			URL: cfg.Gitea.URL, Token: cfg.Gitea.Token,
			Repository: cfg.Gitea.Repository, Timeout: cfg.Gitea.Timeout,
		})
		if err != nil {
			return fmt.Errorf("client Gitea: %w", err)
		}
		forgeReader = forgeClient
		logger.Info("forge configurée", "url", cfg.Gitea.URL, "repo", cfg.Gitea.Repository)
	} else {
		logger.Warn("forge non configurée",
			"raison", "GITEA_URL, GITEA_TOKEN ou GITEA_REPO absent",
			"consequence", "le git_ref des releases se saisira à la main")
	}

	server := dashhttp.NewServer(cfg.HTTP, logger,
		dashhttp.NewAPIRouter(authHandler, runsHandler, logsHandler, queueHandler,
			releasesHandler, dashhttp.NewBenchHandler(db),
			// La pondération du score vient du catalogue : elle a été acceptée par
			// le client et reste ajustable sans redéploiement (B11).
			dashhttp.NewTestsHandler(db, pipelines.ScoreWeights()),
			dashhttp.NewSignoffsHandler(db),
			// L'URL de Redmine sert à construire les liens sortants. Absente, les
			// liens sont omis plutôt que rendus cassés.
			dashhttp.NewVersionsHandler(db, cfg.Redmine.URL),
			dashhttp.NewSyncAdminHandler(db, cfg.Redmine.Configured()),
			dashhttp.NewForgeHandler(forgeReader),
			dashhttp.NewAdminHandler(db), middleware),
		dashhttp.ReadinessCheck{Name: "postgres", Check: db.Ping},
		// Distinct du ping : PostgreSQL peut répondre alors que le Job de
		// migration n'a pas encore tourné. Sans ce contrôle, le pod se
		// déclarerait prêt puis répondrait 500 sur chaque table absente.
		dashhttp.ReadinessCheck{Name: "schema", Check: db.CheckSchema},
		dashhttp.ReadinessCheck{Name: "minio", Check: storage.Ping})

	return server.Run(ctx)
}

// environmentPolicies décrit les exigences de promotion.
//
// TODO(lot 1): lire la table `environments` plutôt que cette liste. En attendant,
// les valeurs correspondent au flux documenté, et un environnement absent échoue
// fermé côté autorisateur.
func environmentPolicies() map[string]auth.EnvironmentPolicy {
	return map[string]auth.EnvironmentPolicy{
		"unstable": {Sensitive: false},
		"qualif":   {Sensitive: false},
		"preprod":  {Sensitive: true},
		"prod":     {Sensitive: true},
	}
}

// exportRegistry ouvre le registre pour la génération de livrables.
//
// Nil quand il n'est pas configuré : un bon de livraison reste produisible sans
// empreintes, à condition de nommer ce qu'il n'a pas pu vérifier — ce que fait le
// document. Refuser de le produire serait pire.
func exportRegistry(cfg config.Config, logger *slog.Logger) worker.Registry {
	client, err := registry.Open(cfg.Registry)
	if err != nil {
		if !errors.Is(err, registry.ErrNotConfigured) {
			logger.Error("accès au registre", "error", err)
		}
		logger.Warn("registre non configuré",
			"conséquence", "les bons de livraison ne porteront pas d'empreinte d'image")
		return nil
	}
	return client
}

// secureCookies n'autorise les cookies non sécurisés que sur http, c'est-à-dire
// en développement local. Déduire du schéma évite un drapeau qu'on oublie de
// positionner en production.
func secureCookies(publicURL string) bool {
	return !strings.HasPrefix(publicURL, "http://")
}

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("démarrage", "role", cfg.Role, "namespace", cfg.Tekton.Namespace)

	if err := store.WaitForDatabase(ctx, cfg.Database, startupWait, logger); err != nil {
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
	logger.Info("stockage objet", "bucket", storage.Bucket())

	// L'accès au cluster est vérifié au démarrage, comme la base et le stockage :
	// un worker qui démarre sans pouvoir lire les PipelineRun n'observe rien et
	// ne le dit pas.
	cluster, err := tekton.Open(cfg.Tekton)
	if err != nil {
		return err
	}

	pipelines, err := catalog.Load(cfg.Tekton.CatalogPath)
	if err != nil {
		return err
	}

	// La validation contre le cluster a lieu ici et pas ailleurs : c'est le
	// `worker` qui crée les PipelineRun, et un catalogue en désaccord avec les
	// Pipeline déployés produirait des runs invalides à la première utilisation
	// plutôt qu'une erreur au démarrage (doc 06 §6.2).
	validateCtx, cancelValidate := context.WithTimeout(ctx, 30*time.Second)
	err = pipelines.ValidateAgainst(validateCtx, cluster)
	cancelValidate()
	if err != nil {
		return fmt.Errorf("catalogue incompatible avec le cluster:\n%w", err)
	}
	logger.Info("catalogue validé", "kinds", pipelines.Kinds(), "plafond", pipelines.MaxConcurrentRuns)

	// Le client Flux découvre les versions d'API servies au lieu de les coder :
	// `kustomize.toolkit.fluxcd.io/v1` est stable depuis Flux 2.0, alors que
	// `image.toolkit.fluxcd.io/v1` n'existe que depuis 2.7, et une même
	// installation peut donc servir les deux dans des versions différentes.
	fluxClient, err := flux.Open(cluster.RESTConfig, cfg.Flux.Namespace)
	if err != nil {
		return err
	}
	logger.Info("client Flux", "namespace", cfg.Flux.Namespace, "versions", fluxClient.Versions())

	environments := worker.NewEnvironmentObserver(db, fluxClient, logger)

	// Le garde-fou de B7 : un suffixe de promotion qui ne correspond à aucune
	// ImagePolicy produirait un tag valide, poussé sans erreur, que Flux
	// ignorerait. Mieux vaut ne pas démarrer.
	syncCtx, cancelSync := context.WithTimeout(ctx, 30*time.Second)
	err = environments.SyncEnvironments(syncCtx, pipelines.Environments)
	cancelSync()
	if err != nil {
		return err
	}

	identity := worker.LeaderIdentity()
	logger.Info("candidature au bail", "identity", identity, "lease_namespace", cfg.Tekton.LeaseNamespace)

	// Tout ce qui suit ne tourne qu'en un exemplaire. Le `replicas: 1` du
	// Deployment n'y suffit pas : pendant un rolling update deux pods coexistent,
	// et c'est l'instant où un ordonnanceur dupliqué lancerait deux fois le même
	// pipeline.
	return worker.RunAsLeader(ctx, cluster.Kube,
		worker.LeaderConfig{Namespace: cfg.Tekton.LeaseNamespace, Identity: identity},
		logger,
		func(leadingCtx context.Context) error {
			// Le collecteur est arrêté avant que la fonction rende la main : les
			// captures en cours doivent cesser d'écrire avant qu'un autre worker
			// prenne le bail, sinon deux processus écrivent les mêmes tranches.
			collector := logs.New(tekton.NewPodLogs(cluster), storage, db, nil, logger)
			defer collector.Stop()

			// L'ingestion des campagnes de charge est branchée sur la fin des
			// runs : le runner k6 vit hors du cluster et n'a aucun chemin de
			// retour vers le dashboard (B16).
			objects := worker.NewObjectStore(storage)
			sink := worker.NewRunSink(db).
				WithLoadIngester(worker.NewLoadIngester(db, objects, logger)).
				WithTestIngester(worker.NewTestIngester(db, objects, logger)).
				WithSecurityIngester(worker.NewSecurityIngester(db, objects, logger))
			observer := tekton.NewObserver(cluster, sink, sink, worker.NewLogAdapter(collector), logger)

			// Les URLs d'écriture des artefacts sont minées ici : le layout de
			// stockage appartient au dashboard, et c'est ce qui sort le secret
			// MinIO des Tasks de test (B14).
			// Ce que les pipelines recevront, journalisé avant toute exécution.
			// Un dépôt ou un Dockerfile qui ne correspond pas à ce qu'on croit
			// piloter ne se voit sinon qu'à l'échec du build, où le message
			// désigne un chemin sans dire d'où il vient.
			resolved := schedulerConfig(cfg)
			logger.Info("cible des pipelines",
				"repo", resolved["forge.mse.repoUrl"],
				"dockerfile", resolved["mse.dockerfile"],
				"registry_project", resolved["registry.project"])

			scheduler := worker.NewSchedulerFromCatalog(
				db, cluster, pipelines, resolved, logger).WithUploads(storage)

			// Les livrables passent par la file comme le reste, mais sont
			// produits ici : générer un bon de livraison lit la base et écrit un
			// objet. Le faire traverser Tekton coûterait un pod et un workspace
			// pour produire deux pages.
			scheduler.Handle(worker.NewExporter(db, storage, exportRegistry(cfg, logger), cfg.Registry.Image, logger))

			// L'observation et l'ordonnancement tournent en parallèle : la
			// seconde dépend de la première pour apprendre qu'un run est fini,
			// et les sérialiser bloquerait la file derrière la synchronisation
			// du cache.
			// La purge des sessions échues et des transactions
			// d'authentification abandonnées : sa boucle manquait, et
			// `PurgeExpired` était documentée comme appelée périodiquement sans
			// que personne ne l'appelle.
			purger := worker.NewPurger(
				auth.NewSessionStore(db.Pool(), cfg.OIDC.SessionTTL),
				auth.NewDBFlowStore(db.Pool(), 10*time.Minute),
				logger)

			errs := make(chan error, 5)
			go func() { errs <- observer.Run(leadingCtx) }()
			go func() { errs <- scheduler.Run(leadingCtx) }()
			go func() { errs <- environments.Run(leadingCtx) }()
			go func() { errs <- purger.Run(leadingCtx) }()

			// Redmine est optionnel : sans clé, le dashboard tourne et les écrans
			// de versions le disent. Faire échouer le worker priverait
			// l'ordonnancement et l'observation d'un accès qu'ils n'utilisent pas.
			//
			// La synchronisation vit sous le bail comme le reste : deux workers
			// interrogeant la même instance doubleraient la charge sur un service
			// tiers pour un résultat identique.
			if cfg.Redmine.Configured() {
				client, err := redmine.New(redmine.Options{
					URL: cfg.Redmine.URL, APIKey: cfg.Redmine.APIKey,
					Timeout: cfg.Redmine.Timeout,
				})
				if err != nil {
					return fmt.Errorf("client Redmine: %w", err)
				}
				sync := worker.NewRedmineSync(db, client, cfg.Redmine.Project, logger)
				logger.Info("synchronisation Redmine active",
					"url", cfg.Redmine.URL, "project", cfg.Redmine.Project)
				go func() { errs <- sync.Run(leadingCtx) }()
			} else {
				logger.Warn("synchronisation Redmine inactive",
					"raison", "REDMINE_URL ou REDMINE_API_KEY absent",
					"consequence", "les versions et les demandes ne seront pas renseignées")
			}

			// La première erreur l'emporte : une boucle morte doit faire perdre
			// le bail plutôt que de laisser l'autre tourner à moitié.
			return <-errs
		})
}

// schedulerConfig expose à l'ordonnanceur les valeurs que le catalogue référence
// par clé.
//
// La table est explicite plutôt que dérivée par réflexion : ce qu'un catalogue
// peut injecter dans un PipelineRun doit être une liste qu'on peut lire, pas
// l'ensemble de la configuration du dashboard.
func schedulerConfig(cfg config.Config) map[string]string {
	envOr := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}

	// Le dépôt applicatif est dérivé de `GITEA_REPO` : c'est le même dépôt que
	// celui dont le formulaire propose les branches, et le nommer deux fois
	// laissait basculer l'un sans l'autre. `FORGE_MSE_REPO_URL` reste une
	// dérogation explicite, non le réglage courant.
	//
	// Les deux autres dépôts ne suivent pas : `mse-kustom` et `mse-tests-e2e`
	// sont d'autres dépôts, pas d'autres vues du même.
	appRepo := envOr("FORGE_MSE_REPO_URL",
		cfg.Gitea.CloneURL(os.Getenv("FORGE_SSH_PORT")))
	if appRepo == "" {
		appRepo = "ssh://git@forge.cadoles.com:2222/CNOUS/mse.git"
	}

	return map[string]string{
		"tekton.namespace":        cfg.Tekton.Namespace,
		"forge.mse.repoUrl":       appRepo,
		"forge.mseKustom.repoUrl": envOr("FORGE_MSE_KUSTOM_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-kustom.git"),
		"forge.e2e.repoUrl":       envOr("FORGE_E2E_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-tests-e2e.git"),
		"registry.project":        envOr("REGISTRY_PROJECT", "reg.cadoles.com/cnous-mse"),
		"mse.overlayLocation":     envOr("MSE_OVERLAY_LOCATION", "overlays/pipeline"),
		"mse.dockerfile":          envOr("MSE_DOCKERFILE", "./misc/k8s/images/portal/Dockerfile"),
		"mse.pipelineTag":         envOr("MSE_PIPELINE_TAG", "pipeline"),
		// Le bucket des pipelines n'est pas celui du dashboard : `mse-pipeline`
		// porte les artefacts que les Tasks publient, `mse-dashboard` ce que le
		// dashboard écrit lui-même (doc 03 §3.5).
		"minio.host":           envOr("PIPELINE_MINIO_HOST", "https://nuo-s3.nuonet.fr"),
		"minio.pipelineBucket": envOr("PIPELINE_MINIO_BUCKET", "mse-pipeline"),
	}
}

func runMigrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("migration du schéma", "dsn", cfg.Redacted().Database.DSN)

	// Le Job de migration démarre en même temps que la base dans un déploiement
	// neuf : sans cette attente, il échoue, Kubernetes le relance, et chaque
	// déploiement laisse un pod en `Error` derrière lui.
	if err := store.WaitForDatabase(ctx, cfg.Database, databaseWait, logger); err != nil {
		return err
	}

	if err := store.Migrate(ctx, cfg.Database, logger); err != nil {
		return err
	}

	state, err := store.MigrationState(ctx, cfg.Database)
	if err != nil {
		return err
	}
	for _, migration := range state {
		logger.Info("migration", "file", migration.Name, "applied", migration.Applied)
	}

	logger.Info("schéma à jour", "count", len(state))
	return nil
}

// databaseWait borne l'attente de la base au démarrage d'une migration.
//
// Deux minutes : de quoi couvrir l'initialisation d'un PostgreSQL neuf — création
// du cluster, premier démarrage — sans laisser un Job tourner indéfiniment contre
// une base qui n'existe pas.
const databaseWait = 2 * time.Minute

// startupWait borne l'attente de la base au démarrage de l'`api` et du `worker`.
//
// Volontairement **plus courte** que le budget de la sonde de démarrage — 20
// tentatives toutes les 3 secondes, soit 60 s. C'est ce qui préserve la
// propriété qui compte : si la base est réellement absente, la sonde tue le pod
// et Kubernetes le redémarre, comme avant.
//
// L'attente ne sert qu'au cas fréquent et bénin : la base repart en même temps
// que le pod. Sans elle, le processus sortait en erreur, était redémarré, et
// réussissait au second essai — un `RESTARTS 1` sur chaque déploiement neuf, et
// un `skaffold run` qui rend 1 sur un déploiement sain. Le même défaut que le
// Job de migration, sous une autre forme.
const startupWait = 45 * time.Second

// runReprocess rejoue les résultats depuis les artefacts bruts.
//
// Rôle ponctuel et non boucle de fond : on le lance après avoir corrigé un
// parseur, pour que l'historique reflète le code actuel plutôt que celui qui
// tournait le jour du run. Le brut conservé est ce qui rend cette
// reconstruction possible sans réexécuter le moindre pipeline.
func runReprocess(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("reprocess", flag.ContinueOnError)
	releaseTag := flags.String("release", "", "limiter le rejeu à une release")
	dryRun := flags.Bool("dry-run", false, "énumérer sans effacer ni reconstruire")
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
		release, err := db.GetRelease(ctx, *releaseTag)
		if err != nil {
			// Refuser plutôt que rejouer tout : un tag mal orthographié
			// déclencherait sinon un rejeu de l'historique entier, c'est-à-dire
			// l'effacement de tout ce qui est projeté.
			return fmt.Errorf("release %q: %w", *releaseTag, err)
		}
		releaseID = release.ID
	}

	objects := worker.NewObjectStore(storage)
	reprocessor := worker.NewReprocessor(db,
		worker.NewTestIngester(db, objects, logger),
		worker.NewSecurityIngester(db, objects, logger),
		worker.NewLoadIngester(db, objects, logger),
		logger)

	logger.Info("rejeu démarré",
		"release", *releaseTag, "simulation", *dryRun, "portée",
		portee(*releaseTag))

	report, err := reprocessor.Run(ctx, worker.ReprocessOptions{
		ReleaseID: releaseID, DryRun: *dryRun,
	})
	if err != nil {
		return err
	}

	logger.Info("rejeu terminé",
		"runs examinés", report.Examined, "runs rejoués", report.Replayed,
		"rapports effacés", report.Deleted, "échecs", report.Failed)

	if len(report.Failed) > 0 {
		// Sortie non nulle : un rejeu partiel ne doit pas passer pour un succès
		// dans un pipeline ou un script d'exploitation.
		return fmt.Errorf("%d run(s) non rejoué(s): %s",
			len(report.Failed), strings.Join(report.Failed, ", "))
	}
	return nil
}

func portee(releaseTag string) string {
	if releaseTag == "" {
		return "tout l'historique"
	}
	return "une release"
}

// runCheckRedmine vérifie l'accès à Redmine, puis s'arrête.
//
// Rôle de diagnostic, pour être sûr qu'une clé déposée fonctionne avant qu'un écran
// n'en dépende. Il ne fait qu'une lecture — `/users/current.json` — et le transport
// du paquet `redmine` interdit structurellement toute autre méthode que GET et
// HEAD, ce qui rend cette vérification sans risque sur l'instance de production.
//
// La clé n'est jamais journalisée. Ce qui l'est : l'URL, le compte auquel elle
// appartient, et son drapeau `admin`.
func runCheckRedmine(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	client, err := redmine.New(redmine.Options{
		URL:     cfg.Redmine.URL,
		APIKey:  cfg.Redmine.APIKey,
		Timeout: cfg.Redmine.Timeout,
	})
	if err != nil {
		return fmt.Errorf("client Redmine: %w", err)
	}

	account, err := client.CheckAccess(ctx)
	if err != nil {
		return fmt.Errorf("vérification de l'accès Redmine: %w", err)
	}

	logger.Info("accès Redmine vérifié",
		"url", cfg.Redmine.URL,
		"project", cfg.Redmine.Project,
		"login", account.Login,
		"user_id", account.ID,
		// Signalé parce qu'il mérite d'être vu : une clé d'administrateur donne
		// bien plus que ce dont le dashboard a besoin. La lecture seule du
		// transport protège d'une erreur de code, pas d'un compte trop puissant.
		"admin", account.Admin)

	if account.Admin {
		logger.Warn("cette clé appartient à un compte administrateur Redmine ; " +
			"un compte en lecture seule sur le projet suffirait")
	}
	return nil
}
