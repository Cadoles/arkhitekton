package http

import (
	"context"
	"errors"
	"fmt"
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
	// parked is the installation waiting for a decision on its source, if any.
	parked   *store.PluginInstallation
	accepted []string
	retried  []int64
	// sources are the installed plugins' sources; pins and checks record what was asked.
	sources map[string]store.PluginSource
	pins    []string
	checks  []string
	request []store.InstallationRequest
}

func (s *installationStore) PluginSources(context.Context) ([]store.PluginSource, error) {
	var all []store.PluginSource
	for _, source := range s.sources {
		all = append(all, source)
	}
	return all, nil
}

func (s *installationStore) PluginSource(_ context.Context, pluginID string) (store.PluginSource, error) {
	source, found := s.sources[pluginID]
	if !found {
		return store.PluginSource{}, store.ErrSourceNotFound
	}
	return source, nil
}

func (s *installationStore) SetSourcePinned(_ context.Context, pluginID string, pinned bool) error {
	s.pins = append(s.pins, fmt.Sprintf("%s %v", pluginID, pinned))
	return nil
}

func (s *installationStore) RequestUpdateCheck(_ context.Context, pluginID string) error {
	s.checks = append(s.checks, pluginID)
	return nil
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

func (s *installationStore) RequestPluginInstallation(_ context.Context, request store.InstallationRequest) (int64, error) {
	s.requested = append(s.requested, request.Reference+" "+request.Digest)
	s.request = append(s.request, request)
	return 3, nil
}

func (s *installationStore) FailPluginInstallation(context.Context, int64, string) error { return nil }

func (s *installationStore) PluginInstallation(_ context.Context, id int64) (store.PluginInstallation, error) {
	if s.parked == nil || s.parked.ID != id {
		return store.PluginInstallation{}, store.ErrInstallationNotFound
	}
	return *s.parked, nil
}

func (s *installationStore) AcceptSource(_ context.Context, pluginID, reason, subject string) error {
	s.accepted = append(s.accepted, pluginID+" "+reason+" "+subject)
	return nil
}

func (s *installationStore) RetryPluginInstallation(_ context.Context, id int64) error {
	s.retried = append(s.retried, id)
	return nil
}

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
	mux.HandleFunc("POST /admin/plugins/installations/{id}/accept", handler.Accept)
	mux.HandleFunc("DELETE /admin/plugins/installations/{id}", handler.Remove)
	mux.HandleFunc("GET /admin/plugins/leftovers", handler.Leftovers)
	mux.HandleFunc("POST /admin/plugins/leftovers/{plugin}/purge", handler.Purge)
	mux.HandleFunc("POST /admin/plugins/{plugin}/update", handler.Update)
	mux.HandleFunc("POST /admin/plugins/{plugin}/check", handler.CheckUpdates)
	mux.HandleFunc("PUT /admin/plugins/{plugin}/source", handler.SetSource)
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

	if request := db.request[0]; request.Follows || request.Digest == "" {
		t.Errorf("a digest pins: %+v", request)
	}

	recorder, _, _ = callInstallations(t, http.MethodPost, "/admin/plugins/installations", `{"reference": "not a reference"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status %d", recorder.Code)
	}
}

// A repository alone installs what it publishes and the plugin follows it; a tag pins. Neither has a
// digest yet: the worker resolves it, the api has no access to the registries.
func TestAnInstallationIsRequestedByRepositoryOrTag(t *testing.T) {
	for reference, follows := range map[string]bool{"reg.example/plugins/gotest": true, "reg.example/plugins/gotest:1.0.0": false} {
		recorder, db, _ := callInstallations(t, http.MethodPost, "/admin/plugins/installations", `{"reference": "`+reference+`"}`)
		if recorder.Code != http.StatusAccepted || len(db.request) != 1 {
			t.Fatalf("%s: status %d: %s", reference, recorder.Code, recorder.Body)
		}
		if request := db.request[0]; request.Follows != follows || request.Digest != "" || request.Reference != reference {
			t.Errorf("%s: %+v", reference, request)
		}
	}
}

func installedSource(pinned bool) *installationStore {
	return &installationStore{sources: map[string]store.PluginSource{"gotest": {
		PluginID: "gotest", Repository: "reg.example/plugins/gotest", Pinned: pinned, ActiveVersion: "1.0.0", ActiveID: 2,
		LatestVersion: "2.0.0", LatestDigest: "sha256:new",
	}}}
}

// Another version of a plugin installed from the same repository asks how: beside it, or in its
// place.
func TestAnotherVersionOfAnInstalledPluginAsksHow(t *testing.T) {
	recorder, db, _ := callInstallationsWith(t, installedSource(true), http.MethodPost, "/admin/plugins/installations", `{"reference": "`+pinned+`"}`)
	if recorder.Code != http.StatusConflict || len(db.jobs) != 0 {
		t.Fatalf("status %d, jobs %v", recorder.Code, db.jobs)
	}

	_, db, _ = callInstallationsWith(t, installedSource(true), http.MethodPost, "/admin/plugins/installations",
		`{"reference": "`+pinned+`", "mode": "update"}`)
	if len(db.request) != 1 || db.request[0].Replaces == nil || *db.request[0].Replaces != 2 || db.request[0].Follows {
		t.Errorf("update: %+v", db.request)
	}
	_, db, _ = callInstallationsWith(t, installedSource(true), http.MethodPost, "/admin/plugins/installations",
		`{"reference": "`+pinned+`", "mode": "install"}`)
	if len(db.request) != 1 || db.request[0].Replaces != nil {
		t.Errorf("beside: %+v", db.request)
	}
}

// A following plugin is updated to the version found, in place of the active one; a pinned one is
// not.
func TestAFollowingPluginIsUpdatedAndAPinnedOneIsNot(t *testing.T) {
	recorder, db, _ := callInstallationsWith(t, installedSource(false), http.MethodPost, "/admin/plugins/gotest/update", ``)
	if recorder.Code != http.StatusAccepted || len(db.request) != 1 || len(db.jobs) != 1 {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	request := db.request[0]
	if request.Reference != "reg.example/plugins/gotest@sha256:new" || request.ExpectedVersion != "2.0.0" ||
		!request.Follows || request.Replaces == nil || *request.Replaces != 2 {
		t.Errorf("request = %+v", request)
	}

	recorder, db, _ = callInstallationsWith(t, installedSource(true), http.MethodPost, "/admin/plugins/gotest/update", ``)
	if recorder.Code != http.StatusConflict || len(db.jobs) != 0 {
		t.Errorf("pinned: status %d", recorder.Code)
	}
	recorder, _, _ = callInstallationsWith(t, installedSource(false), http.MethodPost, "/admin/plugins/other/update", ``)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("unknown plugin: status %d", recorder.Code)
	}
}

// The list says which plugin has a newer version, and whether it crosses a major version.
func TestTheListSaysWhatCanBeUpdated(t *testing.T) {
	recorder, _, _ := callInstallationsWith(t, installedSource(false), http.MethodGet, "/admin/plugins/installations", ``)
	body := recorder.Body.String()
	for _, expected := range []string{`"update_available":true`, `"major":true`, `"latest_version":"2.0.0"`, `"pinned":false`} {
		if !strings.Contains(body, expected) {
			t.Errorf("%s not in %s", expected, body)
		}
	}
}

func TestAPluginIsPinnedOrFollowedAndCheckedOnRequest(t *testing.T) {
	recorder, db, _ := callInstallationsWith(t, installedSource(false), http.MethodPut, "/admin/plugins/gotest/source", `{"pinned": true}`)
	if recorder.Code != http.StatusNoContent || len(db.pins) != 1 || db.pins[0] != "gotest true" {
		t.Errorf("status %d, pins %v", recorder.Code, db.pins)
	}
	recorder, db, _ = callInstallationsWith(t, installedSource(false), http.MethodPost, "/admin/plugins/gotest/check", ``)
	if recorder.Code != http.StatusAccepted || len(db.checks) != 1 {
		t.Errorf("status %d, checks %v", recorder.Code, db.checks)
	}
	recorder, _, _ = callInstallationsWith(t, installedSource(false), http.MethodPut, "/admin/plugins/gotest/source", `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status %d", recorder.Code)
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

// Accepting a source records it against the plugin's id, not against a key: the signature format
// carries no public key, so an unknown signer leaves nothing else to remember. The next version of
// that plugin then installs without asking again.
func TestAcceptingASourceRecordsItAndQueuesTheInstallation(t *testing.T) {
	db := &installationStore{parked: &store.PluginInstallation{
		ID: 4, State: store.InstallationUnrecognised, PluginID: "gotest",
		Reference: pinned, Error: "signed by no trusted key",
	}}
	recorder, _, _ := callInstallationsWith(t, db, http.MethodPost, "/admin/plugins/installations/4/accept", "")

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if len(db.accepted) != 1 || db.accepted[0] != "gotest signed by no trusted key 121" {
		t.Errorf("accepted = %v", db.accepted)
	}
	if len(db.retried) != 1 || db.retried[0] != 4 {
		t.Errorf("retried = %v", db.retried)
	}
	if len(db.jobs) != 1 || db.jobs[0].Kind != PluginInstallJobKind || db.jobs[0].Params[PluginInstallationKey] != "4" {
		t.Errorf("jobs = %+v", db.jobs)
	}
	if len(db.audit) != 1 || db.audit[0] != "plugin.source.accepted:"+pinned {
		t.Errorf("audit = %v", db.audit)
	}
}

// Only an installation waiting on that decision can be accepted: one that failed for another
// reason is not turned into an acceptance by this route, and an unknown one is not found.
func TestOnlyAnInstallationWaitingOnItsSourceIsAccepted(t *testing.T) {
	failed := &installationStore{parked: &store.PluginInstallation{
		ID: 4, State: store.InstallationFailed, PluginID: "gotest", Reference: pinned,
	}}
	recorder, _, _ := callInstallationsWith(t, failed, http.MethodPost, "/admin/plugins/installations/4/accept", "")
	if recorder.Code != http.StatusConflict {
		t.Errorf("a failed installation: status = %d", recorder.Code)
	}
	if len(failed.accepted) != 0 || len(failed.jobs) != 0 {
		t.Errorf("accepted = %v, jobs = %+v", failed.accepted, failed.jobs)
	}

	recorder, unknown, _ := callInstallations(t, http.MethodPost, "/admin/plugins/installations/7/accept", "")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("an unknown installation: status = %d", recorder.Code)
	}
	if len(unknown.accepted) != 0 {
		t.Errorf("accepted = %v", unknown.accepted)
	}
}
