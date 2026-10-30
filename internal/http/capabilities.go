package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * The bindings an administrator records for the pipeline capabilities the catalogue file does not
 * bind: which secrets satisfy their needs, which configuration keys fill their parameters, how
 * their volumes are sized, how long and how many may run.
 *
 * Never a service account: granting a cluster identity is the operator's act, versioned with the
 * deployment. A capability that needs one is bound by the file.
 */

// BindingStore is what recording bindings needs from the database.
type BindingStore interface {
	CapabilityBindings(ctx context.Context) ([]store.CapabilityBinding, error)
	SetCapabilityBinding(ctx context.Context, ref string, binding json.RawMessage, actor string) error
	DeleteCapabilityBinding(ctx context.Context, ref string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// CapabilitiesHandler lists the pipeline capabilities and records their bindings.
type CapabilitiesHandler struct {
	registry capabilities.Source
	catalog  catalog.Source
	store    BindingStore
	// check builds the capabilities as they would be with this binding, and says why they could
	// not: a binding is refused before it is recorded, not after it broke a reload.
	check func(ctx context.Context, binding catalog.Binding) error
	// reload applies a recorded binding on this replica at once; the others follow the counter.
	reload func(ctx context.Context) error
	logger *slog.Logger
}

func NewCapabilitiesHandler(
	registry capabilities.Source, pipelines catalog.Source, bindings BindingStore,
	check func(ctx context.Context, binding catalog.Binding) error, reload func(ctx context.Context) error,
	logger *slog.Logger,
) *CapabilitiesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &CapabilitiesHandler{registry: registry, catalog: pipelines, store: bindings, check: check, reload: reload, logger: logger}
}

// Routes are administration routes.
func (h *CapabilitiesHandler) Routes(mux *http.ServeMux, m *Middleware) {
	mux.Handle("GET /admin/capabilities", Chain(http.HandlerFunc(h.List), m.Authenticate, m.Require(auth.PermAdmin)))
	mux.Handle("PUT /admin/capabilities/{plugin}/{name}/binding", Chain(http.HandlerFunc(h.Bind),
		m.Authenticate, m.Require(auth.PermAdmin)))
	mux.Handle("DELETE /admin/capabilities/{plugin}/{name}/binding", Chain(http.HandlerFunc(h.Unbind),
		m.Authenticate, m.Require(auth.PermAdmin)))
}

type needView struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

// configMapView is a workspace an administrator fills with a ConfigMap of their own.
type configMapView struct {
	Workspace string `json:"workspace"`
	Reason    string `json:"reason,omitempty"`
}

type capabilityView struct {
	Ref    string     `json:"ref"`
	Label  string     `json:"label"`
	Plugin string     `json:"plugin"`
	Needs  []needView `json:"needs"`
	// Params are the parameters a binding may fill from a configuration key.
	Params []string `json:"params"`
	// Volumes are the per-run claims a binding sizes.
	Volumes []string `json:"volumes"`
	// ConfigMaps are the workspaces a binding fills with data the operator owns. Carried with
	// their reason, like the needs: without it, an administrator sees a field and not what to put
	// in it.
	ConfigMaps []configMapView `json:"config_maps"`
	// BoundBy is `file`, `database` or empty: the file's binding is shown, not edited.
	BoundBy string          `json:"bound_by,omitempty"`
	Binding json.RawMessage `json:"binding,omitempty"`
	// Unavailable is why the catalogue cannot launch it, empty when it can.
	Unavailable string `json:"unavailable,omitempty"`
}

// List returns every pipeline capability that can be launched, and how it is bound.
func (h *CapabilitiesHandler) List(w http.ResponseWriter, r *http.Request) {
	registry, pipelines := capabilities.Of(h.registry), catalog.Of(h.catalog)
	recorded, err := h.store.CapabilityBindings(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the bindings", "")
		return
	}
	stored := map[string]json.RawMessage{}
	for _, binding := range recorded {
		stored[binding.Ref] = binding.Binding
	}

	views := []capabilityView{}
	for _, ref := range registry.Refs() {
		declared, plugin, _ := registry.Pipeline(ref)
		if declared.Spec.PipelineRef == "" {
			// Observed, never launched: nothing to bind.
			continue
		}
		view := capabilityView{Ref: ref.String(), Label: declared.Label, Plugin: plugin.ID, Needs: []needView{},
			Params: []string{}, Volumes: []string{}, ConfigMaps: []configMapView{}}
		for _, need := range declared.Spec.Needs {
			view.Needs = append(view.Needs, needView{Name: string(need.Name), Reason: need.Reason})
		}
		for name, param := range declared.Spec.Params {
			if param.Source == capability.ParamBinding {
				view.Params = append(view.Params, name)
			}
		}
		sort.Strings(view.Params)
		for _, workspace := range declared.Spec.Workspaces {
			if workspace.Volume != nil {
				view.Volumes = append(view.Volumes, workspace.Name)
			}
			if workspace.ConfigMap != nil {
				view.ConfigMaps = append(view.ConfigMaps, configMapView{
					Workspace: workspace.Name, Reason: workspace.ConfigMap.Reason})
			}
		}
		switch {
		case pipelines.BoundByFile(view.Ref):
			view.BoundBy = "file"
		case stored[view.Ref] != nil:
			view.BoundBy, view.Binding = "database", stored[view.Ref]
		}
		if pipeline, found := pipelines.Find(view.Ref); found {
			view.Unavailable = pipeline.Unavailable
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": views})
}

// Bind records a capability's binding, after checking the capabilities still build with it.
func (h *CapabilitiesHandler) Bind(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.ref(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable binding", "")
		return
	}
	binding, err := catalog.ParseBinding(raw)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid binding", err.Error())
		return
	}
	if binding.Ref == "" {
		binding.Ref = ref.String()
	}
	if binding.Ref != ref.String() {
		writeProblem(w, http.StatusBadRequest, "Invalid binding", "the binding names another capability")
		return
	}
	if catalog.Of(h.catalog).BoundByFile(binding.Ref) {
		writeProblem(w, http.StatusConflict, "Bound by the catalogue file", "the file's binding wins: change it there")
		return
	}
	if err := h.check(r.Context(), binding); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid binding", err.Error())
		return
	}
	normalised, err := json.Marshal(binding)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot record the binding", "")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.store.SetCapabilityBinding(r.Context(), binding.Ref, normalised, identity.Subject); err != nil {
		h.logger.Error("recording a binding", "capability", binding.Ref, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot record the binding", "")
		return
	}
	h.audit(r, "capability.binding", binding.Ref)
	h.applyNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ref": binding.Ref, "binding": json.RawMessage(normalised)})
}

// Unbind removes a recorded binding: the capability can no longer be launched.
func (h *CapabilitiesHandler) Unbind(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.ref(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteCapabilityBinding(r.Context(), ref.String()); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot remove the binding", "")
		return
	}
	h.audit(r, "capability.unbinding", ref.String())
	h.applyNow(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *CapabilitiesHandler) ref(w http.ResponseWriter, r *http.Request) (capability.Ref, bool) {
	ref, err := capability.ParseRef(r.PathValue("plugin") + "/" + r.PathValue("name"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Unknown capability", err.Error())
		return capability.Ref{}, false
	}
	if _, _, found := capabilities.Of(h.registry).Pipeline(ref); !found {
		writeProblem(w, http.StatusNotFound, "Unknown capability", ref.String())
		return capability.Ref{}, false
	}
	return ref, true
}

func (h *CapabilitiesHandler) applyNow(ctx context.Context) {
	if h.reload == nil {
		return
	}
	if err := h.reload(ctx); err != nil && !errors.Is(err, context.Canceled) {
		h.logger.Error("applying a binding", "error", err)
	}
}

func (h *CapabilitiesHandler) audit(r *http.Request, action, target string) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, nil); err != nil {
		h.logger.Error("audit log", "action", action, "target", target, "error", err)
	}
}
