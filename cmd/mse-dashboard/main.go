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
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
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

	server := dashhttp.NewServer(cfg.HTTP, logger,
		dashhttp.NewAPIRouter(authHandler, runsHandler, middleware),
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

			sink := worker.NewRunSink(db)
			observer := tekton.NewObserver(cluster, sink, sink, worker.NewLogAdapter(collector), logger)

			// TODO(lot 3): ordonnanceur de file.
			return observer.Run(leadingCtx)
		})
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
