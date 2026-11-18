package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
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
	// weights reads and sets each pipeline's weight in the score. Nil: the weights are not served.
	weights ScoreWeightStore
	logger  *slog.Logger
}

// ScoreWeightStore is what setting the pipelines' weights needs.
type ScoreWeightStore interface {
	ScoreWeights(ctx context.Context) (map[string]float64, error)
	configure.ScoreWeightStore
}

// WithWeights serves the pipelines' weights in the score beside their bindings.
func (h *CapabilitiesHandler) WithWeights(weights ScoreWeightStore) *CapabilitiesHandler {
	h.weights = weights
	return h
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
		m.Authenticate, m.Require(auth.PermAdmin), m.CSRF))
	mux.Handle("DELETE /admin/capabilities/{plugin}/{name}/binding", Chain(http.HandlerFunc(h.Unbind),
		m.Authenticate, m.Require(auth.PermAdmin), m.CSRF))
	if h.weights == nil {
		return
	}
	// A weight is not part of the binding: a capability the file binds has its weight set here too.
	mux.Handle("PUT /admin/capabilities/{plugin}/{name}/weight", Chain(http.HandlerFunc(h.SetWeight),
		m.Authenticate, m.Require(auth.PermAdmin), m.CSRF))
	mux.Handle("DELETE /admin/capabilities/{plugin}/{name}/weight", Chain(http.HandlerFunc(h.ClearWeight),
		m.Authenticate, m.Require(auth.PermAdmin), m.CSRF))
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
	// Weight is the weight set for it in the score, absent when none is: it then weighs
	// DefaultWeight, its nature's, zero being out of the score.
	Weight        *float64 `json:"weight,omitempty"`
	DefaultWeight float64  `json:"default_weight"`
	// Group is the nature of its results, which gives the default weight.
	Group string `json:"group,omitempty"`
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

	weights := map[string]float64{}
	if h.weights != nil {
		if weights, err = h.weights.ScoreWeights(r.Context()); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the weights", "")
			return
		}
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
		view.Group = declared.Spec.Group
		if signal, known := results.SignalOf(results.Group(view.Group)); known {
			view.DefaultWeight = results.WeightOf(nil, signal)
		}
		if weight, set := weights[view.Ref]; set {
			view.Weight = &weight
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": views})
}

// Bind records a capability's binding, after checking the capabilities still build with it.
func (h *CapabilitiesHandler) Bind(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	ref, err := service.Ref(r.PathValue("plugin") + "/" + r.PathValue("name"))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable binding", "")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	normalised, err := service.Set(r.Context(), identity.Subject, ref, raw)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref.String(), "binding": json.RawMessage(normalised)})
}

// Unbind removes a recorded binding: the capability can no longer be launched.
func (h *CapabilitiesHandler) Unbind(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	ref, err := service.Ref(r.PathValue("plugin") + "/" + r.PathValue("name"))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := service.Delete(r.Context(), identity.Subject, ref); err != nil {
		writeRefusal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CapabilitiesHandler) service() configure.Bindings {
	return configure.Bindings{
		Store: h.store, Registry: h.registry, Catalog: h.catalog, Check: h.check, Reload: h.reload, Logger: h.logger,
	}
}

// weightRequest carries a pipeline's weight in the score.
type weightRequest struct {
	Weight *float64 `json:"weight"`
}

// SetWeight sets a pipeline's weight in the score; zero leaves it out.
func (h *CapabilitiesHandler) SetWeight(w http.ResponseWriter, r *http.Request) {
	ref, err := h.service().Ref(r.PathValue("plugin") + "/" + r.PathValue("name"))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	var request weightRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil || request.Weight == nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable weight", "expected {\"weight\": 0 to 100}")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	service := configure.ScoreWeights{Store: h.weights, Logger: h.logger}
	if err := service.Set(r.Context(), identity.Subject, ref.String(), *request.Weight); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref.String(), "weight": *request.Weight})
}

// ClearWeight returns a pipeline to its nature's weight.
func (h *CapabilitiesHandler) ClearWeight(w http.ResponseWriter, r *http.Request) {
	ref, err := h.service().Ref(r.PathValue("plugin") + "/" + r.PathValue("name"))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	identity, _ := IdentityFrom(r.Context())
	service := configure.ScoreWeights{Store: h.weights, Logger: h.logger}
	if err := service.Clear(r.Context(), identity.Subject, ref.String()); err != nil {
		writeRefusal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
