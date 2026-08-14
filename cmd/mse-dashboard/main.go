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
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
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
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("rôle inconnu %q", role)
	}
}

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("démarrage", "role", cfg.Role, "addr", cfg.HTTP.Addr, "issuer", cfg.OIDC.IssuerURL)

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

	// L'amorçage a lieu avant l'ouverture du port : démarrer sans administrateur
	// donnerait une interface où personne ne peut rien faire ni rien débloquer.
	granted, err := sessions.EnsureBootstrapAdmins(ctx, cfg.OIDC.BootstrapAdmins)
	if err != nil {
		return fmt.Errorf("amorçage des administrateurs: %w", err)
	}
	logger.Info("administrateurs d'amorçage", "declared", len(cfg.OIDC.BootstrapAdmins), "granted", granted)

	// La découverte OIDC a lieu ici : un fournisseur injoignable doit empêcher le
	// démarrage plutôt que de produire des erreurs opaques au premier utilisateur.
	provider, err := auth.NewProvider(ctx, cfg.OIDC)
	if err != nil {
		return err
	}

	flows := auth.NewFlowStore(10 * time.Minute)
	authorizer := auth.NewAuthorizer(environmentPolicies())
	authHandler := dashhttp.NewAuthHandler(
		provider, flows, sessions, logger, secureCookies(cfg.HTTP.PublicURL), cfg.OIDC.SessionTTL)
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

	server := dashhttp.NewServer(cfg.HTTP, logger,
		dashhttp.NewAPIRouter(authHandler, runsHandler, logsHandler, queueHandler,
			releasesHandler, dashhttp.NewBenchHandler(db), middleware),
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
			sink := worker.NewRunSink(db).WithLoadIngester(
				worker.NewLoadIngester(db, worker.NewObjectStore(storage), logger))
			observer := tekton.NewObserver(cluster, sink, sink, worker.NewLogAdapter(collector), logger)

			// Les URLs d'écriture des artefacts sont minées ici : le layout de
			// stockage appartient au dashboard, et c'est ce qui sort le secret
			// MinIO des Tasks de test (B14).
			scheduler := worker.NewSchedulerFromCatalog(
				db, cluster, pipelines, schedulerConfig(cfg), logger).WithUploads(storage)

			// Les livrables passent par la file comme le reste, mais sont
			// produits ici : générer un bon de livraison lit la base et écrit un
			// objet. Le faire traverser Tekton coûterait un pod et un workspace
			// pour produire deux pages.
			scheduler.Handle(worker.NewExporter(db, storage, exportRegistry(cfg, logger), cfg.Registry.Image, logger))

			// L'observation et l'ordonnancement tournent en parallèle : la
			// seconde dépend de la première pour apprendre qu'un run est fini,
			// et les sérialiser bloquerait la file derrière la synchronisation
			// du cache.
			errs := make(chan error, 3)
			go func() { errs <- observer.Run(leadingCtx) }()
			go func() { errs <- scheduler.Run(leadingCtx) }()
			go func() { errs <- environments.Run(leadingCtx) }()

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

	return map[string]string{
		"tekton.namespace":        cfg.Tekton.Namespace,
		"forge.mse.repoUrl":       envOr("FORGE_MSE_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse.git"),
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
