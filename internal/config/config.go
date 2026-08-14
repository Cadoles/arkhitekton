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
}

// Role décrit ce que fait le processus. Le binaire est unique, déployé en
// plusieurs exemplaires aux responsabilités distinctes (voir doc 03).
type Role string

const (
	RoleAPI     Role = "api"
	RoleWorker  Role = "worker"
	RoleMigrate Role = "migrate"
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
			KubeconfigPath: os.Getenv("KUBECONFIG"),
			CatalogPath:    envOr("PIPELINE_CATALOG_PATH", "/etc/mse-dashboard/catalog.yaml"),
			LeaseNamespace: envOr("POD_NAMESPACE", "mse-dashboard"),
		},
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var problems []error

	// La base est requise par les trois rôles : `migrate` l'applique, `api` et
	// `worker` la lisent.
	if strings.TrimSpace(c.Database.DSN) == "" {
		problems = append(problems, errors.New("DATABASE_URL est requis"))
	}
	if c.Database.MaxConns < 1 {
		problems = append(problems, fmt.Errorf("DATABASE_MAX_CONNS doit être >= 1, vaut %d", c.Database.MaxConns))
	}

	switch c.Role {
	case RoleMigrate:
		// Rien d'autre : appliquer des migrations ne demande ni stockage objet,
		// ni fournisseur d'identité, ni accès au cluster.
	case RoleAPI:
		problems = append(problems, c.validateHTTP()...)
		problems = append(problems, c.validateOIDC()...)
		problems = append(problems, c.validateStorage()...)
	case RoleWorker:
		problems = append(problems, c.validateStorage()...)
		if strings.TrimSpace(c.Tekton.Namespace) == "" {
			problems = append(problems, errors.New("TEKTON_NAMESPACE est requis"))
		}
		if strings.TrimSpace(c.Tekton.LeaseNamespace) == "" {
			problems = append(problems, errors.New("POD_NAMESPACE est requis (namespace du bail de leader)"))
		}
	default:
		problems = append(problems, fmt.Errorf("rôle inconnu %q (attendu: api, worker, migrate)", c.Role))
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
