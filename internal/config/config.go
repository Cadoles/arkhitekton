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
	"strconv"
	"strings"
	"time"
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
	Redmine  RedmineConfig
	Gitea    GiteaConfig
}

// GiteaConfig porte l'accès en lecture à la forge.
//
// **Lecture seule**, appliquée par le transport de `internal/gitea` comme pour
// Redmine. Sert au formulaire de création de release : proposer les branches et
// les tags du dépôt plutôt qu'un champ libre, où une faute de frappe ne se
// découvre qu'à l'échec du build.
//
// Poser un tag ou publier une release Gitea restera un client distinct et une
// décision explicite, pas un assouplissement de celui-ci.
type GiteaConfig struct {
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
func (c GiteaConfig) Configured() bool {
	return c.URL != "" && c.Token != "" && c.Repository != ""
}

// RedmineConfig porte l'accès en lecture aux demandes et aux versions.
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
type RedmineConfig struct {
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
func (c RedmineConfig) Configured() bool {
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
	// RoleCheckRedmine vérifie l'accès à Redmine et s'arrête. Rôle de diagnostic,
	// lancé à la main après avoir déposé une clé.
	//
	// Il existe parce que la clé visée est celle de la **production** : la seule
	// façon de savoir si elle fonctionne, sans lui, serait d'attendre qu'un écran
	// s'en serve. Une vérification explicite en lecture évite d'apprendre par une
	// panne qu'une clé était révoquée.
	RoleCheckRedmine Role = "check-redmine"
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
	SessionTTL      time.Duration
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
		Redmine: RedmineConfig{
			// Les statuts qui rendent une demande éligible à la note de release.
			// Configurables parce que le vocabulaire appartient au client : il
			// peut renommer « En revue » sans qu'aucun code ait à changer.
			ReviewStatuses: listOr("REDMINE_REVIEW_STATUSES", []string{"En revue"}),
			URL:            strings.TrimRight(os.Getenv("REDMINE_URL"), "/"),
			APIKey:         os.Getenv("REDMINE_API_KEY"),
			Project:        os.Getenv("REDMINE_PROJECT"),
			Timeout:        durationOr("REDMINE_TIMEOUT", 15*time.Second),
		},
		Gitea: GiteaConfig{
			URL:        strings.TrimRight(os.Getenv("GITEA_URL"), "/"),
			Token:      os.Getenv("GITEA_TOKEN"),
			Repository: os.Getenv("GITEA_REPO"),
			Timeout:    durationOr("GITEA_TIMEOUT", 10*time.Second),
		},
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var problems []error

	// La base est requise par tous les rôles de service : `migrate` l'applique,
	// `api`, `worker` et `reprocess` la lisent.
	//
	// `check-redmine` en est exempt, et c'est sa raison d'être : vérifier une clé
	// doit être possible depuis un poste qui n'a accès ni à la base ni au cluster.
	// Exiger un DSN ferait échouer le diagnostic pour une dépendance dont il ne se
	// sert pas, ce qui est exactement le genre de contrainte qui pousse à
	// contourner l'outil.
	if c.Role != RoleCheckRedmine {
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
	case RoleCheckRedmine:
		// Ni base, ni stockage, ni cluster : ce rôle ne fait qu'une lecture HTTP.
		// L'exiger davantage empêcherait de vérifier une clé depuis un poste qui
		// n'a pas accès au reste.
		if !c.Redmine.Configured() {
			problems = append(problems, errors.New("REDMINE_URL et REDMINE_API_KEY sont requis"))
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
			"rôle inconnu %q (attendu: api, worker, migrate, reprocess, check-redmine)", c.Role))
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
