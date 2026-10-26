package http

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// fakePrograms is one installed plugin, `deploy`, whose program echoes what it receives.
type fakePrograms struct {
	served *pluginv1.HTTPRequest
	fail   error
}

func (p *fakePrograms) Routes(pluginID string) ([]manifest.Route, bool) {
	if pluginID != "deploy" {
		return nil, false
	}
	return []manifest.Route{
		{Method: "GET", Path: "environments", Permission: "read"},
		{Method: "POST", Path: "releases/{tag}/promote/{environment}", Permission: "deploy:{environment}"},
		{Method: "GET", Path: "files/{rest...}", Permission: "read"},
		{Method: "GET", Path: "status", Permission: "admin", Scope: manifest.ScopeInstance},
	}, true
}

func (p *fakePrograms) Serve(_ context.Context, _ string, request *pluginv1.HTTPRequest) (*pluginv1.HTTPResponse, error) {
	p.served = request
	if p.fail != nil {
		return nil, p.fail
	}
	return &pluginv1.HTTPResponse{Status: 200, Body: []byte(`{"ok":true}`), Headers: map[string]string{
		"content-type": "application/json", "Set-Cookie": "mse_session=stolen",
	}}, nil
}

type fakeGate struct{ disabled map[int64]bool }

func (g fakeGate) ResolvedPlugin(_ context.Context, projectID int64, _ string) (store.PluginSettings, error) {
	if g.disabled[projectID] {
		return store.PluginSettings{}, store.ErrPluginDisabled
	}
	return store.PluginSettings{Enabled: true}, nil
}

type slugProjects struct{}

func (slugProjects) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	switch slug {
	case "app":
		return store.Project{ID: 7, Slug: "app"}, nil
	case "off":
		return store.Project{ID: 8, Slug: "off"}, nil
	}
	return store.Project{}, store.ErrProjectNotFound
}

func pluginRoutesServer(programs *fakePrograms) *http.ServeMux {
	handler := NewPluginRoutesHandler(programs, slugProjects{}, fakeGate{disabled: map[int64]bool{8: true}},
		auth.NewAuthorizer(auth.ProjectPolicies{"app": {"prod": {Sensitive: true}, "staging": {Sensitive: false}}}))
	mux := http.NewServeMux()
	// Without the session middleware: the identity is set as authentication sets it.
	mux.HandleFunc("/projects/{project}/plugins/{plugin}/{path...}", handler.serveProject)
	mux.HandleFunc("/plugins/{plugin}/{path...}", handler.serveInstance)
	return mux
}

func call(mux *http.ServeMux, role auth.Role, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(`{"x":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "mse_session=secret")
	request = request.WithContext(ContextWithIdentity(request.Context(), auth.Identity{Username: "alice", Role: role}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestAPluginRouteReachesTheProgramWithItsCallerAndProject(t *testing.T) {
	programs := &fakePrograms{}
	recorder := call(pluginRoutesServer(programs), auth.RoleViewer, "GET", "/projects/app/plugins/deploy/environments?q=1")
	if recorder.Code != 200 || recorder.Body.String() != `{"ok":true}` {
		t.Fatalf("%d %s", recorder.Code, recorder.Body)
	}
	served := programs.served
	if served.GetPath() != "environments" || served.GetQuery() != "q=1" || served.GetProject().GetSlug() != "app" ||
		served.GetIdentity().GetUsername() != "alice" || served.GetHeaders()["Content-Type"] != "application/json" {
		t.Errorf("served = %v", served)
	}
	if _, leaked := served.GetHeaders()["Cookie"]; leaked {
		t.Error("the session reached the program")
	}
	if recorder.Header().Get("Set-Cookie") != "" || recorder.Header().Get("Content-Type") != "application/json" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", recorder.Header())
	}
}

func TestAPluginRouteIsGuardedByTheDashboard(t *testing.T) {
	for name, attempt := range map[string]struct {
		role         auth.Role
		method, path string
		code         int
	}{
		"an undeclared route":             {auth.RoleAdmin, "GET", "/projects/app/plugins/deploy/secrets", 404},
		"another method":                  {auth.RoleAdmin, "DELETE", "/projects/app/plugins/deploy/environments", 404},
		"an unknown plugin":               {auth.RoleAdmin, "GET", "/projects/app/plugins/other/environments", 404},
		"a project switching it off":      {auth.RoleAdmin, "GET", "/projects/off/plugins/deploy/environments", 404},
		"an unknown project":              {auth.RoleAdmin, "GET", "/projects/none/plugins/deploy/environments", 404},
		"an instance route by a viewer":   {auth.RoleViewer, "GET", "/plugins/deploy/status", 403},
		"a project route on the instance": {auth.RoleAdmin, "GET", "/plugins/deploy/environments", 404},
		"a sensitive environment":         {auth.RoleOperator, "POST", "/projects/app/plugins/deploy/releases/1.0.0/promote/prod", 403},
		"an open environment":             {auth.RoleOperator, "POST", "/projects/app/plugins/deploy/releases/1.0.0/promote/staging", 200},
		"by a releaser":                   {auth.RoleReleaser, "POST", "/projects/app/plugins/deploy/releases/1.0.0/promote/prod", 200},
		"the rest of a path":              {auth.RoleViewer, "GET", "/projects/app/plugins/deploy/files/a/b.txt", 200},
	} {
		programs := &fakePrograms{}
		recorder := call(pluginRoutesServer(programs), attempt.role, attempt.method, attempt.path)
		if recorder.Code != attempt.code {
			t.Errorf("%s: %d, expected %d", name, recorder.Code, attempt.code)
		}
		if attempt.code != 200 && programs.served != nil {
			t.Errorf("%s: the program saw a refused request", name)
		}
	}
}

func TestAProgramsFailureIsNamedWithoutItsMessage(t *testing.T) {
	for failure, code := range map[error]int{
		native.ErrRestarting:                                     http.StatusServiceUnavailable,
		status.Error(codes.DeadlineExceeded, "slow"):             http.StatusGatewayTimeout,
		status.Error(codes.Unavailable, "dial unix /tmp/plugin"): http.StatusBadGateway,
	} {
		recorder := call(pluginRoutesServer(&fakePrograms{fail: failure}), auth.RoleViewer, "GET", "/projects/app/plugins/deploy/environments")
		body, _ := io.ReadAll(recorder.Body)
		if recorder.Code != code || strings.Contains(string(body), "/tmp") {
			t.Errorf("%v: %d %s", failure, recorder.Code, body)
		}
	}
}

func TestATooLargeRequestDoesNotReachTheProgram(t *testing.T) {
	programs := &fakePrograms{}
	request := httptest.NewRequest("GET", "/projects/app/plugins/deploy/environments", strings.NewReader(strings.Repeat("x", maxPluginRequest+1)))
	request = request.WithContext(ContextWithIdentity(request.Context(), auth.Identity{Role: auth.RoleViewer}))
	recorder := httptest.NewRecorder()
	pluginRoutesServer(programs).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || programs.served != nil {
		t.Errorf("%d, served %v", recorder.Code, programs.served != nil)
	}
}

// The plugins' prefixes sit beside the routes that set a plugin's settings, without taking them.
func TestThePluginPrefixesLeaveTheSettingsRoutes(t *testing.T) {
	mux := http.NewServeMux()
	NewPluginRoutesHandler(&fakePrograms{}, slugProjects{}, fakeGate{}, auth.NewAuthorizer(nil)).Routes(mux, &Middleware{})
	mux.Handle("PUT /projects/{project}/plugins/{plugin}", http.NotFoundHandler())
	mux.Handle("GET /projects/{project}/plugins", http.NotFoundHandler())
	_, pattern := mux.Handler(httptest.NewRequest("PUT", "/projects/app/plugins/deploy", nil))
	if pattern != "PUT /projects/{project}/plugins/{plugin}" {
		t.Errorf("the settings route went to %q", pattern)
	}
}
