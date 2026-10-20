package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The modules, and the door: an instance without the MSE plugin stays coherent — its routes
 * answer 404 and `/me` does not announce it. A release's promotion targets are one of those
 * routes.
 */

type pluginReader struct {
	settings map[string]store.PluginSettings
	// projects carries the explicit decisions, per project.
	projects map[int64]map[string]store.PluginSettings
	err      error
}

func (l pluginReader) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return l.settings, l.err
}

func (l pluginReader) ProjectPluginSettings(_ context.Context, projectID int64) (map[string]store.PluginSettings, error) {
	return l.projects[projectID], l.err
}

func modulesWith(mse bool) *Modules {
	return NewModules(testRegistry, pluginReader{settings: map[string]store.PluginSettings{
		moduleID:  {PluginID: moduleID, Enabled: mse},
		"redmine": {PluginID: "redmine", Enabled: true},
	}}, quietLogger())
}

// Only plugins of kind module are announced: a switched-on tracker brings no screen.
func TestOnlySwitchedOnModulesAreAnnounced(t *testing.T) {
	if got := modulesWith(true).Active(context.Background()); !slices.Equal(got, []string{"mse"}) {
		t.Errorf("modules = %v, expected [mse]", got)
	}
	if got := modulesWith(false).Active(context.Background()); len(got) != 0 {
		t.Errorf("modules = %v, expected none", got)
	}
}

// An unreadable read hides the modules rather than showing screens whose every request would
// fail.
func TestAFailedReadHidesTheModules(t *testing.T) {
	modules := NewModules(testRegistry, pluginReader{err: errors.New("database unreachable")}, quietLogger())
	if modules.Enabled(context.Background(), moduleID) {
		t.Error("a module whose state is unreadable is announced switched on")
	}
}

// A route of a switched-off module answers 404, and the handler is not reached.
func TestARouteOfASwitchedOffModuleAnswers404(t *testing.T) {
	middleware := NewMiddleware(nil, nil, quietLogger()).WithModules(modulesWith(false))
	reached := false
	handler := middleware.RequireModule(moduleID)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { reached = true }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/environments", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code %d, expected 404", recorder.Code)
	}
	if reached {
		t.Error("the handler of a switched-off module was reached")
	}
}

func TestARouteOfASwitchedOnModulePasses(t *testing.T) {
	middleware := NewMiddleware(nil, nil, quietLogger()).WithModules(modulesWith(true))
	reached := false
	handler := middleware.RequireModule(moduleID)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { reached = true }))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/environments", nil))
	if !reached {
		t.Error("the handler of a switched-on module was not reached")
	}
}

// `/me` announces the switched-on modules: that is what navigation reads.
func TestMeAnnouncesTheModules(t *testing.T) {
	handler := (&AuthHandler{logger: quietLogger()}).WithModules(modulesWith(true))

	request := httptest.NewRequest(http.MethodGet, "/me", nil)
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "s", Username: "vcarroy", Role: auth.RoleAdmin}))
	recorder := httptest.NewRecorder()
	handler.Me(recorder, request)

	var payload struct {
		Modules []string `json:"modules"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if !slices.Equal(payload.Modules, []string{"mse"}) {
		t.Errorf("modules = %v, expected [mse]", payload.Modules)
	}
}

// A project inherits the module switched on for the instance, may switch it off for itself,
// and cannot switch it back on when the instance has switched it off.
func TestAModuleResolvesWithinTheProject(t *testing.T) {
	reader := func(global bool) *Modules {
		return NewModules(testRegistry, pluginReader{
			settings: map[string]store.PluginSettings{moduleID: {PluginID: moduleID, Enabled: global}},
			projects: map[int64]map[string]store.PluginSettings{
				2: {moduleID: {PluginID: moduleID, Enabled: false}},
				3: {moduleID: {PluginID: moduleID, Enabled: true}},
			},
		}, quietLogger())
	}
	ctx := context.Background()

	for _, testCases := range []struct {
		global   bool
		project  int64
		expected bool
		label    string
	}{
		{true, 1, true, "with no decision, the project inherits"},
		{true, 2, false, "switched off for the project"},
		{false, 3, false, "the switched-off instance wins over the project"},
	} {
		if got := reader(testCases.global).EnabledIn(ctx, testCases.project, moduleID); got != testCases.expected {
			t.Errorf("%s: %v, expected %v", testCases.label, got, testCases.expected)
		}
	}
}

type slugResolver map[string]int64

func (r slugResolver) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	id, found := r[slug]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: id, Slug: slug}, nil
}

// A route naming a project answers 404 when the module is off for it, even when it is on for
// the instance.
func TestAProjectRouteFollowsTheProjectsModule(t *testing.T) {
	modules := NewModules(testRegistry, pluginReader{
		settings: map[string]store.PluginSettings{moduleID: {PluginID: moduleID, Enabled: true}},
		projects: map[int64]map[string]store.PluginSettings{
			2: {moduleID: {PluginID: moduleID, Enabled: false}},
		},
	}, quietLogger())
	middleware := NewMiddleware(nil, nil, quietLogger()).
		WithProjects(slugResolver{"mse": 1, "tekton": 2}).WithModules(modules)

	for slug, expected := range map[string]int{"mse": http.StatusOK, "tekton": http.StatusNotFound} {
		handler := middleware.RequireModule(moduleID)(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		request := httptest.NewRequest(http.MethodGet, "/projects/"+slug+"/releases/1.0.0/targets", nil)
		request.SetPathValue("project", slug)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != expected {
			t.Errorf("project %s: code %d, expected %d", slug, recorder.Code, expected)
		}
	}
}

// moduleID is the module these tests switch: the core knows no module by name.
const moduleID = "mse"
