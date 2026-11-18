package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

type bindingStore struct {
	bindings map[string]json.RawMessage
	audit    []string
	weights  map[string]float64
}

func (s *bindingStore) ScoreWeights(context.Context) (map[string]float64, error) {
	return s.weights, nil
}

func (s *bindingStore) SetScoreWeight(_ context.Context, kind string, weight float64, _ string) error {
	if s.weights == nil {
		s.weights = map[string]float64{}
	}
	s.weights[kind] = weight
	return nil
}

func (s *bindingStore) ClearScoreWeight(_ context.Context, kind string) error {
	delete(s.weights, kind)
	return nil
}

func (s *bindingStore) CapabilityBindings(context.Context) ([]store.CapabilityBinding, error) {
	var rows []store.CapabilityBinding
	for ref, binding := range s.bindings {
		rows = append(rows, store.CapabilityBinding{Ref: ref, Binding: binding})
	}
	return rows, nil
}

func (s *bindingStore) SetCapabilityBinding(_ context.Context, ref string, binding json.RawMessage, _ string) error {
	s.bindings[ref] = binding
	return nil
}

func (s *bindingStore) DeleteCapabilityBinding(_ context.Context, ref string) error {
	delete(s.bindings, ref)
	return nil
}

func (s *bindingStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	s.audit = append(s.audit, action+":"+target)
	return nil
}

// bindingFixture: `go/unit` is free to bind, `php/static` is bound by the catalogue file.
func bindingFixture(t *testing.T) (*CapabilitiesHandler, *bindingStore, *int) {
	t.Helper()
	pipeline := func(name, ref string) capability.Pipeline {
		return capability.Pipeline{Name: name, Label: name, Interpreter: noReading{},
			Spec: capability.PipelineSpec{PipelineRef: ref, Produces: []capability.Product{capability.ProductResults},
				Artifacts:  []capability.Artifact{{Name: "results", Param: "results-upload-url", Object: "r.json", Format: capability.FormatFile}},
				Workspaces: []capability.Workspace{{Name: "git-credentials", Need: capability.NeedSourceRead}},
				Needs:      []capability.Need{{Name: capability.NeedSourceRead, Reason: "clone"}},
				Params:     map[string]capability.Param{"repo-url": {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"}},
				Group:      name}}
	}
	registry, err := capabilities.New(
		capability.Plugin{ID: "go", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{pipeline("unit", "go-unit")}},
		capability.Plugin{ID: "php", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{pipeline("static", "php-static")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	pipelines, err := catalog.Parse([]byte("capabilities:\n  - ref: php/static\n    needs:\n      source-read: { secret: { secretName: git } }\n"),
		catalog.WithCapabilities(registry))
	if err != nil {
		t.Fatal(err)
	}
	bindings := &bindingStore{bindings: map[string]json.RawMessage{}}
	reloads := 0
	handler := NewCapabilitiesHandler(registry, pipelines, bindings,
		func(_ context.Context, binding catalog.Binding) error {
			if binding.Timeout == "forever" {
				return errors.New(`timeout "forever" is not a duration`)
			}
			return nil
		},
		func(context.Context) error { reloads++; return nil }, quietLogger()).WithWeights(bindings)
	return handler, bindings, &reloads
}

func callCapabilities(handler *CapabilitiesHandler, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/capabilities", handler.List)
	mux.HandleFunc("PUT /admin/capabilities/{plugin}/{name}/binding", handler.Bind)
	mux.HandleFunc("DELETE /admin/capabilities/{plugin}/{name}/binding", handler.Unbind)
	mux.HandleFunc("PUT /admin/capabilities/{plugin}/{name}/weight", handler.SetWeight)
	mux.HandleFunc("DELETE /admin/capabilities/{plugin}/{name}/weight", handler.ClearWeight)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, asAdmin(httptest.NewRequest(method, path, strings.NewReader(body))))
	return recorder
}

func TestAnAdministratorBindsWhatTheFileDoesNot(t *testing.T) {
	handler, bindings, reloads := bindingFixture(t)

	recorder := callCapabilities(handler, http.MethodPut, "/admin/capabilities/go/unit/binding",
		`{"timeout": "20m", "needs": {"source-read": {"secret": {"configKey": "pipeline.gitSecret"}}}}`)
	if recorder.Code != http.StatusOK || bindings.bindings["go/unit"] == nil || *reloads != 1 ||
		len(bindings.audit) != 1 || bindings.audit[0] != "capability.binding:go/unit" {
		t.Fatalf("code %d, body %s, stored %v, reloads %d, audit %v", recorder.Code, recorder.Body, bindings.bindings, *reloads, bindings.audit)
	}

	var listed struct {
		Capabilities []capabilityView `json:"capabilities"`
	}
	_ = json.Unmarshal(callCapabilities(handler, http.MethodGet, "/admin/capabilities", "").Body.Bytes(), &listed)
	boundBy := map[string]string{}
	for _, view := range listed.Capabilities {
		boundBy[view.Ref] = view.BoundBy
	}
	if boundBy["go/unit"] != "database" || boundBy["php/static"] != "file" {
		t.Errorf("bound by %v", boundBy)
	}

	if recorder := callCapabilities(handler, http.MethodDelete, "/admin/capabilities/go/unit/binding", ""); recorder.Code != http.StatusNoContent ||
		bindings.bindings["go/unit"] != nil {
		t.Errorf("unbinding: %d", recorder.Code)
	}
}

// What a screen may not grant, what the file binds, and what would not build are refused.
func TestABindingIsRefusedBeforeItIsRecorded(t *testing.T) {
	handler, bindings, _ := bindingFixture(t)
	for label, call := range map[string]struct {
		path, body string
		code       int
	}{
		"service account": {"/admin/capabilities/go/unit/binding", `{"needs": {"cluster-deploy": {"serviceAccountName": "admin"}}}`, http.StatusBadRequest},
		"bound by file":   {"/admin/capabilities/php/static/binding", `{"timeout": "5m"}`, http.StatusConflict},
		"does not build":  {"/admin/capabilities/go/unit/binding", `{"timeout": "forever"}`, http.StatusBadRequest},
		"unknown key":     {"/admin/capabilities/go/unit/binding", `{"timeouts": "5m"}`, http.StatusBadRequest},
		"another ref":     {"/admin/capabilities/go/unit/binding", `{"ref": "php/static"}`, http.StatusBadRequest},
		"unknown":         {"/admin/capabilities/go/lint/binding", `{}`, http.StatusNotFound},
	} {
		if recorder := callCapabilities(handler, http.MethodPut, call.path, call.body); recorder.Code != call.code {
			t.Errorf("%s: code %d, expected %d: %s", label, recorder.Code, call.code, recorder.Body)
		}
	}
	if len(bindings.bindings) != 0 {
		t.Errorf("recorded anyway: %v", bindings.bindings)
	}
}

// noReading stands for a capability's interpreter, which these tests never call.
type noReading struct{ capability.Interpreter }

// A pipeline's weight is set and cleared from the screen, the file's capabilities included; the
// list says what it weighs without one.
func TestAPipelinesWeightIsSetFromTheScreen(t *testing.T) {
	handler, bindings, _ := bindingFixture(t)
	listed := func() map[string]capabilityView {
		t.Helper()
		var body struct {
			Capabilities []capabilityView `json:"capabilities"`
		}
		_ = json.Unmarshal(callCapabilities(handler, http.MethodGet, "/admin/capabilities", "").Body.Bytes(), &body)
		views := map[string]capabilityView{}
		for _, view := range body.Capabilities {
			views[view.Ref] = view
		}
		return views
	}

	if views := listed(); views["go/unit"].DefaultWeight != 25 || views["go/unit"].Weight != nil || views["php/static"].DefaultWeight != 10 {
		t.Fatalf("defaults = %+v", views)
	}
	if recorder := callCapabilities(handler, http.MethodPut, "/admin/capabilities/php/static/weight", `{"weight": 10}`); recorder.Code != http.StatusOK {
		t.Fatalf("a file-bound capability's weight: %d %s", recorder.Code, recorder.Body)
	}
	if weight := listed()["php/static"].Weight; weight == nil || *weight != 10 || bindings.audit[len(bindings.audit)-1] != "score.weight:php/static" {
		t.Errorf("weight = %v, audit = %v", weight, bindings.audit)
	}
	if recorder := callCapabilities(handler, http.MethodPut, "/admin/capabilities/php/static/weight", `{"weight": 120}`); recorder.Code != http.StatusBadRequest {
		t.Errorf("a weight above 100 is refused: %d", recorder.Code)
	}
	if recorder := callCapabilities(handler, http.MethodDelete, "/admin/capabilities/php/static/weight", ""); recorder.Code != http.StatusNoContent || listed()["php/static"].Weight != nil {
		t.Errorf("clearing: %d, weights %v", recorder.Code, bindings.weights)
	}
}
