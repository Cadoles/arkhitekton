// Package config charge et valide la configuration du dashboard.
//
// Toute la validation a lieu au démarrage, avant qu'un seul port ne soit ouvert.
// Le principe vaut d'être explicite : un dashboard qui démarre avec une
// configuration incomplète découvre le problème au premier clic d'un utilisateur,
// souvent sur l'opération la plus rare — une promotion en production. Échouer au
// démarrage transforme une panne fonctionnelle diffuse en erreur de déploiement,
// visible immédiatement et par la bonne personne.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
)

// Config regroupe la configuration du binaire, quel que soit son rôle (api,
// worker, migrate). Chaque rôle n'en valide que ce qu'il utilise.
type Config struct {
	Role     Role
	HTTP     HTTPConfig
	Database DatabaseConfig
	Storage  StorageConfig
	OIDC     OIDCConfig
	Tekton   TektonConfig
	Flux     FluxConfig
	Registry RegistryConfig
	Tracker  TrackerConfig
	Forge    ForgeConfig
	Versions VersionsConfig
	Results  ResultsConfig
	Secrets  SecretsConfig
	Webhooks WebhookConfig
}

// WebhookConfig borne ce qu'un appel sortant configurable peut atteindre.
//
// Les deux autorisations sont **fausses par défaut**, et c'est le bon défaut :
// une URL saisie dans l'interface devient une requête émise depuis le cluster,
// par un processus qui porte un jeton de compte de service. Ouvrir d'office
// reviendrait à offrir une falsification de requête côté serveur (doc 14 §14.7).
type WebhookConfig struct {
	// AllowPrivate autorise les cibles privées et la boucle locale. Un
	// déploiement local en a besoin ; une production, non.
	//
	// Ne rouvre jamais le lien-local : les métadonnées d'instance ne sont pas une
	// cible qu'un déploiement veut atteindre.
	AllowPrivate bool
	// AllowInsecure autorise `http://`. La charge porte le tag d'une release et
	// le jeton qui l'authentifie ; en clair, tout cela traverse un réseau
	// lisible.
	AllowInsecure bool
	// Timeout borne un appel. Un destinataire lent ne doit pas retenir le moteur
	// de plans : les gestes d'une étape partent ensemble, et un appel suspendu
	// bloquerait la conclusion de l'étape.
	Timeout time.Duration
}

// SecretsConfig porte la clé qui scelle les valeurs sensibles des réglages.
//
// Elle vit dans l'environnement et non dans la base — c'est tout l'intérêt : une
// sauvegarde de la base ne dit rien sans elle. Le corollaire est qu'elle doit
// être sauvegardée **ailleurs**, faute de quoi une restauration rend des réglages
// que plus rien n'ouvre.
//
// Son absence n'empêche pas de démarrer. Elle rend impossible l'enregistrement
// d'un champ sensible, ce que l'interface dit à l'endroit où on le saisit :
// refuser de démarrer punirait un déploiement qui n'a encore aucun réglage à
// protéger, et le message serait lu loin de la personne qui peut agir.
type SecretsConfig struct {
	// Key est la clé courante, en base64 de 32 octets : `openssl rand -base64 32`.
	// Elle ne doit apparaître ni dans un log, ni dans un message d'erreur.
	Key string
	// RetiredKeys porte les clés remplacées, qui n'ouvrent plus que l'existant.
	//
	// Une rotation se fait en deux temps — la nouvelle scelle, l'ancienne ouvre —
	// parce que les valeurs déjà en base ne se re-scellent qu'au fil des
	// écritures. Retirer l'ancienne trop tôt les rendrait illisibles.
	RetiredKeys []string
}

// Configured dit si le scellement est possible.
func (c SecretsConfig) Configured() bool { return strings.TrimSpace(c.Key) != "" }

// ResultsConfig porte la lecture des résultats laissés par les pipelines.
type ResultsConfig struct {
	// MarkerToken est la marque des lignes de protocole qu'une Task écrit sur sa
	// sortie standard : `MSE` donne `##MSE-META##`, vide donne `##META##`.
	//
	// Configurable parce que le nom d'un projet dans un **protocole** est un
	// couplage d'une autre nature que dans une interface : un pipeline générique
	// qui émet `##MSE-META##` reste marqué MSE dans son code source. Le
	// déploiement MSE déclare donc `MSE`, ce que ses Tasks émettent déjà
	// (doc 13 §13.3).
	MarkerToken string
}

// VersionsConfig porte la façon dont les noms de version se rapprochent.
type VersionsConfig struct {
	// NamePrefixes énumère les préfixes retirés pour comparer deux noms.
	//
	// Configurables parce que la convention appartient au client : l'instance
	// réelle porte `v3.2.0` et `MSE 3.1.0` dans le même projet, et le second est
	// le nom d'un client, pas une convention de versionnement. Il était en dur
	// dans `internal/release` (doc 12 §12.9, chantier 5).
	//
	// Attention : la forme normalisée est **stockée** dans `redmine_versions`.
	// Changer cette liste rend la colonne obsolète, et le démarrage la recalcule
	// pour cette raison — voir `store.RenormalizeVersions`.
	NamePrefixes []string
}

// ForgeConfig porte l'accès en lecture à la forge.
//
// **Lecture seule**, appliquée par le transport de `internal/gitea` comme pour
// Redmine. Sert au formulaire de création de release : proposer les branches et
// les tags du dépôt plutôt qu'un champ libre, où une faute de frappe ne se
// découvre qu'à l'échec du build.
//
// Poser un tag ou publier une release Gitea restera un client distinct et une
// décision explicite, pas un assouplissement de celui-ci.
type ForgeConfig struct {
	URL string
	// Token ne doit apparaître ni dans un log, ni dans un message d'erreur, ni
	// dans une URL. Il ouvre la forge entière au nom de son détenteur : demander
	// un jeton à portée `read:repository` suffit et vaut mieux.
	Token string
	// Repository au format `owner/nom` — `CNOUS/mse`.
	Repository string
	Timeout    time.Duration
}

// Configured dit si l'accès est renseigné. L'absence est un état normal : le
// formulaire retombe sur la saisie libre et le dit.
func (c ForgeConfig) Configured() bool {
	return c.URL != "" && c.Token != "" && c.Repository != ""
}

// TrackerConfig porte l'accès en lecture aux demandes et aux versions.
//
// **Lecture seule, et l'instance visée est la production.** Le dashboard consulte
// les versions et les demandes ; il n'en crée ni n'en modifie aucune. Créer une
// version, fermer une demande, poster une note restent des gestes humains faits
// dans Redmine.
//
// L'interdiction n'est pas confiée à la discipline des appelants : le transport de
// `internal/redmine` refuse toute méthode autre que GET et HEAD avant d'atteindre
// le réseau. Une clé de production circule ici, sur une base sans sauvegarde à
// notre disposition — la garantie devait être structurelle.
type TrackerConfig struct {
	URL string
	// APIKey ne doit apparaître ni dans un log, ni dans un message d'erreur, ni
	// dans une URL. Elle est transmise en en-tête `X-Redmine-API-Key`, jamais en
	// chaîne de requête, où elle finirait dans les journaux d'accès.
	APIKey string
	// Project est l'identifiant du projet, tel qu'il apparaît dans l'URL Redmine.
	Project string
	// Timeout borne un appel : une lenteur de Redmine ne doit pas devenir une
	// lenteur du dashboard.
	Timeout time.Duration
	// ReviewStatuses énumère les statuts qui rendent une demande éligible à la
	// note d'une release. Le vocabulaire appartient au client : il peut renommer
	// « En revue » sans qu'aucun code ait à changer.
	ReviewStatuses []string
}

// Configured dit si l'accès est renseigné.
//
// L'absence est un état normal, pas une erreur : le dashboard tourne sans Redmine,
// les écrans qui en dépendent affichant un état dégradé explicite (doc 08 §8.2).
func (c TrackerConfig) Configured() bool {
	return c.URL != "" && c.APIKey != ""
}

// RegistryConfig porte l'accès en lecture au registre d'images.
//
// Lecture seule : le dashboard ne pousse jamais d'image. Le re-tag de promotion
// est fait par la Task `promote-image`, avec ses propres identifiants — donner
// l'écriture ici ferait porter au dashboard un pouvoir qu'il n'exerce pas.
type RegistryConfig struct {
	URL      string
	Username string
	// Password ne doit apparaître ni dans un log, ni dans un message d'erreur.
	Password string
	// Project est le préfixe des dépôts (`cnous-mse`, ou `vcarroy` en local).
	Project string
	// Image est le dépôt promu à l'intérieur du projet. Configurable parce que le
	// nom du portail n'est pas une constante du dashboard.
	//
	// La seconde image publiée par une release — `portal-cli` — n'est pas ici :
	// elle n'est pas promue, elle suit. Son nom vit dans `internal/images`, avec
	// celui du portail par défaut, parce qu'il doit rester le même que celui que
	// `build-image` publie et que `mse-kustom` déploie.
	Image string
}

// FluxConfig borne la lecture des objets Flux.
type FluxConfig struct {
	// Namespace où vivent ImagePolicy et Kustomization. Distinct de celui des
	// pipelines : Flux vit dans `flux-system`.
	Namespace string
}

// Role décrit ce que fait le processus. Le binaire est unique, déployé en
// plusieurs exemplaires aux responsabilités distinctes (voir doc 03).
type Role string

const (
	RoleAPI     Role = "api"
	RoleWorker  Role = "worker"
	RoleMigrate Role = "migrate"
	// RoleReprocess rejoue les résultats depuis les artefacts bruts. Rôle
	// ponctuel, lancé à la main après correction d'un parseur : il n'a pas
	// vocation à tourner en permanence, d'où l'absence de Deployment.
	RoleReprocess Role = "reprocess"
	// RoleCheckTracker vérifie l'accès à Redmine et s'arrête. Rôle de diagnostic,
	// lancé à la main après avoir déposé une clé.
	//
	// Il existe parce que la clé visée est celle de la **production** : la seule
	// façon de savoir si elle fonctionne, sans lui, serait d'attendre qu'un écran
	// s'en serve. Une vérification explicite en lecture évite d'apprendre par une
	// panne qu'une clé était révoquée.
	RoleCheckTracker Role = "check-tracker"
)

type HTTPConfig struct {
	Addr            string
	PublicURL       string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

type DatabaseConfig struct {
	// DSN porte le mot de passe : il ne doit apparaître ni dans un log, ni dans
	// un message d'erreur. Voir Redacted().
	DSN             string
	MaxConns        int
	ConnMaxLifetime time.Duration
}

type StorageConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	// Region n'est pas décorative : une région erronée invalide la signature
	// SigV4, avec un message du serveur qui n'y renvoie pas. L'instance cible
	// utilise "NUO-Region" et non "us-east-1".
	Region string
	// PresignTTL doit couvrir le pipeline le plus long (90 min pour l'audit de
	// sécurité) sans excéder ce qui est raisonnable pour une capacité d'écriture.
	PresignTTL time.Duration
	UseSSL     bool
}

type OIDCConfig struct {
	// IssuerURL doit correspondre exactement à la claim `iss` du jeton, slash
	// final compris. Gitea annonce "https://forge.cadoles.com/" : l'omettre fait
	// échouer la vérification avec un message peu parlant.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// BootstrapAdmins reçoit les identifiants (`sub` ou nom d'utilisateur) promus
	// administrateurs au démarrage. Sans cette liste, personne ne peut attribuer
	// le premier rôle et l'interface est inutilisable après déploiement.
	BootstrapAdmins []string
	// BootstrapUsers déclare un rôle par compte — `alice:releaser` — là où
	// BootstrapAdmins ne sait dire qu'« administrateur ». Les deux servent au
	// même amorçage, posé une fois : ils ne sont plus relus ensuite, et la base
	// fait foi.
	BootstrapUsers map[string]string
	SessionTTL     time.Duration
}

type TektonConfig struct {
	Namespace      string
	KubeconfigPath string
	CatalogPath    string
	// LeaseNamespace porte le Lease d'élection de leader. Distinct de Namespace :
	// le bail vit dans le namespace du dashboard, où le compte de service a le
	// droit de l'écrire, et non dans celui des pipelines, où il n'a que la
	// lecture (doc 07 §7.3).
	LeaseNamespace string
	// DashboardURL est la racine du dashboard Tekton, s'il est déployé. Facultatif :
	// vide, l'interface n'affiche simplement pas le lien vers l'objet. En faire une
	// exigence obligerait à déployer un composant dont ce dashboard n'a pas besoin
	// pour fonctionner.
	DashboardURL string
}

// Load lit la configuration depuis l'environnement et la valide pour le rôle
// demandé. L'erreur retournée agrège tous les problèmes rencontrés : corriger
// une configuration à raison d'un redémarrage par variable manquante est une
// perte de temps évitable.
func Load(role Role) (Config, error) {
	cfg := Config{
		Role: role,
		HTTP: HTTPConfig{
			Addr:            envOr("HTTP_ADDR", ":8080"),
			PublicURL:       os.Getenv("PUBLIC_URL"),
			ReadTimeout:     durationOr("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    durationOr("HTTP_WRITE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: durationOr("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
		},
		Database: DatabaseConfig{
			DSN:             os.Getenv("DATABASE_URL"),
			MaxConns:        intOr("DATABASE_MAX_CONNS", 10),
			ConnMaxLifetime: durationOr("DATABASE_CONN_MAX_LIFETIME", time.Hour),
		},
		Storage: StorageConfig{
			Endpoint:   os.Getenv("MINIO_ENDPOINT"),
			Bucket:     envOr("MINIO_BUCKET", "mse-dashboard"),
			AccessKey:  os.Getenv("MINIO_ACCESS_KEY"),
			SecretKey:  os.Getenv("MINIO_SECRET_KEY"),
			Region:     envOr("MINIO_REGION", "NUO-Region"),
			PresignTTL: durationOr("MINIO_PRESIGN_TTL", 2*time.Hour),
			UseSSL:     boolOr("MINIO_USE_SSL", true),
		},
		OIDC: OIDCConfig{
			IssuerURL:       os.Getenv("OIDC_ISSUER_URL"),
			ClientID:        os.Getenv("OIDC_CLIENT_ID"),
			ClientSecret:    os.Getenv("OIDC_CLIENT_SECRET"),
			RedirectURL:     os.Getenv("OIDC_REDIRECT_URL"),
			Scopes:          listOr("OIDC_SCOPES", []string{"openid", "profile", "email"}),
			BootstrapAdmins: listOr("BOOTSTRAP_ADMINS", nil),
			BootstrapUsers:  parseGrants(os.Getenv("BOOTSTRAP_USERS")),
			SessionTTL:      durationOr("SESSION_TTL", 12*time.Hour),
		},
		Tekton: TektonConfig{
			Namespace:      envOr("TEKTON_NAMESPACE", "mse-tekton-pipelines"),
			DashboardURL:   strings.TrimRight(os.Getenv("TEKTON_DASHBOARD_URL"), "/"),
			KubeconfigPath: os.Getenv("KUBECONFIG"),
			CatalogPath:    envOr("PIPELINE_CATALOG_PATH", "/etc/mse-dashboard/catalog.yaml"),
			LeaseNamespace: envOr("POD_NAMESPACE", "mse-dashboard"),
		},
		Flux: FluxConfig{
			Namespace: envOr("FLUX_NAMESPACE", "flux-system"),
		},
		Registry: RegistryConfig{
			URL:      envOr("REGISTRY_URL", "https://reg.cadoles.com"),
			Username: os.Getenv("REGISTRY_USERNAME"),
			Password: os.Getenv("REGISTRY_PASSWORD"),
			Project:  envOr("REGISTRY_PROJECT", "reg.cadoles.com/cnous-mse"),
			Image:    envOr("REGISTRY_IMAGE", "portal"),
		},
		Results: ResultsConfig{
			MarkerToken: os.Getenv("MARKER_TOKEN"),
		},
		Versions: VersionsConfig{
			// `v` seul par défaut : il vient du versionnement sémantique et ne
			// suppose rien d'un projet donné. Tout le reste — `MSE `, un préfixe
			// d'équipe — se déclare, et le déploiement MSE le fait.
			NamePrefixes: prefixesOr("VERSION_NAME_PREFIXES", []string{release.DefaultPrefix}),
		},
		Tracker: TrackerConfig{
			// Les statuts qui rendent une demande éligible à la note de release.
			// Configurables parce que le vocabulaire appartient au client : il
			// peut renommer « En revue » sans qu'aucun code ait à changer.
			ReviewStatuses: listOr("TRACKER_REVIEW_STATUSES", []string{"En revue"}),
			URL:            strings.TrimRight(os.Getenv("TRACKER_URL"), "/"),
			APIKey:         os.Getenv("TRACKER_API_KEY"),
			Project:        os.Getenv("TRACKER_PROJECT"),
			Timeout:        durationOr("TRACKER_TIMEOUT", 15*time.Second),
		},
		Webhooks: WebhookConfig{
			AllowPrivate:  boolOr("WEBHOOK_ALLOW_PRIVATE", false),
			AllowInsecure: boolOr("WEBHOOK_ALLOW_INSECURE", false),
			Timeout:       durationOr("WEBHOOK_TIMEOUT", 15*time.Second),
		},
		Secrets: SecretsConfig{
			Key:         strings.TrimSpace(os.Getenv("SECRETS_KEY")),
			RetiredKeys: listOr("SECRETS_RETIRED_KEYS", nil),
		},
		Forge: ForgeConfig{
			URL:        strings.TrimRight(os.Getenv("FORGE_URL"), "/"),
			Token:      os.Getenv("FORGE_TOKEN"),
			Repository: os.Getenv("FORGE_REPO"),
			Timeout:    durationOr("FORGE_TIMEOUT", 10*time.Second),
		},
	}

	return cfg, cfg.validate()
}

// renamedVariables associe chaque variable d'environnement retirée à celle qui
// la remplace.
//
// La configuration nommait les produits — `GITEA_TOKEN`, `REDMINE_API_KEY` —
// comme le schéma les nommait avant les lots C et D. Le vocabulaire est
// désormais celui des frontières : une forge, un suivi (doc 12 §12.9, lot E).
//
// Le renommage est **refusé bruyamment** plutôt qu'absorbé. Un déploiement resté
// sur l'ancien nom se retrouverait avec une forge « non configurée » — un état
// valide, que le dashboard traite en retombant sur la saisie libre — et personne
// ne ferait le lien avec le renommage. Le suivi, lui, cesserait simplement de se
// synchroniser. Deux dégradations silencieuses, que le démarrage doit empêcher.
var renamedVariables = map[string]string{
	"GITEA_URL":               "FORGE_URL",
	"GITEA_TOKEN":             "FORGE_TOKEN",
	"GITEA_REPO":              "FORGE_REPO",
	"GITEA_TIMEOUT":           "FORGE_TIMEOUT",
	"REDMINE_URL":             "TRACKER_URL",
	"REDMINE_API_KEY":         "TRACKER_API_KEY",
	"REDMINE_PROJECT":         "TRACKER_PROJECT",
	"REDMINE_TIMEOUT":         "TRACKER_TIMEOUT",
	"REDMINE_REVIEW_STATUSES": "TRACKER_REVIEW_STATUSES",
}

// removedVariables associe chaque variable supprimée à la raison de sa
// suppression.
//
// Distinctes des renommées : il n'y a pas de remplaçante à indiquer, seulement un
// mécanisme qui n'existe plus. Le refus reste bruyant pour la même raison —
// laisser la variable en place ferait croire à un déploiement qu'il configure
// quelque chose, alors que plus rien ne la lit.
var removedVariables = map[string]string{
	"PIPELINE_GIT_PUBLIC_KEY": "la clé de déploiement partagée n'existe plus: " +
		"chaque projet a la sienne, posée par le dashboard et activée depuis " +
		"l'écran du projet",
}

// checkRenamed refuse de démarrer sur une variable retirée.
//
// Vérifié quel que soit le rôle : une variable héritée est une faute de
// configuration, indépendamment de ce que le processus s'apprête à faire.
func checkRenamed() []error {
	// Trié, pour que deux démarrages sur la même configuration rendent le même
	// message. Une liste dont l'ordre change à chaque exécution se relit mal.
	legacy := make([]string, 0, len(renamedVariables))
	for name := range renamedVariables {
		if _, present := os.LookupEnv(name); present {
			legacy = append(legacy, name)
		}
	}
	sort.Strings(legacy)

	problems := make([]error, 0, len(legacy))
	for _, name := range legacy {
		problems = append(problems, fmt.Errorf(
			"%s n'existe plus, employer %s — la configuration ne nomme plus les "+
				"produits mais les frontières", name, renamedVariables[name]))
	}

	removed := make([]string, 0, len(removedVariables))
	for name := range removedVariables {
		if _, present := os.LookupEnv(name); present {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	for _, name := range removed {
		problems = append(problems, fmt.Errorf("%s n'existe plus: %s",
			name, removedVariables[name]))
	}
	return problems
}

func (c Config) validate() error {
	var problems []error

	problems = append(problems, checkRenamed()...)

	// La base est requise par tous les rôles de service : `migrate` l'applique,
	// `api`, `worker` et `reprocess` la lisent.
	//
	// `check-tracker` en est exempt, et c'est sa raison d'être : vérifier une clé
	// doit être possible depuis un poste qui n'a accès ni à la base ni au cluster.
	// Exiger un DSN ferait échouer le diagnostic pour une dépendance dont il ne se
	// sert pas, ce qui est exactement le genre de contrainte qui pousse à
	// contourner l'outil.
	if c.Role != RoleCheckTracker {
		if strings.TrimSpace(c.Database.DSN) == "" {
			problems = append(problems, errors.New("DATABASE_URL est requis"))
		}
		if c.Database.MaxConns < 1 {
			problems = append(problems, fmt.Errorf("DATABASE_MAX_CONNS doit être >= 1, vaut %d", c.Database.MaxConns))
		}
	}

	switch c.Role {
	case RoleMigrate:
		// Rien d'autre : appliquer des migrations ne demande ni stockage objet,
		// ni fournisseur d'identité, ni accès au cluster.
	case RoleAPI:
		problems = append(problems, c.validateHTTP()...)
		problems = append(problems, c.validateOIDC()...)
		problems = append(problems, c.validateStorage()...)
	case RoleReprocess:
		// Le stockage suffit : le rejeu lit le brut et écrit en base, sans
		// toucher au cluster ni créer le moindre run.
		problems = append(problems, c.validateStorage()...)
	case RoleCheckTracker:
		// Ni base, ni stockage, ni cluster : ce rôle ne fait qu'une lecture HTTP.
		// L'exiger davantage empêcherait de vérifier une clé depuis un poste qui
		// n'a pas accès au reste.
		if !c.Tracker.Configured() {
			problems = append(problems, errors.New("TRACKER_URL et TRACKER_API_KEY sont requis"))
		}
	case RoleWorker:
		problems = append(problems, c.validateStorage()...)
		if strings.TrimSpace(c.Tekton.Namespace) == "" {
			problems = append(problems, errors.New("TEKTON_NAMESPACE est requis"))
		}
		if strings.TrimSpace(c.Tekton.LeaseNamespace) == "" {
			problems = append(problems, errors.New("POD_NAMESPACE est requis (namespace du bail de leader)"))
		}
	default:
		problems = append(problems, fmt.Errorf(
			"rôle inconnu %q (attendu: api, worker, migrate, reprocess, check-tracker)", c.Role))
	}

	return errors.Join(problems...)
}

func (c Config) validateHTTP() []error {
	var problems []error
	if strings.TrimSpace(c.HTTP.Addr) == "" {
		problems = append(problems, errors.New("HTTP_ADDR est requis"))
	}
	if u := strings.TrimSpace(c.HTTP.PublicURL); u == "" {
		problems = append(problems, errors.New("PUBLIC_URL est requis (base des URLs de redirection)"))
	} else if _, err := url.Parse(u); err != nil {
		problems = append(problems, fmt.Errorf("PUBLIC_URL invalide: %w", err))
	}
	return problems
}

func (c Config) validateOIDC() []error {
	var problems []error
	for name, value := range map[string]string{
		"OIDC_ISSUER_URL":    c.OIDC.IssuerURL,
		"OIDC_CLIENT_ID":     c.OIDC.ClientID,
		"OIDC_CLIENT_SECRET": c.OIDC.ClientSecret,
		"OIDC_REDIRECT_URL":  c.OIDC.RedirectURL,
	} {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, fmt.Errorf("%s est requis", name))
		}
	}

	// Sans administrateur d'amorçage, l'authentification fonctionne mais aucun
	// utilisateur ne peut rien faire, et personne ne peut débloquer la situation
	// depuis l'interface. Le symptôme — « tout le monde est refusé » — ne désigne
	// pas sa cause ; autant refuser de démarrer.
	if len(c.OIDC.BootstrapAdmins) == 0 {
		problems = append(problems, errors.New(
			"BOOTSTRAP_ADMINS est requis: sans lui aucun rôle ne peut être attribué"))
	}

	if c.OIDC.SessionTTL <= 0 {
		problems = append(problems, errors.New("SESSION_TTL doit être > 0"))
	}
	return problems
}

func (c Config) validateStorage() []error {
	var problems []error
	for name, value := range map[string]string{
		"MINIO_ENDPOINT":   c.Storage.Endpoint,
		"MINIO_BUCKET":     c.Storage.Bucket,
		"MINIO_ACCESS_KEY": c.Storage.AccessKey,
		"MINIO_SECRET_KEY": c.Storage.SecretKey,
		"MINIO_REGION":     c.Storage.Region,
	} {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, fmt.Errorf("%s est requis", name))
		}
	}
	if c.Storage.PresignTTL < 90*time.Minute {
		// Le pipeline security-audit a un timeout de 90 minutes. Une URL qui
		// expire avant la fin du run fait échouer la publication au tout dernier
		// moment, après que le travail coûteux a été fait.
		problems = append(problems, fmt.Errorf(
			"MINIO_PRESIGN_TTL vaut %s, insuffisant: le pipeline le plus long dure 90m",
			c.Storage.PresignTTL))
	}
	return problems
}

// Redacted rend une copie sûre à journaliser : les secrets sont remplacés par un
// marqueur, sans masquer leur présence ou leur absence.
func (c Config) Redacted() Config {
	out := c
	out.Database.DSN = redactDSN(c.Database.DSN)
	out.Storage.SecretKey = redact(c.Storage.SecretKey)
	out.OIDC.ClientSecret = redact(c.OIDC.ClientSecret)
	out.Registry.Password = redact(c.Registry.Password)
	out.Tracker.APIKey = redact(c.Tracker.APIKey)
	out.Forge.Token = redact(c.Forge.Token)
	out.Secrets.Key = redact(c.Secrets.Key)
	// Les clés retirées sont masquées une à une plutôt que remplacées par une
	// liste vide : leur **nombre** dit où en est une rotation, et c'est justement
	// ce qu'on vient lire dans un journal de démarrage.
	if len(c.Secrets.RetiredKeys) > 0 {
		retired := make([]string, len(c.Secrets.RetiredKeys))
		for index, key := range c.Secrets.RetiredKeys {
			retired[index] = redact(key)
		}
		out.Secrets.RetiredKeys = retired
	}
	return out
}

func redact(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "<masqué>"
}

// redactDSN conserve la structure de l'URL — hôte, base, options — et n'ôte que
// le mot de passe. Un DSN entièrement masqué ne permet plus de diagnostiquer une
// erreur de connexion, ce qui est précisément le moment où on le journalise.
func redactDSN(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return ""
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil {
		return "<masqué>"
	}
	if _, hasPassword := parsed.User.Password(); !hasPassword {
		return dsn
	}
	parsed.User = url.UserPassword(parsed.User.Username(), "xxxxx")
	return parsed.String()
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func intOr(key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return v
}

func boolOr(key string, fallback bool) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return v
}

func durationOr(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return v
}

// listOr découpe une valeur séparée par des virgules, en ignorant les entrées
// vides — "a,,b" et "a, b" donnent le même résultat.
// prefixesOr lit une liste de préfixes en **préservant leur espace final**.
//
// Distinct de listOr, et la différence n'est pas cosmétique : `MSE ` et `MSE` ne
// désignent pas la même règle. Le premier ramène `MSE 3.1.0` à `3.1.0` et laisse
// `MSE3-1.5` intact ; le second ramènerait aussi `MSE3-1.5` à `3-1.5`, c'est-à-dire
// rattacherait treize versions closes à des numéros inventés. listOr, qui coupe
// des deux côtés, produirait silencieusement le second.
//
// L'espace **avant** un élément est retiré : il vient de la virgule de séparation
// — `v, MSE ` — et le conserver ferait échouer toute correspondance.
func prefixesOr(key string, fallback []string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimLeft(part, " \t")
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func listOr(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// bootstrapRoles borne ce qu'un amorçage peut attribuer.
//
// Alignée sur la contrainte du schéma : une valeur hors liste ferait échouer
// l'insertion en cours d'amorçage, sur un message qui désignerait une contrainte
// plutôt qu'une variable mal remplie.
var bootstrapRoles = map[string]bool{
	"viewer": true, "operator": true, "releaser": true, "admin": true,
}

// parseGrants lit `compte:role,autre:role`.
//
// Une entrée sans rôle reconnu est **écartée** plutôt que refusée au démarrage :
// l'amorçage n'est pas la raison d'être du dashboard, et le priver de démarrage
// pour une virgule en trop empêcherait aussi de corriger depuis l'interface. Les
// déclarations en attente sont journalisées au démarrage de l'api, où l'absence
// se voit.
func parseGrants(raw string) map[string]string {
	grants := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		identifier, role, found := strings.Cut(entry, ":")
		identifier = strings.TrimSpace(identifier)
		role = strings.ToLower(strings.TrimSpace(role))
		if !found || identifier == "" || !bootstrapRoles[role] {
			continue
		}
		grants[identifier] = role
	}
	return grants
}
