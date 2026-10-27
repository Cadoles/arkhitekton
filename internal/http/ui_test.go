package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/ui"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

const deployInterface = `
apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: deploy.environments }
spec:
  path: environments
  parent: shell
  blocks: [{ id: deploy.list, component: deploy/environments }]
---
apiVersion: ui.arkhitekton/v1
kind: Navigation
metadata: { name: main }
spec:
  home: /environments
  items:
    - { id: deploy.environments, before: core.versions, label: Environnements, icon: server, to: /environments }
    - { id: deploy.status, label: État, icon: cog, to: /deploy-status, permission: admin }
`

type fakeInterface struct{ composed *ui.Interface }

func (f fakeInterface) Interface() *ui.Interface { return f.composed }

func (fakeInterface) EventLabels() map[string]string {
	return map[string]string{"deploy.release.promoted": "Promotion aboutie"}
}

func uiServer(t *testing.T) *http.ServeMux {
	t.Helper()
	resources, err := ui.Read([]byte(deployInterface))
	if err != nil {
		t.Fatal(err)
	}
	composed, refused := ui.Compose(ui.Core(), []ui.Contribution{{
		Plugin: "deploy", Resources: resources, Routes: []manifest.Route{{Method: "GET", Path: "environments", Permission: "read"}},
	}}, ui.Options{})
	if len(refused) > 0 {
		t.Fatal(refused)
	}
	handler := NewUIHandler(fakeInterface{composed}, slugProjects{}, fakeGate{disabled: map[int64]bool{8: true}},
		auth.NewAuthorizer(nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", handler.serve)
	return mux
}

type served struct {
	Screens []struct {
		Name string `json:"name"`
	} `json:"screens"`
	Navigations map[string]struct {
		Home  string `json:"home"`
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	} `json:"navigations"`
	EventLabels map[string]string `json:"eventLabels"`
}

func readInterface(t *testing.T, recorder *httptest.ResponseRecorder) served {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d %s", recorder.Code, recorder.Body)
	}
	var body served
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func servesScreen(body served, name string) bool {
	for _, screen := range body.Screens {
		if screen.Name == name {
			return true
		}
	}
	return false
}

func TestTheInterfaceFollowsTheProjectAndTheRole(t *testing.T) {
	mux := uiServer(t)

	on := readInterface(t, call(mux, auth.RoleViewer, "GET", "/ui?project=app"))
	main := on.Navigations["main"]
	if main.Home != "/environments" || len(main.Items) != 5 || main.Items[0].ID != "deploy.environments" {
		t.Errorf("main, viewer, switched on = %+v", main)
	}
	if !servesScreen(on, "deploy.environments") || on.EventLabels["deploy.release.promoted"] != "Promotion aboutie" {
		t.Errorf("served = %+v", on)
	}
	if admin := readInterface(t, call(mux, auth.RoleAdmin, "GET", "/ui?project=app")); len(admin.Navigations["main"].Items) != 6 {
		t.Errorf("main, admin = %+v", admin.Navigations["main"])
	}

	off := readInterface(t, call(mux, auth.RoleViewer, "GET", "/ui?project=off"))
	if main := off.Navigations["main"]; main.Home != "" || len(main.Items) != 4 || servesScreen(off, "deploy.environments") {
		t.Errorf("switched off = %+v", off)
	}

	if recorder := call(mux, auth.RoleViewer, "GET", "/ui?project=none"); recorder.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", recorder.Code)
	}
}

func TestTheInterfaceIsNotSentTwice(t *testing.T) {
	mux := uiServer(t)
	first := call(mux, auth.RoleViewer, "GET", "/ui?project=app")
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	request := httptest.NewRequest("GET", "/ui?project=app", nil)
	request.Header.Set("If-None-Match", tag)
	request = request.WithContext(ContextWithIdentity(request.Context(), auth.Identity{Username: "alice", Role: auth.RoleViewer}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotModified {
		t.Errorf("%d", recorder.Code)
	}
}
