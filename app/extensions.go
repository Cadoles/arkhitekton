package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The extensions a distribution compiles in, and what the dashboard does with them: their plugins
 * join the built-in ones, their migrations follow the core's, their hooks are composed with the
 * others'.
 */

// extensions are the distribution's, set once by Main.
var extensions []host.Extension

// setExtensions refuses an ambiguous set: an extension is a plugin's switch, and two of one id, or
// one whose descriptor names another plugin, would make the switch control something else.
func setExtensions(given []host.Extension) error {
	seen := map[string]bool{}
	for _, extension := range given {
		if !capability.ValidName(extension.ID) {
			return fmt.Errorf("extension %q: lower case, digits and dashes only", extension.ID)
		}
		if seen[extension.ID] {
			return fmt.Errorf("extension %q given twice", extension.ID)
		}
		seen[extension.ID] = true
		if extension.Plugin.ID != "" && extension.Plugin.ID != extension.ID {
			return fmt.Errorf("extension %q brings plugin %q", extension.ID, extension.Plugin.ID)
		}
	}
	extensions = given
	return nil
}

// The dashboard's store is what it lends extensions.
var _ host.Store = (*store.DB)(nil)

// carriedPlugins are the built-in plugins and the extensions'.
func carriedPlugins() []capability.Plugin {
	plugins := builtin.Plugins()
	for _, extension := range extensions {
		if extension.Plugin.ID != "" {
			plugins = append(plugins, extension.Plugin)
		}
	}
	return plugins
}

// carriedRegistry gathers the carried plugins.
func carriedRegistry() (*capabilities.Registry, error) {
	registry, err := capabilities.New(carriedPlugins()...)
	if err != nil {
		return nil, fmt.Errorf("carried plugins: %w", err)
	}
	return registry, nil
}

// carriedStacks are the reference stacks, completed by the extensions' definitions.
func carriedStacks() map[string][]capability.Manifest {
	stacks := builtin.Stacks()
	for _, extension := range extensions {
		for stack, manifests := range extension.Stacks {
			stacks[stack] = append(stacks[stack], manifests...)
		}
	}
	return stacks
}

// migrateExtensions applies each extension's migrations, after the core's: their tables refer to
// the core's. Applied whether the extension is switched on or not: the switch is flipped without a
// redeployment, and the schema must already be there.
func migrateExtensions(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) error {
	for _, extension := range extensions {
		if extension.Migrations == nil {
			continue
		}
		if err := extension.Migrations.Migrate(ctx, cfg, logger); err != nil {
			return err
		}
	}
	return nil
}

// waitForExtensionSchemas waits until the database carries every extension's migrations.
func waitForExtensionSchemas(ctx context.Context, cfg config.DatabaseConfig, timeout time.Duration, logger *slog.Logger) error {
	for _, extension := range extensions {
		if extension.Migrations == nil {
			continue
		}
		if err := extension.Migrations.WaitForSchema(ctx, cfg, timeout, logger); err != nil {
			return err
		}
	}
	return nil
}

// extensionReadiness checks each extension's schema, then what it asks to be checked.
func extensionReadiness(pool *pgxpool.Pool, hooks []host.APIHooks) []dashhttp.ReadinessCheck {
	var checks []dashhttp.ReadinessCheck
	for _, extension := range extensions {
		if extension.Migrations != nil {
			checks = append(checks, dashhttp.ReadinessCheck{
				Name: "schema-" + extension.ID, Check: extension.Migrations.CheckSchema(pool),
			})
		}
	}
	for _, hook := range hooks {
		checks = append(checks, hook.Readiness...)
	}
	return checks
}

// apiHooks wires every extension into the `api` role.
func apiHooks(lent host.APIHost) ([]host.APIHooks, error) {
	var all []host.APIHooks
	for _, extension := range extensions {
		if extension.API == nil {
			continue
		}
		hooks, err := extension.API(lent)
		if err != nil {
			return nil, fmt.Errorf("extension %q: %w", extension.ID, err)
		}
		all = append(all, hooks)
	}
	return all, nil
}

// workerHooks wires every extension into the `worker` role.
func workerHooks(lent host.WorkerHost) ([]host.WorkerHooks, error) {
	var all []host.WorkerHooks
	for _, extension := range extensions {
		if extension.Worker == nil {
			continue
		}
		hooks, err := extension.Worker(lent)
		if err != nil {
			return nil, fmt.Errorf("extension %q: %w", extension.ID, err)
		}
		all = append(all, hooks)
	}
	return all, nil
}

// composedRoutes are every extension's routes.
func composedRoutes(hooks []host.APIHooks) []dashhttp.Mount {
	var routes []dashhttp.Mount
	for _, hook := range hooks {
		routes = append(routes, hook.Routes...)
	}
	return routes
}

// composedDeployments merges the environments each extension says runs a release. Nil when none
// says anything: the release screen then shows no deployment.
func composedDeployments(hooks []host.APIHooks) func(context.Context, int64) (map[string][]string, error) {
	var sources []func(context.Context, int64) (map[string][]string, error)
	for _, hook := range hooks {
		if hook.Deployments != nil {
			sources = append(sources, hook.Deployments)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return func(ctx context.Context, projectID int64) (map[string][]string, error) {
		merged := map[string][]string{}
		for _, source := range sources {
			deployed, err := source(ctx, projectID)
			if err != nil {
				return nil, err
			}
			for tag, environments := range deployed {
				merged[tag] = append(merged[tag], environments...)
			}
		}
		return merged, nil
	}
}

// composedPromotions lists every extension's promotions of a release.
func composedPromotions(hooks []host.APIHooks) func(context.Context, int64, int64) ([]host.ReleasePromotion, error) {
	var sources []func(context.Context, int64, int64) ([]host.ReleasePromotion, error)
	for _, hook := range hooks {
		if hook.Promotions != nil {
			sources = append(sources, hook.Promotions)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return func(ctx context.Context, projectID, releaseID int64) ([]host.ReleasePromotion, error) {
		var all []host.ReleasePromotion
		for _, source := range sources {
			promotions, err := source(ctx, projectID, releaseID)
			if err != nil {
				return nil, err
			}
			all = append(all, promotions...)
		}
		return all, nil
	}
}

// composedSuggestions lists every extension's environments. Nil when none offers any: the field
// then stays free text.
func composedSuggestions(hooks []host.APIHooks) func(context.Context, string) []host.Suggestion {
	var sources []func(context.Context, string) []host.Suggestion
	for _, hook := range hooks {
		if hook.EnvironmentSuggestions != nil {
			sources = append(sources, hook.EnvironmentSuggestions)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return func(ctx context.Context, projectSlug string) []host.Suggestion {
		var all []host.Suggestion
		for _, source := range sources {
			all = append(all, source(ctx, projectSlug)...)
		}
		return all
	}
}

// composedSchemeCheck refuses a version scheme any extension refuses.
func composedSchemeCheck(hooks []host.APIHooks) func(context.Context, host.Scheme) error {
	var checks []func(context.Context, host.Scheme) error
	for _, hook := range hooks {
		if hook.SchemeCheck != nil {
			checks = append(checks, hook.SchemeCheck)
		}
	}
	if len(checks) == 0 {
		return nil
	}
	return func(ctx context.Context, scheme host.Scheme) error {
		var problems []error
		for _, check := range checks {
			problems = append(problems, check(ctx, scheme))
		}
		return errors.Join(problems...)
	}
}

// composedModules are every extension's modules.
func composedModules(hooks []host.WorkerHooks) []host.Module {
	var modules []host.Module
	for _, hook := range hooks {
		modules = append(modules, hook.Modules...)
	}
	return modules
}

// composedPromotionFacts is the one extension's statement of a concluded promotion. Two would
// state the same promotion twice, under two names: refused.
func composedPromotionFacts(hooks []host.WorkerHooks) (host.PromotionFacts, error) {
	var found host.PromotionFacts
	for _, hook := range hooks {
		if hook.PromotionFacts == nil {
			continue
		}
		if found != nil {
			return nil, errors.New("two extensions state the facts of a promotion")
		}
		found = hook.PromotionFacts
	}
	return found, nil
}

// composedProjectSettings merges the values every extension adds to a project's pipelines.
func composedProjectSettings(hooks []host.WorkerHooks) func(context.Context, int64) (map[string]string, error) {
	var sources []func(context.Context, int64) (map[string]string, error)
	for _, hook := range hooks {
		if hook.ProjectSettings != nil {
			sources = append(sources, hook.ProjectSettings)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		merged := map[string]string{}
		for _, source := range sources {
			values, err := source(ctx, projectID)
			if err != nil {
				return nil, err
			}
			maps.Copy(merged, values)
		}
		return merged, nil
	}
}

// extensionSchedulerConfig adds the keys the extensions supply to the scheduler's configuration.
func extensionSchedulerConfig(base map[string]string) map[string]string {
	for _, extension := range extensions {
		if extension.Seed.SchedulerConfig != nil {
			maps.Copy(base, extension.Seed.SchedulerConfig())
		}
	}
	return base
}

// extensionProjects lets each extension complete the projects a fresh instance is seeded with.
func extensionProjects(seeded []store.Project) []store.Project {
	for _, extension := range extensions {
		if extension.Seed.Projects != nil {
			seeded = extension.Seed.Projects(seeded)
		}
	}
	return seeded
}

// seedExtensionSettings fills the settings the configuration supplies to each extension.
//
// Fills in only what is missing: the database is authoritative from the first value written, and
// a setting changed from the administration screen must not be taken back at the next restart.
// Logged and not fatal: the dashboard starts, and the administration screen allows setting these
// values by hand.
func seedExtensionSettings(ctx context.Context, db *store.DB, logger *slog.Logger) {
	for _, extension := range extensions {
		if extension.Seed.Settings == nil {
			continue
		}
		if err := seedSettings(ctx, db, extension.ID, extension.Seed.Settings()); err != nil {
			logger.Error("seeding an extension's settings", "extension", extension.ID, "error", err)
		}
	}
}

func seedSettings(ctx context.Context, db *store.DB, pluginID string, seeds map[string]string) error {
	settings, err := db.PluginSettingsAll(ctx)
	if err != nil {
		return err
	}
	entry, declared := settings[pluginID]
	if !declared {
		// The row does not exist yet: writing it here would set it switched off, whereas it is
		// `EnsurePlugins` that decides its initial state.
		return nil
	}

	config := make(map[string]string, len(entry.Config))
	maps.Copy(config, entry.Config)
	var posed []string
	for field, value := range seeds {
		if strings.TrimSpace(config[field]) != "" || strings.TrimSpace(value) == "" {
			continue
		}
		config[field] = value
		posed = append(posed, field)
	}
	if len(posed) == 0 {
		return nil
	}
	sort.Strings(posed)

	entry.PluginID = pluginID
	entry.Config = config
	return db.SetPluginSettings(ctx, entry, "seeding")
}
