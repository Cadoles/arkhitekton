package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type installationStore struct {
	requested []string
	jobs      []store.JobRequest
	activated []int64
	removed   []int64
	audit     []string
	// pending has an installation in progress.
	pending bool
}

func (s *installationStore) PluginInstallations(context.Context) ([]store.PluginInstallation, error) {
	installations := []store.PluginInstallation{{ID: 2, PluginID: "gotest", Version: "1.0.0", State: store.InstallationInstalled, Active: true}}
	if s.pending {
		installations = append(installations, store.PluginInstallation{ID: 5, State: store.InstallationPending})
	}
	return installations, nil
}

func (s *installationStore) PluginLeftovers(context.Context) ([]store.PluginLeftover, error) {
	return []store.PluginLeftover{{PluginID: "deploy", Version: "2.0.1", Table: "goose_deploy_db_version"}}, nil
}

func (s *installationStore) PluginLeftoverOf(_ context.Context, pluginID string) (store.PluginLeftover, error) {
	if pluginID != "deploy" {
		return store.PluginLeftover{}, store.ErrNothingToPurge
	}
	return store.PluginLeftover{PluginID: "deploy", Version: "2.0.1", Table: "goose_deploy_db_version"}, nil
}

func (s *installationStore) RequestPluginInstallation(_ context.Context, reference, digest, _ string) (int64, error) {
	s.requested = append(s.requested, reference+" "+digest)
	return 3, nil
}

func (s *installationStore) FailPluginInstallation(context.Context, int64, string) error { return nil }

func (s *installationStore) ActivatePluginInstallation(_ context.Context, id int64) error {
	s.activated = append(s.activated, id)
	return nil
}

func (s *installationStore) RemovePluginInstallation(_ context.Context, id int64) (string, error) {
	if id == 404 {
		return "", store.ErrInstallationNotFound
	}
	s.removed = append(s.removed, id)
	if id == 2 {
		return "gotest", nil
	}
	return "", nil
}

func (s *installationStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	s.jobs = append(s.jobs, request)
	return 11, nil
}

func (s *installationStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	s.audit = append(s.audit, action+":"+target)
	return nil
}

const pinned = "reg.example/plugins/gotest@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func callInstallations(t *testing.T, method, path, body string) (*httptest.ResponseRecorder, *installationStore, int) {
	t.Helper()
	return callInstallationsWith(t, &installationStore{}, method, path, body)
}

func callInstallationsWith(
	t *testing.T, db *installationStore, method, path, body string,
) (*httptest.ResponseRecorder, *installationStore, int) {
	t.Helper()
	reloads := 0
	handler := NewInstallationsHandler(db, func(_ context.Context, id int64) error {
		if id == 9 {
			return errors.New(`Tekton Pipeline "go-unit-tests" claimed by gotest/unit-tests and go/unit`)
		}
		return nil
	}, func(context.Context) error { reloads++; return nil }, quietLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/plugins/installations", handler.List)
	mux.HandleFunc("POST /admin/plugins/installations", handler.Request)
	mux.HandleFunc("POST /admin/plugins/installations/{id}/activate", handler.Activate)
	mux.HandleFunc("DELETE /admin/plugins/installations/{id}", handler.Remove)
	mux.HandleFunc("GET /admin/plugins/leftovers", handler.Leftovers)
	mux.HandleFunc("POST /admin/plugins/leftovers/{plugin}/purge", handler.Purge)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, asAdmin(httptest.NewRequest(method, path, strings.NewReader(body))))
	return recorder, db, reloads
}

func TestAnInstallationIsRequestedByDigest(t *testing.T) {
	recorder, db, _ := callInstallations(t, http.MethodPost, "/admin/plugins/installations", `{"reference": "`+pinned+`"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	if len(db.requested) != 1 || len(db.jobs) != 1 || db.jobs[0].Kind != PluginInstallJobKind ||
		db.jobs[0].Params[PluginInstallationKey] != "3" {
		t.Errorf("requested %v, jobs %+v", db.requested, db.jobs)
	}

	for _, reference := range []string{"reg.example/plugins/gotest:1.0.0", "reg.example/plugins/gotest", "not a reference"} {
		recorder, db, _ := callInstallations(t, http.MethodPost, "/admin/plugins/installations", `{"reference": "`+reference+`"}`)
		if recorder.Code != http.StatusBadRequest || len(db.jobs) != 0 {
			t.Errorf("%s: status %d, jobs %v", reference, recorder.Code, db.jobs)
		}
	}
}

// A version that would not build with the capabilities in force is refused before it is activated.
func TestAnActivationIsCheckedFirst(t *testing.T) {
	recorder, db, reloads := callInstallations(t, http.MethodPost, "/admin/plugins/installations/9/activate", "")
	if recorder.Code != http.StatusConflict || len(db.activated) != 0 || reloads != 0 ||
		!strings.Contains(recorder.Body.String(), "claimed by") {
		t.Errorf("status %d, activated %v: %s", recorder.Code, db.activated, recorder.Body)
	}
	recorder, db, reloads = callInstallations(t, http.MethodPost, "/admin/plugins/installations/4/activate", "")
	if recorder.Code != http.StatusNoContent || len(db.activated) != 1 || reloads != 1 {
		t.Errorf("status %d, activated %v, reloads %d", recorder.Code, db.activated, reloads)
	}
}

func TestRemovingTheActiveVersionIsAudited(t *testing.T) {
	recorder, db, reloads := callInstallations(t, http.MethodDelete, "/admin/plugins/installations/2", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"uninstalled":"gotest"`) || reloads != 1 {
		t.Errorf("status %d, reloads %d: %s", recorder.Code, reloads, recorder.Body)
	}
	if len(db.audit) != 1 || db.audit[0] != "plugin.uninstalled:2" {
		t.Errorf("audit = %v", db.audit)
	}
	if recorder, _, _ := callInstallations(t, http.MethodDelete, "/admin/plugins/installations/404", ""); recorder.Code != http.StatusNotFound {
		t.Errorf("an unknown installation: %d", recorder.Code)
	}
}

// A purge is queued for what an uninstalled plugin left, and refused for anything else or while an
// installation is in progress.
func TestAPurgeIsQueuedForWhatAPluginLeft(t *testing.T) {
	recorder, _, _ := callInstallations(t, http.MethodGet, "/admin/plugins/leftovers", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"table":"goose_deploy_db_version"`) {
		t.Errorf("leftovers %d: %s", recorder.Code, recorder.Body)
	}

	recorder, db, _ := callInstallations(t, http.MethodPost, "/admin/plugins/leftovers/deploy/purge", "")
	if recorder.Code != http.StatusAccepted || len(db.jobs) != 1 || db.jobs[0].Kind != PluginPurgeJobKind ||
		db.jobs[0].Params[PluginPurgeKey] != "deploy" {
		t.Fatalf("status %d, jobs %+v: %s", recorder.Code, db.jobs, recorder.Body)
	}
	if len(db.audit) != 1 || db.audit[0] != "plugin.purge.requested:deploy" {
		t.Errorf("audit = %v", db.audit)
	}

	if recorder, db, _ := callInstallations(t, http.MethodPost, "/admin/plugins/leftovers/gotest/purge", ""); recorder.Code != http.StatusNotFound || len(db.jobs) != 0 {
		t.Errorf("an installed plugin purged: %d, %v", recorder.Code, db.jobs)
	}
	busy := &installationStore{pending: true}
	if recorder, _, _ := callInstallationsWith(t, busy, http.MethodPost, "/admin/plugins/leftovers/deploy/purge", ""); recorder.Code != http.StatusConflict || len(busy.jobs) != 0 {
		t.Errorf("a purge beside an installation: %d, %v", recorder.Code, busy.jobs)
	}
}
