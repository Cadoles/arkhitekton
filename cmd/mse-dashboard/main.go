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
	"sort"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/discovery"

	"k8s.io/client-go/dynamic"

	"k8s.io/client-go/restmapper"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/deploykeys"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
	pipelinelib "forge.cadoles.com/vcarroy/mse-dashboard/internal/pipelines"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/portaldata"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/redmine"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/registry"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/webhook"
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
  mse-dashboard check-tracker  Vérifie l'accès au suivi et s'arrête (lecture seule)

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
	case config.RoleCheckTracker:
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
	// Puis le **schéma**, et pas seulement la connexion : le Job de migration
	// démarre en même temps que ce pod, et servir des requêtes contre un schéma
	// incomplet rendrait des 500 sur des tables qui vont exister.
	if err := store.WaitForSchema(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}

	db, err := openStore(ctx, cfg)
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

	// L'amorçage n'a plus lieu ici : il est posé une fois, avec la migration du
	// schéma, et la base en est ensuite seule maîtresse. Ce qui reste au démarrage
	// est le constat de ce qui attend encore son titulaire — un identifiant mal
	// orthographié n'a aucun autre symptôme que « cet utilisateur n'a aucune
	// permission ».
	if pending, err := db.PendingGrants(ctx); err != nil {
		logger.Error("rôles d'amorçage en attente", "error", err)
	} else if len(pending) > 0 {
		identifiers := make([]string, 0, len(pending))
		for _, grant := range pending {
			identifiers = append(identifiers, grant.Identifier+":"+grant.Role)
		}
		logger.Info("rôles d'amorçage en attente d'une première connexion",
			"count", len(pending), "declarations", identifiers)
	}

	// La découverte OIDC a lieu ici : un fournisseur injoignable doit empêcher le
	// démarrage plutôt que de produire des erreurs opaques au premier utilisateur.
	provider, err := auth.NewProvider(ctx, cfg.OIDC)
	if err != nil {
		return err
	}

	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	// Les lignes manquantes des plugins embarqués, à chaque démarrage. L'amorçage
	// ne les pose qu'une fois : un plugin apparu dans une version ultérieure
	// n'aurait jamais de ligne, et l'écran l'afficherait sans état.
	if err := db.EnsurePlugins(ctx, pluginIDs()); err != nil {
		logger.Error("amorçage des plugins", "error", err)
	}

	// En base et non en mémoire : deux répliques d'api sans affinité de session
	// servent `/auth/login` et `/auth/callback` par des instances différentes, et
	// l'état de transition serait introuvable une fois sur deux.
	flows := auth.NewDBFlowStore(db.Pool(), 10*time.Minute)
	authorizer := auth.NewAuthorizer(environmentPolicies())
	authHandler := dashhttp.NewAuthHandler(
		provider, flows, sessions, logger, secureCookies(cfg.HTTP.PublicURL),
		cfg.OIDC.SessionTTL).
		// `/me` rend le projet courant : l'interface en a besoin pour composer ses
		// URL, et la liste des projets exige `admin` qu'elle n'a pas forcément.
		WithProjects(db)
	// Les contrôles rattachés à un projet ont besoin de traduire le nom porté par
	// l'URL : c'est lui qui dit dans quel périmètre le rôle est vérifié.
	middleware := dashhttp.NewMiddleware(sessions, authorizer, logger).WithProjects(db)
	runsHandler := dashhttp.NewRunsHandler(db).
		WithTektonDashboard(cfg.Tekton.DashboardURL).
		// L'annuaire traduit le sujet OIDC du demandeur en nom : c'est « qui a
		// lancé » qu'on lit sur un écran d'exécution, pas un identifiant.
		WithDirectory(db, logger)
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
	queueHandler := dashhttp.NewQueueHandler(db, pipelines).
		// Les listes du formulaire de lancement : tags de release et images
		// publiées. Le préfixe du registre vient de la même configuration que
		// celle employée pour lire les tags — le déduire ailleurs ferait diverger
		// ce qu'on propose de ce qu'on tire.
		WithSuggestions(db, cfg.Registry.Project, logger)
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

	releasesHandler := dashhttp.NewReleasesHandler(
		db, pipelines, authorizer, images, storage, cfg.Registry.Image, cfg.Registry.Project).
		// Même pondération que le gestionnaire des tests : la pastille de la
		// liste et l'écran d'une release affichent le même chiffre.
		WithScoreCatalog(pipelines).
		WithTrackerURL(cfg.Tracker.URL).
		// Le même normaliseur que le store, et ce n'est pas une précaution : ce
		// handler compare une saisie à une colonne que le store a écrite.
		WithNormalizer(versionNames(cfg)).
		// Le même déployeur que l'observateur, et le même que celui qui a validé
		// les environnements au démarrage : un aperçu de promotion qui répondrait
		// autrement que la promotion elle-même serait pire que pas d'aperçu.
		WithDeployer(flux.NewDeployer(nil, flux.DefaultPromotionKind, "")).
		// L'ouverture du plan d'une release, à sa création.
		//
		// Un moteur côté `api`, et ce n'est pas un second ordonnanceur : ouvrir un
		// plan écrit une exécution et met des jobs en file, ce que ce handler
		// faisait déjà. Le worker garde l'exclusivité de ce qui **sort** — créer
		// les PipelineRun — et de l'avancement des gestes conclus.
		WithPlans(worker.NewReleasePlanner(
			worker.NewEngine(db, worker.NewReleaseRunner(pipelines, db, logger), logger).
				WithArtifacts(db, worker.ProductOf(pipelines)),
			pipelines, db, cfg.Registry.Project, logger))

	// La forge est optionnelle : sans jeton, le formulaire de création de release
	// retombe sur la saisie libre du git_ref et le dit. Le client est en lecture
	// seule par transport, comme celui de Redmine.
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

		// Le client d'écriture pose le tag Git d'une release (B3). Distinct du
		// client de lecture, dont le transport refuse tout ce qui n'est pas GET :
		// l'assouplir aurait fait qu'un appel de lecture mal écrit puisse écrire.
		//
		// Sa construction ne vérifie pas la portée du jeton — seule une écriture
		// réelle le dirait. Un jeton en lecture seule produit donc une réserve à
		// la première release, pas un échec au démarrage.
		writer, err := gitea.NewWriter(gitea.Options{
			URL: cfg.Forge.URL, Token: cfg.Forge.Token,
			Repository: cfg.Forge.Repository, Timeout: cfg.Forge.Timeout,
		})
		if err != nil {
			return fmt.Errorf("client Gitea en écriture: %w", err)
		}
		releasesHandler = releasesHandler.WithForge(forgeClient, writer)

		logger.Info("forge configurée", "url", cfg.Forge.URL, "repo", cfg.Forge.Repository)
	} else {
		logger.Warn("forge non configurée",
			"raison", "FORGE_URL, FORGE_TOKEN ou FORGE_REPO absent",
			"consequence", "le git_ref des releases se saisira à la main")
	}

	server := dashhttp.NewServer(cfg.HTTP, logger,
		dashhttp.NewAPIRouter(authHandler, runsHandler, logsHandler, queueHandler,
			releasesHandler, dashhttp.NewBenchHandler(db),
			// La pondération du score vient du catalogue : elle a été acceptée par
			// le client et reste ajustable sans redéploiement (B11).
			dashhttp.NewTestsHandler(db, pipelines),
			dashhttp.NewSignoffsHandler(db),
			// L'URL de Redmine sert à construire les liens sortants. Absente, les
			// liens sont omis plutôt que rendus cassés.
			dashhttp.NewVersionsHandler(db, cfg.Tracker.URL).
				// Le même normaliseur que le magasin : c'est lui qui décide ce
				// qu'est la ligne de version d'un nom comme `MSE 3.1.0`, et le
				// tag proposé au formulaire en dépend.
				WithNormalizer(versionNames(cfg)),
			dashhttp.NewSyncAdminHandler(db),
			dashhttp.NewForgeHandler(forgeReader),
			dashhttp.NewAdminHandler(db),
			// Le test d'accès emploie le jeton de la forge, avec un client par
			// dépôt : celui de la configuration est lié au sien, et le test porte
			// justement sur un autre. Sans jeton, l'écran le dit plutôt que de
			// rendre un échec qu'on prendrait pour un défaut de droits.
			projectsHandler(cfg, db),
			// Le chiffreur peut être nil : sans clé, les réglages se lisent et
			// s'activent, seule l'écriture d'un champ sensible est refusée — avec
			// un message qui nomme la variable, à l'endroit où on le saisit.
			dashhttp.NewPluginsHandler(db, cipher, logger),
			// Le même catalogue que la file : l'écran montre ce que l'instance
			// sait lancer, et un second chargement pourrait montrer autre chose.
			dashhttp.NewProjectPipelinesHandler(db, pipelines, logger),
			// Le fil des faits, en lecture. Rien n'y réagit encore : il sert à
			// répondre à « pourquoi rien ne s'est passé ? », question qui n'a de
			// sens qu'une fois qu'on peut voir ce qui a bien été constaté.
			dashhttp.NewEventsHandler(db),
			// Le même catalogue que la file : l'éditeur propose ce que l'instance
			// sait lancer, et un second chargement pourrait proposer autre chose.
			dashhttp.NewAutomationsHandler(db, pipelines, logger),
			// Ce qu'une réaction a réellement fait. Un moteur qu'on ne peut pas
			// déboguer est pire qu'un comportement figé (doc 14 §14.10).
			dashhttp.NewPlanRunsHandler(db),
			middleware),
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
	// Puis le schéma. Le worker se réconcilie au démarrage — il rattrape les runs
	// conclus pendant son absence — et cette réconciliation touche des tables que
	// le Job de migration n'a peut-être pas encore créées.
	if err := store.WaitForSchema(ctx, cfg.Database, startupWait, logger); err != nil {
		return err
	}

	db, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// Le worker **ouvre** les champs sensibles des réglages, il n'en scelle
	// aucun : c'est lui qui présente une clé d'API au suivi à chaque passe.
	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	if err := db.EnsurePlugins(ctx, pluginIDs()); err != nil {
		logger.Error("amorçage des plugins", "error", err)
	}

	// `versions.normalized_name` est une colonne dérivée : changer les préfixes
	// configurés la rend obsolète, et le rapprochement comparerait alors une forme
	// calculée avec les nouveaux à une forme écrite avec les anciens. Le worker
	// est seul à le faire — l'api tourne à plusieurs répliques, et trois
	// renormalisations concurrentes écriraient la même chose trois fois.
	corrected, err := db.RenormalizeVersions(ctx)
	if err != nil {
		return err
	}
	if corrected > 0 {
		logger.Info("noms de version renormalisés",
			"corrigees", corrected, "prefixes", versionNames(cfg).Prefixes())
	}

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

	// Les projets qui n'ont jamais déclaré leur automatisation la reçoivent,
	// composée depuis leurs cibles. Sans elle, tous cesseraient de construire au
	// déploiement de cette version (doc 15 §15.8).
	//
	// **Après la validation du catalogue**, et pas avant : la reprise compose des
	// gestes qui désignent des familles, et les figer depuis un catalogue en
	// désaccord avec le cluster écrirait en base des plans qui ne partiront
	// jamais. Une reprise ne repasse pas.
	//
	// Ici et pas dans l'`api`, pour la même raison que la renormalisation :
	// celle-ci tourne à plusieurs répliques, et trois reprises concurrentes
	// écriraient la même chose trois fois. L'`api` lit la déclaration, elle ne la
	// compose pas.
	if err := worker.NewAutomationReprise(db, pipelines, logger).Run(ctx); err != nil {
		return err
	}

	// Le client Flux découvre les versions d'API servies au lieu de les coder :
	// `kustomize.toolkit.fluxcd.io/v1` est stable depuis Flux 2.0, alors que
	// `image.toolkit.fluxcd.io/v1` n'existe que depuis 2.7, et une même
	// installation peut donc servir les deux dans des versions différentes.
	fluxClient, err := flux.Open(cluster.RESTConfig, cfg.Flux.Namespace)
	if err != nil {
		return err
	}
	logger.Info("client Flux", "namespace", cfg.Flux.Namespace, "versions", fluxClient.Versions())

	// Deux dépendances distinctes, et depuis le lot D deux clients distincts :
	// l'une dit ce que le déploiement a appliqué, l'autre ce que l'application
	// rapporte d'elle-même. C'est leur écart qui fait la valeur de l'écran des
	// environnements, et les confondre le ferait disparaître (B8).
	//
	// L'attestation est **facultative** : un cluster sans le CRD du portail est
	// une configuration valide — c'est le cas de tout projet qui n'est pas le
	// MSE —, et le dashboard y retombe sur ce que le déploiement déclare. Tant
	// qu'un seul client résolvait les deux, cette absence faisait échouer le
	// démarrage sur une dépendance principale parfaitement installée.
	var attester provider.Attester = provider.NoAttester{}
	switch portalClient, err := portaldata.Open(cluster.RESTConfig); {
	case errors.Is(err, portaldata.ErrAbsent):
		logger.Info("aucune source d'attestation dans ce cluster",
			"consequence", "l'état des environnements sera celui que le déployeur déclare")
	case err != nil:
		return err
	default:
		attester = portalClient
		// `api_version` et non `version` : le logger porte déjà un attribut
		// `version` — celui du binaire —, et deux clés homonymes dans un même
		// enregistrement le rendent illisible.
		logger.Info("source d'attestation", "api_version", portalClient.Version())
	}

	deployer := flux.NewDeployer(fluxClient, flux.DefaultPromotionKind, "")
	environments := worker.NewEnvironmentObserver(db, deployer, attester, logger)

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
			// Ce que les pipelines recevront, journalisé avant toute exécution.
			// Un dépôt ou un Dockerfile qui ne correspond pas à ce qu'on croit
			// piloter ne se voit sinon qu'à l'échec du build, où le message
			// désigne un chemin sans dire d'où il vient.
			resolved := schedulerConfig(cfg)
			logger.Info("cible des pipelines",
				"repo", resolved["forge.mse.repoUrl"],
				"dockerfile", resolved["mse.dockerfile"],
				"registry_project", resolved["registry.project"],
				"kustom_repo", resolved["forge.mseKustom.repoUrl"],
				"kustom_revision", resolved["forge.mseKustom.revision"],
				// L'overlay est cherché dans la kustomization, pas dans
				// l'application : viser la racine de l'une parce que l'autre y
				// porte son Dockerfile a coûté un déploiement.
				"kustom_overlay", resolved["mse.overlayLocation"],
				"git_secret", resolved["pipeline.gitSecret"],
				"registry_secret", resolved["pipeline.registrySecret"],
				// Le stockage des pipelines n'est pas celui du dashboard, et il
				// ne se manifeste qu'à la dernière étape d'un audit : une heure
				// de scans aboutis, puis un refus d'identifiants.
				"pipeline_minio", resolved["minio.host"])

			// L'identité de la base entre dans le nom des runs : sans elle, une
			// base neuve réattribue des identifiants de jobs déjà portés par des
			// PipelineRun restés dans le cluster, et l'ordonnanceur lit le conflit
			// comme « c'était déjà fait » sans rien lancer.
			instance, err := db.InstanceID(ctx)
			if err != nil {
				return err
			}
			logger.Info("identité de la base", "instance", instance)

			objects := worker.NewObjectStore(storage)
			sink := worker.NewRunSink(db).
				WithLoadIngester(worker.NewLoadIngester(db, objects, logger)).
				WithTestIngester(worker.NewTestIngester(db, objects, logger)).
				WithSecurityIngester(worker.NewSecurityIngester(db, objects, logger)).
				// La lecture des descripteurs d'exécution. Elle passe par le
				// lecteur de journaux et non par le stockage d'artefacts : un
				// outil qui n'a pas conclu n'a rien publié, et c'est précisément
				// ce cas qu'elle rend visible.
				WithMarkerIngester(worker.NewMarkerIngester(
					db, logs.NewReader(storage),
					results.NewMarkerSet(cfg.Results.MarkerToken), logger)).
				// Le catalogue permet d'enchaîner les vérifications d'une
				// construction réussie : sans lui, l'observation continue mais
				// une release construite reste en attente d'un test que rien
				// ne lance.
				WithCatalog(pipelines, resolved["registry.project"]).
				// Sans l'identité de la base, un run resté dans le cluster après
				// une remise à zéro conclut le job homonyme de la base courante.
				WithInstance(instance).
				// L'émission des faits (doc 14).
				WithEvents(db)

			// Le moteur de plans, qui enchaîne les vérifications d'une
			// construction réussie et conclut leurs gestes à la fin de leur job.
			//
			// Assemblé après le sink parce qu'il en dépend : le runner a besoin du
			// catalogue et de la file, que le sink porte déjà.
			// Les appels sortants, avec leur transport à cible contrôlée. Les
			// autorisations viennent du déploiement et sont fausses par défaut :
			// une URL saisie dans l'interface est émise depuis le cluster.
			actions := worker.NewActionRunner(
				webhook.New(webhook.Options{
					AllowPrivate:  cfg.Webhooks.AllowPrivate,
					AllowInsecure: cfg.Webhooks.AllowInsecure,
					Timeout:       cfg.Webhooks.Timeout,
				}),
				secretResolver{store: db, cipher: cipher}, logger).
				// Les gestes qu'un plugin fournit. Ils n'empruntent pas le
				// transport ci-dessus : leur cible n'est pas saisie dans un
				// formulaire mais lue dans la configuration du plugin, et le
				// contrôle d'adresse existe précisément parce qu'ailleurs elle
				// l'est. Le jeton est celui du plugin, ouvert le temps de l'appel.
				WithPlugins(worker.NewPluginActionRunner(
					db, cipher, cfg.Forge.Timeout, logger))
			if cfg.Webhooks.AllowPrivate || cfg.Webhooks.AllowInsecure {
				logger.Warn("appels sortants élargis",
					"cibles_privees", cfg.Webhooks.AllowPrivate,
					"clair", cfg.Webhooks.AllowInsecure,
					"consequence", "une réaction mal réglée peut atteindre le réseau interne")
			}

			// Le moteur, et l'ancrage du cycle de vie sur les étapes à rôle :
			// c'est le règlement d'une étape qui avance la release, là où
			// l'observateur comptait les constructions job par job (doc 15 §15.3).
			engine := worker.NewVerificationEngine(sink, db, actions, logger).
				WithStages(worker.NewReleaseLifecycle(db, logger)).
				// Ce que les gestes produisent, relevé à leur conclusion et offert
				// aux étapes suivantes sous `{artifacts.<geste>.<nom>}`.
				WithArtifacts(db, worker.ProductOf(pipelines))
			// L'aiguillage : ce qui relie un fait constaté aux réactions qu'un
			// projet a déclarées. Sans lui, les faits s'écrivent et personne n'y
			// réagit — l'état des tranches 2 à 5.
			dispatcher := worker.NewDispatcher(db, engine, logger)
			sink = sink.WithEngine(engine).WithDispatcher(dispatcher)
			observer := tekton.NewObserver(cluster, sink, sink, worker.NewLogAdapter(collector), logger)

			// Les URLs d'écriture des artefacts sont minées ici : le layout de
			// stockage appartient au dashboard, et c'est ce qui sort le secret
			// MinIO des Tasks de test (B14).

			scheduler := worker.NewSchedulerFromCatalog(
				db, cluster, pipelines, resolved, logger).
				WithUploads(storage).
				WithInstance(instance).
				// La clé de déploiement du projet remplace la clé partagée au
				// moment de composer le run. Un projet sans clé propre retombe
				// sur la partagée : c'est l'état d'avant le lot G, et le bon
				// pour un projet déclaré avant lui.
				WithProjectKeys(db)

			// Les livrables passent par la file comme le reste, mais sont
			// produits ici : générer un bon de livraison lit la base et écrit un
			// objet. Le faire traverser Tekton coûterait un pod et un workspace
			// pour produire deux pages.
			scheduler.Handle(worker.NewExporter(db, storage, exportRegistry(cfg, logger), cfg.Registry.Image, logger))

			// La note de release, pour la même raison : elle lit Redmine, la
			// forge et la base. Sans forge configurée, la tâche n'est pas
			// enregistrée et les jobs de note échouent en le disant — plutôt que
			// de composer des notes vides sans expliquer pourquoi.
			if cfg.Forge.Configured() {
				forgeClient, err := gitea.New(gitea.Options{
					URL: cfg.Forge.URL, Token: cfg.Forge.Token,
					Repository: cfg.Forge.Repository, Timeout: cfg.Forge.Timeout,
				})
				if err != nil {
					return fmt.Errorf("client Gitea du worker: %w", err)
				}
				scheduler.Handle(worker.NewNoteBuilder(
					forgeClient, db, cfg.Tracker.ReviewStatuses, logger))
				logger.Info("note de release",
					"statuts_en_revue", cfg.Tracker.ReviewStatuses)
			} else {
				logger.Warn("forge non configurée: les notes de release ne seront pas composées")
			}

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

			// Une place par boucle, et pas moins : seule la **première** erreur est
			// lue — une boucle morte doit faire perdre le bail plutôt que de
			// laisser l'autre tourner à moitié — et les suivantes ne sont donc
			// jamais dépilées.
			//
			// Sur un canal saturé, elles bloqueraient leur goroutine pour toujours,
			// y compris à l'arrêt où chacune rend `ctx.Err()`. Le processus
			// sortirait quand même, mais en laissant derrière lui des boucles qui
			// n'ont jamais pu se conclure — et un profil de goroutines qui accuse
			// une fuite là où il n'y a qu'un canal trop court.
			errs := make(chan error, 8)
			go func() { errs <- observer.Run(leadingCtx) }()
			go func() { errs <- scheduler.Run(leadingCtx) }()
			go func() { errs <- environments.Run(leadingCtx) }()
			go func() { errs <- purger.Run(leadingCtx) }()

			// La pose des clés de déploiement. Sous le bail comme le reste : deux
			// workers qui poseraient la même clé se la remplaceraient l'un
			// l'autre, et la moitié publique enregistrée ne serait plus celle du
			// Secret déposé.
			keys := projectKeys(cluster, cfg, pipelines, logger)
			go func() {
				errs <- worker.NewKeyReconciler(db, keys, logger).Run(leadingCtx)
			}()

			// Les bibliothèques de référence, posées pour les piles que les
			// projets déclarent. Sous le bail comme le reste : deux workers qui
			// poseraient la même définition se la réécriraient l'un l'autre.
			if installer, err := libraryInstaller(cluster, cfg); err != nil {
				logger.Warn("bibliothèques de pipelines indisponibles",
					"raison", err,
					"consequence", "les projets emploieront les pipelines déjà présents")
			} else {
				go func() {
					errs <- worker.NewLibraryReconciler(db, installer, logger).Run(leadingCtx)
				}()
			}

			// La synchronisation du suivi. Elle ne porte plus sa boucle : elle est
			// devenue une **sonde**, et c'est la boucle commune qui résout ses
			// réglages, desselle sa clé, tient son curseur et isole ses échecs.
			//
			// La projection n'a pas bougé — versions, adoptions, demandes et leurs
			// seaux. Ce qui s'ajoute, ce sont les faits qu'elle constate au
			// passage, et sur lesquels une réaction peut désormais se brancher.
			tracker := worker.NewTrackerSync(db, trackerConnector(cfg), logger).
				WithNormalizer(versionNames(cfg))

			// Les sondes, qui produisent les faits d'un plugin. Sous le bail comme
			// le reste : deux workers interrogeant la même instance doubleraient
			// la charge sur un service tiers pour un résultat identique.
			//
			// Une sonde n'est interrogée que pour les projets dont le plugin est
			// actif, chacune à sa cadence, et sa première passe ne déclenche rien —
			// une activation ne réveille pas le passé (doc 14 §14.4).
			probes := worker.NewProbeRunner(db, cipher, cfg.Probes.Interval, logger,
				worker.NewGiteaPullProbe(cfg.Forge.Timeout),
				worker.NewRedmineProbe(tracker)).
				WithDispatcher(dispatcher)
			go func() { errs <- probes.Run(leadingCtx) }()

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
		cfg.Forge.CloneURL(os.Getenv("FORGE_SSH_PORT")))
	if appRepo == "" {
		appRepo = "ssh://git@forge.cadoles.com:2222/CNOUS/mse.git"
	}

	return map[string]string{
		"tekton.namespace": cfg.Tekton.Namespace,
		// Noms des secrets montés sur les workspaces. Le dashboard **désigne** les
		// identifiants, il ne les manipule jamais : `secrets` est volontairement
		// absent de sa RBAC (doc 07 §7.3), et une clé passée en paramètre
		// atterrirait dans la spec du PipelineRun. Même principe que les URLs
		// pré-signées de B14.
		//
		// Les secrets vivent dans le namespace des PipelineRun, pas dans celui du
		// dashboard : c'est là que Tekton les monte.
		"pipeline.gitSecret":      envOr("PIPELINE_GIT_SECRET", "git-credentials"),
		"pipeline.registrySecret": envOr("PIPELINE_REGISTRY_SECRET", "regcred"),
		// Secret de tirage des images d'outillage, distinct de celui qui sert à
		// pousser : le pod tire depuis `cnous-mse`, `crane` pousse vers le projet
		// de la release. Deux portées, potentiellement deux comptes.
		"pipeline.imagePullSecret": envOr("PIPELINE_IMAGE_PULL_SECRET", "regcred"),
		"forge.mse.repoUrl":        appRepo,
		"forge.mseKustom.repoUrl":  envOr("FORGE_MSE_KUSTOM_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-kustom.git"),
		// La branche de l'infrastructure de déploiement à auditer. Sans elle,
		// l'audit retombait sur le défaut du pipeline — `unstable`, la convention
		// de `CNOUS/mse-kustom` — et échouait au clone sur tout dépôt qui ne la
		// porte pas : « couldn't find remote ref unstable ».
		"forge.mseKustom.revision": envOr("FORGE_MSE_KUSTOM_REVISION", "unstable"),
		"forge.e2e.repoUrl":        envOr("FORGE_E2E_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-tests-e2e.git"),
		// L'audit de sécurité lit ses règles Semgrep et ses politiques dans
		// `mse-tekton` : c'est un dépôt qu'il clone, au même titre que l'application
		// et l'infrastructure de déploiement.
		"forge.tekton.repoUrl": envOr("FORGE_TEKTON_REPO_URL", "ssh://git@forge.cadoles.com:2222/CNOUS/mse-tekton.git"),
		"registry.project":     envOr("REGISTRY_PROJECT", "reg.cadoles.com/cnous-mse"),
		"mse.overlayLocation":  envOr("MSE_OVERLAY_LOCATION", "overlays/pipeline"),
		"mse.dockerfile":       envOr("MSE_DOCKERFILE", "./misc/k8s/images/portal/Dockerfile"),
		// Contexte de build, distinct du Dockerfile : `mse` place le sien dans
		// une sous-arborescence tout en construisant depuis la racine.
		"mse.buildContext": envOr("MSE_BUILD_CONTEXT", "."),
		"mse.pipelineTag":  envOr("MSE_PIPELINE_TAG", "pipeline"),
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

	// L'amorçage suit la migration, dans le même Job : le schéma qu'il alimente
	// vient d'être posé, et lui donner son propre déclencheur laisserait une
	// fenêtre où l'api démarre sur une base sans administrateur — personne ne
	// pourrait alors rien attribuer depuis l'interface.
	//
	// Une étape déjà appliquée n'est jamais rejouée, même si la configuration a
	// changé depuis. C'est ce qui rend la base seule maîtresse après le premier
	// passage : un dépôt retiré de l'interface ne revient pas, et un rôle retiré à
	// quelqu'un ne se réattribue pas à la faveur d'un redéploiement.
	return bootstrap(ctx, cfg, logger)
}

// bootstrap pose les données d'amorçage, une fois.
//
// Il ouvre sa propre connexion plutôt que d'en recevoir une : `runMigrate`
// travaille jusqu'ici sans pool, chaque opération ouvrant la sienne, et lui en
// faire porter un pour la seule étape qui en a besoin étendrait sa durée de vie à
// tout le Job.
func bootstrap(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	db, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	cipher, err := openCipher(cfg, logger)
	if err != nil {
		return err
	}
	plugins, err := configuredPlugins(cfg, cipher, logger)
	if err != nil {
		return err
	}

	report, err := db.Bootstrap(ctx, store.BootstrapSpec{
		Grants:          configuredGrants(cfg),
		Projects:        configuredProjects(cfg),
		Plugins:         plugins,
		TrackerPluginID: trackerPluginID,
	})

	// Le rapport est journalisé avant l'erreur : une étape peut avoir abouti
	// pendant qu'une autre échouait, et le taire ferait croire à un amorçage
	// entièrement raté.
	if report.GrantsApplied > 0 || report.ProjectsApplied > 0 ||
		report.TrackersApplied > 0 || report.PluginsApplied > 0 {
		logger.Info("amorçage appliqué",
			"roles", report.GrantsApplied, "depots", report.ProjectsApplied,
			"suivis", report.TrackersApplied, "plugins", report.PluginsApplied)
	}
	if len(report.Skipped) > 0 {
		logger.Info("amorçage déjà fait, étapes ignorées",
			"steps", report.Skipped, "source", "la base fait foi")
	}
	if err != nil {
		return fmt.Errorf("amorçage: %w", err)
	}
	return nil
}

// configuredGrants rend les rôles déclarés dans la configuration.
//
// Deux variables, et la seconde n'est pas un doublon : `BOOTSTRAP_ADMINS` ne sait
// dire qu'« administrateur », ce qui suffit à débloquer une base neuve mais oblige
// à passer par l'interface pour tout le reste. `BOOTSTRAP_USERS` porte le rôle
// avec le compte — `alice:releaser` — et permet de décrire une équipe entière
// avant sa première connexion.
//
// Un compte nommé dans les deux prend le rôle le plus explicite : une déclaration
// qui précise `viewer` ne doit pas être écrasée par un raccourci qui dit
// « administrateur ».
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

	// L'ordre d'une map n'est pas stable, et un amorçage doit produire la même
	// chose à chaque exécution — ne serait-ce que pour que ses journaux se
	// comparent.
	sort.Slice(grants, func(i, j int) bool { return grants[i].Identifier < grants[j].Identifier })
	return grants
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

	db, err := openStore(ctx, cfg)
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
		// Le tag n'est unique que dans un projet : le rejeu vise donc celui qui
		// se construit, comme le reste du dashboard tant que les routes ne
		// portent pas le projet.
		project, err := db.BuildableProject(ctx)
		if err != nil {
			return err
		}
		release, err := db.GetRelease(ctx, project.ID, *releaseTag)
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
		URL:     cfg.Tracker.URL,
		APIKey:  cfg.Tracker.APIKey,
		Timeout: cfg.Tracker.Timeout,
	})
	if err != nil {
		return fmt.Errorf("client Redmine: %w", err)
	}

	account, err := client.CheckAccess(ctx)
	if err != nil {
		return fmt.Errorf("vérification de l'accès Redmine: %w", err)
	}

	logger.Info("accès Redmine vérifié",
		"url", cfg.Tracker.URL,
		"project", cfg.Tracker.Project,
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

// projectsHandler assemble la déclaration des dépôts pilotés.
//
// Une seule clé désormais, celle du projet, posée par le worker : l'API tourne
// sans jeton de cluster, délibérément, parce que c'est la surface exposée
// (doc 12 §12.7). La clé **partagée** qui coexistait avec elle a été retirée —
// une clé unique ajoutée à plusieurs dépôts fait de sa révocation une panne pour
// tous à la fois, et ne dit rien de qui a cloné quoi.
func projectsHandler(cfg config.Config, db *store.DB) *dashhttp.ProjectsHandler {
	handler := dashhttp.NewProjectsHandler(db).
		WithForgeURL(cfg.Forge.URL).
		WithImageRegistry(cfg.Registry.Project).
		// Le même déployeur que celui qui promeut, et sans client : la question
		// posée — « sais-tu acheminer ce schéma ? » — ne demande pas le cluster,
		// et un aperçu qui répondrait autrement que la promotion elle-même serait
		// pire que pas d'aperçu.
		WithDeployer(flux.NewDeployer(nil, flux.DefaultPromotionKind, ""))

	if cfg.Forge.URL == "" || cfg.Forge.Token == "" {
		return handler
	}
	return handler.WithProbe(gitea.Prober{
		URL: cfg.Forge.URL, Token: cfg.Forge.Token, Timeout: cfg.Forge.Timeout,
	})
}

// openStore ouvre le pool et lui applique la convention de nommage des versions.
//
// Un point d'ouverture unique et non quatre : la forme normalisée est une colonne,
// et un binaire qui l'écrirait avec d'autres préfixes que celui qui la lit
// rendrait le rapprochement muet. Une ouverture qui oublierait le normaliseur ne
// casserait rien à la compilation — d'où le passage obligé.
func openStore(ctx context.Context, cfg config.Config) (*store.DB, error) {
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	return db.WithNormalizer(versionNames(cfg)), nil
}

// versionNames construit le normaliseur déclaré par la configuration.
func versionNames(cfg config.Config) release.Normalizer {
	return release.NewNormalizer(cfg.Versions.NamePrefixes)
}

// configuredProjects rend les dépôts que la configuration désigne.
//
// Quatre, et ils ne jouent pas le même rôle : l'application est construite, les
// trois autres sont clonés — la kustomization par les tests d'intégration, les
// pipelines par l'audit qui y lit ses règles, la campagne E2E par elle-même.
// Seule la première porte donc un Dockerfile.
//
// Les étapes de l'application sont nommées ici, et nulle part ailleurs. Elles
// vivaient en constantes Go — `portal`, `portal-cli`, `portal-dev` — que le
// dashboard parcourait pour savoir quoi construire. Elles décrivent un dépôt
// donné, pas le produit : leur place est dans l'amorçage, qui est justement ce
// qui installe une vérité propre à un déploiement avant que la base ne prenne le
// relais (doc 12 §12.9).
func configuredProjects(cfg config.Config) []store.Project {
	resolved := schedulerConfig(cfg)

	promoted, held := true, false
	targets := []store.Target{
		// Le runtime HTTP : front office, back office, web services.
		{Stage: "web", Image: "portal", Promote: &promoted},
		// Les consumers Messenger, les cronjobs et les jobs. Nommée
		// `portal-consumer` jusqu'à la migration FrankenPHP, où elle a cessé de
		// ne servir que les consumers.
		{Stage: "cli", Image: "portal-cli", Promote: &promoted},
		// L'image « grasse » : xdebug, xhprof, client PostgreSQL. Elle fait
		// tourner le job de bootstrap des overlays à fausses données et permet de
		// reproduire un défaut sur exactement la version livrée. Construite mais
		// non promue : aucun environnement du flux ne la déploie.
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
			clone: resolved["forge.mse.repoUrl"],
			label: "Application",
			// Le seul des quatre à avoir un outil de suivi. Les trois autres sont
			// des dépôts clonés par des pipelines : ils ne portent ni version ni
			// release, donc rien à rapprocher.
			tracker:    cfg.Tracker.Project,
			dockerfile: resolved["mse.dockerfile"],
			context:    resolved["mse.buildContext"],
			targets:    targets,
		},
		{clone: resolved["forge.mseKustom.repoUrl"], label: "Kustomizations"},
		{clone: resolved["forge.tekton.repoUrl"], label: "Pipelines Tekton"},
		{clone: resolved["forge.e2e.repoUrl"], label: "Campagne E2E"},
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

		// Le registre n'est renseigné que sur ce qui se construit : un dépôt
		// cloné sans image n'en publie aucune, et lui en attribuer un ferait
		// croire le contraire.
		registry := ""
		if declaration.dockerfile != "" {
			registry = resolved["registry.project"]
		}

		projects = append(projects, store.Project{
			Repository: repository, Label: declaration.label, ForgeURL: forgeURL,
			TrackerProject: declaration.tracker,
			Dockerfile:     declaration.dockerfile, BuildContext: buildContext,
			ImageRegistry: registry,
			// Plusieurs cibles seulement là où il y en a : un Dockerfile simple
			// coché « multi-étapes » ferait attendre une liste qui n'existe pas.
			MultiStage: len(declaration.targets) > 0,
			Targets:    declaration.targets,
			// L'auteur est la configuration elle-même, et le dire évite
			// d'attribuer à quelqu'un une déclaration que personne n'a faite.
			CreatedBy: "configuration",
		})
	}
	return projects
}

// projectKeys ouvre de quoi poser une clé de déploiement par projet.
//
// Appelé depuis le rôle `api`, parce que c'est lui qui déclare les projets — et
// non le worker, qui n'en voit que les jobs. Les deux partagent le même compte
// de service, donc la même Role : le droit d'écrire un Secret sans pouvoir en
// lire aucun est déjà accordé (doc 12 §12.7).
//
// **Rend nil plutôt qu'une erreur.** Un dashboard sans accès au cluster reste
// parfaitement utilisable : les projets s'y déclarent, et emploient la clé
// partagée du déploiement. Faire échouer le démarrage sur une dépendance qui ne
// sert qu'à une fonction serait le même défaut que celui corrigé au lot D, où
// l'attestation absente empêchait le déployeur d'exister.
func projectKeys(
	cluster *tekton.Client, cfg config.Config, pipelines *catalog.Catalog, logger *slog.Logger,
) *deploykeys.Issuer {
	namespace := cfg.Tekton.Namespace
	owner := deploykeys.NewOwner(cluster.Kube, namespace)
	// Tous les comptes que le catalogue emploie : n'en tenir qu'un rendrait la
	// clé montable pour certains pipelines du projet et pas pour les autres.
	accounts := pipelines.ServiceAccounts()
	mounter := deploykeys.NewMounter(cluster.Kube, namespace, accounts...)
	logger.Info("clés de déploiement par projet",
		"namespace", namespace, "comptes_de_service", accounts)
	return deploykeys.NewIssuer(owner, mounter)
}

// libraryInstaller ouvre de quoi poser les bibliothèques de référence.
//
// La découverte des versions servies est refaite ici plutôt qu'empruntée au
// client Tekton : celui-ci est typé, alors que la pose manipule des manifestes
// quelconques par le client dynamique. Les deux parlent au même cluster, avec la
// même résolution.
func libraryInstaller(
	cluster *tekton.Client, cfg config.Config,
) (*pipelinelib.Installer, error) {
	dynamicClient, err := dynamic.NewForConfig(cluster.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("client dynamique: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cluster.RESTConfig)
	if err != nil {
		return nil, fmt.Errorf("client de découverte: %w", err)
	}

	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil && len(resources) == 0 {
		return nil, fmt.Errorf("découverte des API du cluster: %w", err)
	}

	return pipelinelib.NewInstaller(dynamicClient,
		restmapper.NewDiscoveryRESTMapper(resources), cfg.Tekton.Namespace), nil
}
