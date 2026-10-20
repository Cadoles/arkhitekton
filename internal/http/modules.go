package http

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * An instance's active modules.
 *
 * A module brings a whole domain — the MSE plugin, its environments and its promotion. Switched
 * off, its routes answer 404 and the interface does not offer them: an instance without it must
 * stay coherent.
 *
 * # Read at each request
 *
 * The switch is flipped from the administration screen. Reading it when the `api` starts would
 * make it lie until the next deployment, and the tab would stay displayed after it was switched
 * off. The read bears on a table of a few rows.
 */

// PluginReader returns the plugins' settings, for the instance and for a project.
type PluginReader interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
}

// Modules says which modules are active.
type Modules struct {
	registry *capabilities.Registry
	store    PluginReader
	logger   *slog.Logger
}

func NewModules(registry *capabilities.Registry, reader PluginReader, logger *slog.Logger) *Modules {
	if logger == nil {
		logger = slog.Default()
	}
	return &Modules{registry: registry, store: reader, logger: logger}
}

// Active returns the identifiers of the switched-on modules.
//
// An unreadable read returns an empty list: the interface then hides the modules rather than
// showing screens whose every request would fail.
func (m *Modules) Active(ctx context.Context) []string {
	settings, err := m.store.PluginSettingsAll(ctx)
	if err != nil {
		m.logger.Error("reading the active modules", "error", err)
		return []string{}
	}
	active := []string{}
	for _, descriptor := range m.registry.Plugins() {
		if descriptor.HasCapability(capability.KindModule) && settings[descriptor.ID].Enabled {
			active = append(active, descriptor.ID)
		}
	}
	return active
}

// Enabled says whether a module is switched on.
//
// A nil Modules answers yes: that is the state of an assembly that does not wire the check —
// the handler tests — and it reproduces the behaviour before modules existed.
func (m *Modules) Enabled(ctx context.Context, id string) bool {
	if m == nil {
		return true
	}
	for _, active := range m.Active(ctx) {
		if active == id {
			return true
		}
	}
	return false
}

// ActiveIn returns the modules switched on for a project.
//
// The instance switch is permission: a module switched off there is off for every project.
// Switched on, a project inherits it unless it decides otherwise — the same rule as a
// project's plugin list.
func (m *Modules) ActiveIn(ctx context.Context, projectID int64) []string {
	settings, err := m.store.PluginSettingsAll(ctx)
	if err != nil {
		m.logger.Error("reading the active modules", "error", err)
		return []string{}
	}
	own, err := m.store.ProjectPluginSettings(ctx, projectID)
	if err != nil {
		m.logger.Error("reading a project's modules", "project", projectID, "error", err)
		return []string{}
	}
	active := []string{}
	for _, descriptor := range m.registry.Plugins() {
		if !descriptor.HasCapability(capability.KindModule) || !settings[descriptor.ID].Enabled {
			continue
		}
		if entry, declared := own[descriptor.ID]; declared && descriptor.PerProject && !entry.Enabled {
			continue
		}
		active = append(active, descriptor.ID)
	}
	return active
}

// EnabledIn says whether a module is switched on for a project. Nil answers yes, like
// [Modules.Enabled].
func (m *Modules) EnabledIn(ctx context.Context, projectID int64, id string) bool {
	if m == nil {
		return true
	}
	return slices.Contains(m.ActiveIn(ctx, projectID), id)
}

// RequireModule answers 404 when the module is switched off.
//
// 404 and not 403: the route does not exist on this instance, and no role would make it
// appear.
func (m *Middleware) RequireModule(id string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enabled := m.modules.Enabled(r.Context(), id)
			// A route naming a project resolves within that project: a module switched on for the
			// instance may be off for it.
			if enabled && r.PathValue("project") != "" && m.projects != nil {
				projectID, ok := requestProject(w, r, m.projects)
				if !ok {
					return
				}
				enabled = m.modules.EnabledIn(r.Context(), projectID, id)
			}
			if !enabled {
				writeProblem(w, http.StatusNotFound, "Module disabled",
					"module "+id+" is switched off")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WithModules wires the module check.
func (m *Middleware) WithModules(modules *Modules) *Middleware {
	m.modules = modules
	return m
}
