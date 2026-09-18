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

// PluginReader rend les réglages des plugins, de l'instance et d'un projet.
type PluginReader interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
}

// Modules dit quels modules sont actifs.
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

// Active rend les identifiants des modules allumés.
//
// Une lecture impossible rend une liste vide : l'interface masque alors les
// modules plutôt que d'afficher des écrans dont chaque requête échouerait.
func (m *Modules) Active(ctx context.Context) []string {
	settings, err := m.store.PluginSettingsAll(ctx)
	if err != nil {
		m.logger.Error("lecture des modules actifs", "error", err)
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

// Enabled dit si un module est allumé.
//
// Un `Modules` nil répond oui : c'est l'état d'un assemblage qui ne branche pas
// la vérification — les tests des handlers —, et il reproduit le comportement
// d'avant les modules.
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

// ActiveIn rend les modules allumés pour un projet.
//
// Le global vaut autorisation : un module éteint sur l'instance l'est pour tous
// les projets. Allumé, un projet en hérite sauf décision contraire — la même règle
// que la liste des plugins d'un projet (doc 16 §16.18).
func (m *Modules) ActiveIn(ctx context.Context, projectID int64) []string {
	settings, err := m.store.PluginSettingsAll(ctx)
	if err != nil {
		m.logger.Error("lecture des modules actifs", "error", err)
		return []string{}
	}
	own, err := m.store.ProjectPluginSettings(ctx, projectID)
	if err != nil {
		m.logger.Error("lecture des modules d'un projet", "projet", projectID, "error", err)
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

// EnabledIn dit si un module est allumé pour un projet. Nil répond oui, comme
// [Modules.Enabled].
func (m *Modules) EnabledIn(ctx context.Context, projectID int64, id string) bool {
	if m == nil {
		return true
	}
	return slices.Contains(m.ActiveIn(ctx, projectID), id)
}

// RequireModule répond 404 quand le module est éteint.
//
// 404 et non 403 : la route n'existe pas sur cette instance, et aucun rôle ne la
// ferait apparaître.
func (m *Middleware) RequireModule(id string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enabled := m.modules.Enabled(r.Context(), id)
			// Une route qui nomme un projet se résout dans ce projet : un module
			// allumé sur l'instance peut être éteint pour lui.
			if enabled && r.PathValue("project") != "" && m.projects != nil {
				projectID, ok := requestProject(w, r, m.projects)
				if !ok {
					return
				}
				enabled = m.modules.EnabledIn(r.Context(), projectID, id)
			}
			if !enabled {
				writeProblem(w, http.StatusNotFound, "Module inactif",
					"le module "+id+" est éteint")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WithModules branche la vérification des modules.
func (m *Middleware) WithModules(modules *Modules) *Middleware {
	m.modules = modules
	return m
}
