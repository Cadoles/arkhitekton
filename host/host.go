// Package host is what an extension compiled into a distribution of arkhitekton imports: the
// contract it implements, [Extension], and what the dashboard lends it.
//
// A plugin that only brings pipelines needs the sdk module and nothing else. An extension also
// brings code the dashboard runs with its own privileges: routes, tables, loops under the
// worker's lease. That code is written against this package, never against the dashboard's
// internals, so that it builds outside this repository.
//
// The data the dashboard shares is aliased from its own types; the services it lends are narrow
// interfaces its implementations satisfy. Both are a contract still young: until arkhitekton 1.0
// a minor version may change them, and says so in its notes.
package host

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/client-go/rest"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/module"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// Extension is what a distribution adds to the dashboard. Every field but ID is optional.
type Extension struct {
	// ID is the plugin the extension's switch is: its settings, its per-project activation.
	ID string
	// Plugin is the extension's descriptor, its pipelines included, registered with the built-in
	// plugins. It declares the `module` capability when the extension brings modules.
	Plugin capability.Plugin
	// Stacks adds Tekton definitions to the reference stacks a project may declare, by stack.
	Stacks map[string][]capability.Manifest
	// Migrations are the extension's tables, applied by the `migrate` role after the core's.
	Migrations *MigrationSet
	Seed       Seed
	// API wires the extension into the `api` role.
	API func(APIHost) (APIHooks, error)
	// Worker wires the extension into the `worker` role.
	Worker func(WorkerHost) (WorkerHooks, error)
}

// Seed is what the configuration gives an extension before the database takes over.
type Seed struct {
	// Settings fills, at every start-up, the extension's settings the database leaves empty. A
	// value an administrator set is never taken back.
	Settings func() map[string]string
	// Projects completes the projects a fresh instance is seeded with, once.
	Projects func(seeded []Project) []Project
	// SchedulerConfig adds configuration values the catalogue references by key.
	SchedulerConfig func() map[string]string
}

// Store is what an extension reads and writes of the dashboard's data. Its own tables it reaches
// through the pool.
type Store interface {
	ProjectBySlug(ctx context.Context, slug string) (Project, error)
	GetProject(ctx context.Context, id int64) (Project, error)
	GetRelease(ctx context.Context, projectID int64, tag string) (Release, error)
	Enqueue(ctx context.Context, request JobRequest) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (PluginSettings, error)
}

// APIHost is what the `api` role lends an extension.
type APIHost struct {
	Store      Store
	Pool       *pgxpool.Pool
	Catalog    *Catalog
	Authorizer *Authorizer
	Modules    *Modules
	Logger     *slog.Logger
}

// APIHooks are what an extension adds to the `api` role.
type APIHooks struct {
	// Routes mount the extension's routes; each sets its own access control.
	Routes []Mount
	// Deployments returns, per release tag, the environments running it.
	Deployments func(ctx context.Context, projectID int64) (map[string][]string, error)
	// Promotions returns a release's requested promotions.
	Promotions func(ctx context.Context, projectID, releaseID int64) ([]ReleasePromotion, error)
	// EnvironmentSuggestions returns the environments offered when a run asks for one.
	EnvironmentSuggestions func(ctx context.Context, projectSlug string) []Suggestion
	// SchemeCheck refuses a version scheme the extension cannot deploy.
	SchemeCheck func(ctx context.Context, scheme Scheme) error
	// Readiness are checks beyond the extension's schema, which the dashboard checks itself.
	Readiness []ReadinessCheck
}

// WorkerHost is what the `worker` role lends an extension.
type WorkerHost struct {
	Store   Store
	Pool    *pgxpool.Pool
	Cluster *rest.Config
	Catalog *Catalog
	Logger  *slog.Logger
}

// WorkerHooks are what an extension adds to the `worker` role.
type WorkerHooks struct {
	// Modules run under the worker's lease while their plugin is switched on for a project.
	Modules []Module
	// PromotionFacts states the fact of a promotion that concluded.
	PromotionFacts PromotionFacts
	// ProjectSettings returns the configuration values a project adds to its pipelines'
	// parameters.
	ProjectSettings func(ctx context.Context, projectID int64) (map[string]string, error)
}

// PromotionFacts states the fact of a concluded promotion.
type PromotionFacts interface {
	Promoted(at time.Time, linked JobReleaseContext) Event
}

// The dashboard's data an extension shares.
type (
	Project            = store.Project
	Target             = store.Target
	Release            = store.Release
	JobRequest         = store.JobRequest
	JobReleaseContext  = store.JobReleaseContext
	PluginSettings     = store.PluginSettings
	MigrationSet       = store.MigrationSet
	Catalog            = catalog.Catalog
	ProjectDeclaration = catalog.ProjectDeclaration
	Environment        = catalog.Environment
	Authorizer         = auth.Authorizer
	Identity           = auth.Identity
	Permission         = auth.Permission
	Role               = auth.Role
	Middleware         = dashhttp.Middleware
	Mount              = dashhttp.Mount
	ProjectResolver    = dashhttp.ProjectResolver
	Modules            = dashhttp.Modules
	ReleasePromotion   = dashhttp.ReleasePromotion
	Suggestion         = dashhttp.Suggestion
	ReadinessCheck     = dashhttp.ReadinessCheck
	Module             = module.Module
	ModuleState        = module.State
	ModuleProject      = module.Project
	Event              = event.Event
	EventType          = event.Type
	Scheme             = version.Scheme
	SemVer             = version.SemVer
)

// The dashboard's errors an extension tells apart with errors.Is.
var (
	ErrProjectNotFound = store.ErrProjectNotFound
	ErrReleaseNotFound = store.ErrReleaseNotFound
	ErrPluginDisabled  = store.ErrPluginDisabled
	ErrSchemaOutdated  = store.ErrSchemaOutdated
)

// Permissions, kinds and states an extension names.
const (
	PermRead          = auth.PermRead
	PermReleaseEdit   = auth.PermReleaseEdit
	KindRetag         = catalog.KindRetag
	ReleaseStateReady = release.StateReady
)

// WriteJSON returns a JSON response, as the rest of the API does.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	dashhttp.WriteJSON(w, status, payload)
}

// WriteProblem returns an error in RFC 9457 form.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	dashhttp.WriteProblem(w, status, title, detail)
}

// RequestProject resolves the project the path names, or writes the response.
func RequestProject(w http.ResponseWriter, r *http.Request, projects ProjectResolver) (int64, bool) {
	return dashhttp.RequestProject(w, r, projects)
}

// IdentityFrom returns the identity authentication put on the request's context.
func IdentityFrom(ctx context.Context) (Identity, bool) { return dashhttp.IdentityFrom(ctx) }

// Chain wraps a handler in middleware, the first given being the outermost.
func Chain(handler http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	return dashhttp.Chain(handler, middleware...)
}

// RedactError removes a DSN's password from an error that quotes it.
func RedactError(err error, dsn string) error { return store.RedactError(err, dsn) }
