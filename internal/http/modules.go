package http

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les modules actifs d'une instance.
 *
 * Un module apporte un domaine entier — le plugin MSE, ses environnements et sa
 * promotion. Éteint, ses routes répondent 404 et l'interface ne les propose pas :
 * une instance sans lui doit rester cohérente (doc 16 §16.8).
 *
 * # Lu à chaque requête
 *
 * L'interrupteur se bascule depuis l'écran d'administration. Le lire au
 * démarrage de l'`api` le ferait mentir jusqu'au prochain déploiement, et
 * l'onglet resterait affiché après qu'on l'a éteint. La lecture porte sur une
 * table de quelques lignes.
 */

// PluginReader returns the plugins' settings, for the instance and for a project.
type PluginReader interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
}

// Modules says which modules are active.
type Modules struct {
	store  PluginReader
	logger *slog.Logger
}

func NewModules(reader PluginReader, logger *slog.Logger) *Modules {
	if logger == nil {
		logger = slog.Default()
	}
	return &Modules{store: reader, logger: logger}
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
	for _, descriptor := range plugin.Registry() {
		if descriptor.Kind == plugin.KindModule && settings[descriptor.ID].Enabled {
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
		m.logger.Error("reading a project's modules", "projet", projectID, "error", err)
		return []string{}
	}
	active := []string{}
	for _, descriptor := range plugin.Registry() {
		if descriptor.Kind != plugin.KindModule || !settings[descriptor.ID].Enabled {
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
				writeProblem(w, http.StatusNotFound, "Module inactif",
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
