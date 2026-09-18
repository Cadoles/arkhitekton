// Package config loads and validates the configuration. Every variable is
// documented in docs/04-configuration.md.
//
// All validation happens at start-up, before a single port is opened. An instance
// that starts on an incomplete configuration discovers the problem at a user's
// first click, often on the rarest operation; failing at start-up turns a diffuse
// functional outage into a deployment error, seen at once by the right person.
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
)

// Config holds the configuration of the binary whatever its role. Each role
// validates only what it uses.
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
	Results  ResultsConfig
	Secrets  SecretsConfig
	Webhooks WebhookConfig
	Probes   ProbeConfig
}

// ProbeConfig bounds how often outside sources are polled.
//
// The load is projects × probes × frequency, and the multiplicand is someone
// else's machine. A floor is applied when the loop is built: declaring ten seconds
// does not obtain them.
type ProbeConfig struct {
	Interval time.Duration
}

// WebhookConfig bounds what a configurable outgoing call may reach.
//
// Both permissions default to false. A URL typed in the interface becomes a
// request issued from inside the cluster by a process holding a service account
// token: opening that by default would hand out a server-side request forgery.
type WebhookConfig struct {
	// AllowPrivate allows private and loopback targets. A local deployment needs
	// it, a production one does not. It never reopens link-local: instance
	// metadata is not a target any deployment wants to reach.
	AllowPrivate bool
	// AllowInsecure allows `http://`. The payload carries a release tag and the
	// token that authenticates it; in the clear, all of it crosses a readable
	// network.
	AllowInsecure bool
	// Timeout bounds one call. A slow recipient must not hold up the plan engine:
	// the steps of a stage are issued together, and a hung call would block the
	// stage from concluding.
	Timeout time.Duration
}

// SecretsConfig carries the key that seals the secret values of plugin settings.
//
// It lives in the environment and not in the database — that is the whole point: a
// database backup says nothing without it. The corollary is that it must be backed
// up elsewhere, or a restore yields settings nothing can open.
//
// Its absence does not prevent start-up. It makes saving a secret field
// impossible, which the interface says where the field is typed; refusing to start
// would punish a deployment that has nothing to protect yet, and the message would
// be read far from whoever can act on it.
type SecretsConfig struct {
	// Key is the current key, 32 bytes base64: `openssl rand -base64 32`. It must
	// appear in no log and in no error message.
	Key string
	// RetiredKeys carries replaced keys, which only open what already exists.
	//
	// Rotation is two-staged — the new key seals, the old one opens — because
	// values already stored are re-sealed only as they are rewritten. Retiring the
	// old key too early makes them unreadable.
	RetiredKeys []string
}

// Configured says whether sealing is possible.
func (c SecretsConfig) Configured() bool { return strings.TrimSpace(c.Key) != "" }

// ResultsConfig carries how results left by pipelines are read.
type ResultsConfig struct {
	// MarkerToken prefixes the protocol lines a Task writes on stdout: `MSE` gives
	// `##MSE-META##`, empty gives `##META##`. See docs/05-pipeline-catalogue.md.
	//
	// Configurable because a deployment's name inside a protocol is a different
	// kind of coupling than inside an interface: a generic pipeline emitting
	// `##MSE-META##` stays MSE-branded in its own source.
	MarkerToken string
}

// ForgeConfig carries read access to the forge.
//
// Read-only, enforced by the transport in internal/gitea as it is for the tracker.
// It feeds the release form: offering the repository's branches and tags rather
// than a free-text field where a typo only surfaces when the build fails.
//
// Writing to the forge stays a separate client and an explicit decision, not a
// loosening of this one.
type ForgeConfig struct {
	URL string
	// Token must appear in no log, no error message and no URL. It opens the whole
	// forge in its holder's name: a `read:repository` token is enough, and better.
	Token string
	// Repository au format `owner/nom` — `CNOUS/mse`.
	Repository string
	Timeout    time.Duration
}

// Configured says whether access is set. Its absence is a normal state: the form
// falls back to free-text input and says so.
func (c ForgeConfig) Configured() bool {
	return c.URL != "" && c.Token != "" && c.Repository != ""
}

// TrackerConfig carries read access to issues and versions.
//
// Read-only, and the instance it targets is production. Creating a version,
// closing an issue, posting a note stay human gestures made in the tracker.
//
// The prohibition is not left to callers' discipline: the transport in
// internal/redmine refuses any method other than GET and HEAD before reaching the
// network. A production key circulates here, so the guarantee had to be
// structural.
type TrackerConfig struct {
	URL string
	// APIKey must appear in no log, no error message and no URL. It travels in the
	// `X-Redmine-API-Key` header, never in a query string, where it would end up in
	// access logs.
	APIKey string
	// Project is the identifier as it appears in the tracker's URL.
	Project string
	// Timeout bounds one call: a slow tracker must not become a slow dashboard.
	Timeout time.Duration
	// ReviewStatuses lists the statuses that make an issue eligible for a release
	// note. The vocabulary belongs to the deployment, which can rename them without
	// any code changing.
	ReviewStatuses []string
}

// Configured says whether access is set. Its absence is a normal state, not an
// error: the dashboard runs without a tracker, and the screens that depend on one
// show an explicit degraded state.
func (c TrackerConfig) Configured() bool {
	return c.URL != "" && c.APIKey != ""
}

// RegistryConfig carries read access to the image registry.
//
// Read-only: the dashboard never pushes an image. Promotion re-tags through a Task
// carrying its own credentials — granting write access here would give the
// dashboard a power it does not exercise.
type RegistryConfig struct {
	URL      string
	Username string
	// Password must appear in no log and in no error message.
	Password string
	// Project is the repository prefix.
	Project string
	// Image is the repository that promotion re-tags, configurable because the
	// application's name is not a constant of the dashboard.
	//
	// The second image a release publishes is not here: it is not promoted, it
	// follows. Its name lives in internal/images, because it must stay the one the
	// build publishes and the GitOps repository deploys.
	Image string
}

// FluxConfig bounds how Flux objects are read.
type FluxConfig struct {
	// Namespace where ImagePolicy and Kustomization objects live, distinct from the
	// pipelines' namespace.
	Namespace string
}

// Role says what this process does. One binary, deployed several times with
// distinct responsibilities — see docs/02-architecture.md.
type Role string

const (
	RoleAPI     Role = "api"
	RoleWorker  Role = "worker"
	RoleMigrate Role = "migrate"
	// RoleReprocess replays results from raw artifacts. A one-off role, run by hand
	// after a parser is fixed, which is why it has no Deployment.
	RoleReprocess Role = "reprocess"
	// RoleCheckTracker verifies tracker access and exits. It exists because the key
	// it targets is a production one: without it, the only way to know whether a
	// key works is to wait for a screen to use it.
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
	// DSN carries the password: it must appear in no log and in no error message.
	// See Redacted().
	DSN             string
	MaxConns        int
	ConnMaxLifetime time.Duration
}

type StorageConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	// Region is not decorative: a wrong region invalidates the SigV4 signature,
	// with a server message that does not point at it.
	Region string
	// PresignTTL must outlast the longest pipeline without granting a write
	// capability for longer than is reasonable.
	PresignTTL time.Duration
	UseSSL     bool
}

type OIDCConfig struct {
	// IssuerURL must match the token's `iss` claim exactly, trailing slash
	// included. Omitting it fails verification with an unhelpful message.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// BootstrapAdmins lists the identifiers (`sub` or username) promoted to
	// administrator at start-up. Without it nobody can grant the first role and the
	// interface is unusable after deployment.
	BootstrapAdmins []string
	// BootstrapUsers declares one role per account — `alice:releaser` — where
	// BootstrapAdmins can only say "administrator". Both are read once and never
	// again: the database is authoritative afterwards.
	BootstrapUsers map[string]string
	SessionTTL     time.Duration
}

type TektonConfig struct {
	Namespace      string
	KubeconfigPath string
	CatalogPath    string
	// LeaseNamespace holds the leader election Lease. Distinct from Namespace: the
	// lease lives in the dashboard's own namespace, where the service account may
	// write it, and not in the pipelines' namespace, where it may only read.
	LeaseNamespace string
	// DashboardURL is the root of a Tekton Dashboard, if one is deployed. Empty
	// simply hides the link; requiring it would force deploying a component this
	// dashboard does not need.
	DashboardURL string
}

// Load reads the configuration from the environment and validates it for the
// given role. The error aggregates every problem found: fixing a configuration at
// one restart per missing variable is avoidable waste.
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
			Bucket:     envOr("MINIO_BUCKET", "arkhitekton"),
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
			CatalogPath:    envOr("PIPELINE_CATALOG_PATH", "/etc/arkhitekton/catalog.yaml"),
			LeaseNamespace: envOr("POD_NAMESPACE", "arkhitekton"),
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
		Tracker: TrackerConfig{
			// The statuses that make an issue eligible for a release note.
			// Configurable because the vocabulary belongs to the deployment, which
			// can rename them without any code changing.
			ReviewStatuses: listOr("TRACKER_REVIEW_STATUSES", []string{"En revue"}),
			URL:            strings.TrimRight(os.Getenv("TRACKER_URL"), "/"),
			APIKey:         os.Getenv("TRACKER_API_KEY"),
			Project:        os.Getenv("TRACKER_PROJECT"),
			Timeout:        durationOr("TRACKER_TIMEOUT", 15*time.Second),
		},
		Probes: ProbeConfig{
			Interval: durationOr("PROBE_INTERVAL", 5*time.Minute),
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

// renamedVariables maps each retired environment variable to its replacement.
//
// The configuration used to name products — GITEA_TOKEN, REDMINE_API_KEY. It now
// names boundaries: a forge, a tracker.
//
// A retired name is refused loudly rather than absorbed. A deployment left on the
// old name would end up with an "unconfigured" forge — a valid state, handled by
// falling back to free-text input — and nobody would connect that to the rename.
// The tracker would simply stop synchronising. Two silent degradations that
// start-up must prevent.
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

// removedVariables maps each deleted variable to why it went. Distinct from the
// renamed ones: there is no replacement to point at, only a mechanism that no
// longer exists. Leaving it in place would let a deployment believe it configures
// something nothing reads.
var removedVariables = map[string]string{
	"PIPELINE_GIT_PUBLIC_KEY": "la clé de déploiement partagée n'existe plus: " +
		"chaque projet a la sienne, posée par le dashboard et activée depuis " +
		"l'écran du projet",
}

// checkRenamed refuses to start on a retired variable, whatever the role: a
// leftover name is a configuration mistake regardless of what the process is about
// to do.
func checkRenamed() []error {
	// Sorted, so two start-ups on the same configuration produce the same message.
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

	// Every service role needs the database. check-tracker is exempt, and that is
	// its point: verifying a key must be possible from a machine with access to
	// neither the database nor the cluster.
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
		// Nothing else: applying migrations needs no object store, no identity
		// provider and no cluster access.
	case RoleAPI:
		problems = append(problems, c.validateHTTP()...)
		problems = append(problems, c.validateOIDC()...)
		problems = append(problems, c.validateStorage()...)
	case RoleReprocess:
		// The object store is enough: replaying reads raw artifacts and writes to
		// the database, without touching the cluster or creating any run.
		problems = append(problems, c.validateStorage()...)
	case RoleCheckTracker:
		// No database, no store, no cluster: this role only performs an HTTP read.
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

	// Without a bootstrap administrator, authentication works but no user can do
	// anything and nobody can unblock it from the interface. The symptom —
	// "everyone is refused" — does not point at its cause.
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
		// The longest pipeline runs for well over an hour. A URL expiring before the
		// run ends fails the upload at the last moment, after the expensive work is
		// done.
		problems = append(problems, fmt.Errorf(
			"MINIO_PRESIGN_TTL vaut %s, insuffisant: le pipeline le plus long dure 90m",
			c.Storage.PresignTTL))
	}
	return problems
}

// Redacted returns a copy safe to log: secrets are replaced by a marker, without
// hiding whether they are set.
func (c Config) Redacted() Config {
	out := c
	out.Database.DSN = redactDSN(c.Database.DSN)
	out.Storage.SecretKey = redact(c.Storage.SecretKey)
	out.OIDC.ClientSecret = redact(c.OIDC.ClientSecret)
	out.Registry.Password = redact(c.Registry.Password)
	out.Tracker.APIKey = redact(c.Tracker.APIKey)
	out.Forge.Token = redact(c.Forge.Token)
	out.Secrets.Key = redact(c.Secrets.Key)
	// Retired keys are masked one by one rather than replaced by an empty list:
	// how many there are says where a rotation stands, which is what a start-up log
	// is read for.
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

// redactDSN keeps the shape of the URL — host, database, options — and removes
// only the password. A fully masked DSN cannot be used to diagnose a connection
// error, which is precisely when it gets logged.
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

// listOr splits a comma-separated value, ignoring empty entries: "a,,b" and
// "a, b" give the same result.
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

// bootstrapRoles bounds what a bootstrap may grant. It mirrors the schema
// constraint: a value outside the list would fail the insert mid-bootstrap, with a
// message naming a constraint rather than a mistyped variable.
var bootstrapRoles = map[string]bool{
	"viewer": true, "operator": true, "releaser": true, "admin": true,
}

// parseGrants reads `account:role,other:role`.
//
// An entry with an unrecognised role is dropped rather than refused at start-up:
// blocking start-up over a stray comma would also block fixing it from the
// interface. Pending grants are logged when the api starts, where their absence
// shows.
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
