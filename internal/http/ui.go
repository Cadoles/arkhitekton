package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/ui"
)

/*
 * The interface: the screens, menus and widgets of the core and of the active plugins, composed
 * by the dashboard and filtered for whoever asks, in the project they look at.
 *
 * One call when the interface starts rather than one per screen: the router must know every path
 * before the first one shows. The project comes as a parameter: the interface follows the current
 * project, and a plugin switched on for the instance may be off for it.
 */

// InterfaceSource is what the interface is composed from.
type InterfaceSource interface {
	// Interface is the composed interface, before filtering.
	Interface() *ui.Interface
	// EventLabels are the labels the active plugins give their facts, by type.
	EventLabels() map[string]string
}

// UIHandler serves the interface.
type UIHandler struct {
	source     InterfaceSource
	projects   projectResolver
	gate       PluginGate
	authorizer *auth.Authorizer
}

// NewUIHandler returns the handler of the interface.
func NewUIHandler(source InterfaceSource, projects projectResolver, gate PluginGate, authorizer *auth.Authorizer) *UIHandler {
	return &UIHandler{source: source, projects: projects, gate: gate, authorizer: authorizer}
}

// Routes mounts the interface.
func (h *UIHandler) Routes(mux *http.ServeMux, middleware *Middleware) {
	mux.Handle("GET /ui", Chain(http.HandlerFunc(h.serve), middleware.Authenticate))
}

type interfaceResponse struct {
	ui.View
	EventLabels map[string]string `json:"eventLabels"`
}

func (h *UIHandler) serve(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	var project *store.Project
	if slug := r.URL.Query().Get("project"); slug != "" {
		found, err := h.projects.ProjectBySlug(r.Context(), slug)
		switch {
		case errors.Is(err, store.ErrProjectNotFound):
			writeProblem(w, http.StatusNotFound, "Unknown project", slug)
			return
		case err != nil:
			writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
			return
		}
		project = &found
	}

	failed := false
	active := map[string]bool{}
	viewer := ui.Viewer{
		Active: func(plugin string) bool {
			if known, asked := active[plugin]; asked {
				return known
			}
			var projectID int64
			if project != nil {
				projectID = project.ID
			}
			settings, err := h.gate.ResolvedPlugin(r.Context(), projectID, plugin)
			if err != nil && !errors.Is(err, store.ErrPluginDisabled) {
				failed = true
			}
			active[plugin] = err == nil && settings.Enabled
			return active[plugin]
		},
		Can: func(permission string) bool {
			if project != nil {
				return h.authorizer.CanIn(identity, project.ID, auth.Permission(permission))
			}
			return h.authorizer.Can(identity, auth.Permission(permission))
		},
	}
	view := h.source.Interface().For(viewer)
	if failed {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins' settings", "")
		return
	}

	body, err := json.Marshal(interfaceResponse{View: view, EventLabels: h.source.EventLabels()})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot write the interface", "")
		return
	}
	sum := sha256.Sum256(body)
	tag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
