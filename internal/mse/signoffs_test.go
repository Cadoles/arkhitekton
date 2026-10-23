package mse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/host/hosttest"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

type fakeSignoffStore struct {
	release      host.Release
	releaseErr   error
	environments []storage.Environment
	signoffs     []storage.Signoff
	set          []string
	deleted      []string
	deleteErr    error
	audit        []string
	unknownEnv   bool
}

func (f *fakeSignoffStore) GetRelease(context.Context, int64, string) (host.Release, error) {
	if f.releaseErr != nil {
		return host.Release{}, f.releaseErr
	}
	return f.release, nil
}

func (f *fakeSignoffStore) ListSignoffs(context.Context, int64) ([]storage.Signoff, error) {
	return f.signoffs, nil
}

func (f *fakeSignoffStore) SetSignoff(_ context.Context, _ int64, environmentID, role, status, detail, decidedBy string) error {
	f.set = append(f.set, strings.Join([]string{environmentID, role, status, detail, decidedBy}, "|"))
	return nil
}

func (f *fakeSignoffStore) DeleteSignoff(_ context.Context, _ int64, environmentID, role string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, environmentID+"|"+role)
	return nil
}

func (f *fakeSignoffStore) SignoffEnvironmentExists(context.Context, int64, string) (bool, error) {
	return !f.unknownEnv, nil
}

func (f *fakeSignoffStore) ListEnvironments(context.Context, int64) ([]storage.Environment, error) {
	return f.environments, nil
}

func (f *fakeSignoffStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

func newSignoffStore() *fakeSignoffStore {
	return &fakeSignoffStore{
		release: host.Release{ID: 1, Tag: "1.8.0-rc1"},
		environments: []storage.Environment{
			{ID: "qualif", Label: "Qualification", Rank: 2},
			{ID: "prod", Label: "Production", Rank: 4},
		},
	}
}

func signoffRequestFor(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.SetPathValue("project", projectSlug)
	request.SetPathValue("tag", "1.8.0-rc1")
	request.SetPathValue("env", "qualif")
	identity := host.Identity{Subject: "121", Username: "vcarroy", Role: host.RoleOperator}
	return request.WithContext(hosttest.WithIdentity(request.Context(), identity))
}

// A reservation or a refusal with no reason tells whoever reads it later nothing, and yet that
// is the moment when the question "why?" arises.
func TestARefusalWithNoReasonIsRefused(t *testing.T) {
	for _, status := range []string{"warn", "fail"} {
		fake := newSignoffStore()
		handler := NewSignoffsHandler(fake, quietLogger())

		request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
			`{"role":"Client","status":"`+status+`","detail":""}`)
		recorder := httptest.NewRecorder()
		handler.Set(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("opinion %s with no reason: code %d, expected 400", status, recorder.Code)
		}
		if len(fake.set) != 0 {
			t.Errorf("opinion %s with no reason recorded: %v", status, fake.set)
		}
	}
}

// A validation needs no reason: it is the common case, and requiring one would have everyone
// write "ok" — a required field empty of meaning is filled in anyway.
func TestAValidationWithNoReasonIsAccepted(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"Client","status":"pass"}`)
	recorder := httptest.NewRecorder()
	handler.Set(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("code %d, expected 204: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.set) != 1 || !strings.HasPrefix(fake.set[0], "qualif|Client|pass|") {
		t.Errorf("opinion recorded: %v", fake.set)
	}
	// The opinion is recorded on the real subject and not on the account name: the same distinction
	// that had made the administrators' bootstrap fail.
	if !strings.HasSuffix(fake.set[0], "|121") {
		t.Errorf("opinion attributed to %q, expected subject 121", fake.set[0])
	}
}

// `na` is not recordable: waiting is the absence of a row. Accepting it would create two
// representations of the same state, one of which removal would not know how to tell apart.
func TestWaitingIsNotARecordableOpinion(t *testing.T) {
	for _, status := range []string{"na", "", "valide", "PASS"} {
		fake := newSignoffStore()
		handler := NewSignoffsHandler(fake, quietLogger())

		request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
			`{"role":"Client","status":"`+status+`","detail":"no matter"}`)
		recorder := httptest.NewRecorder()
		handler.Set(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("status %q: code %d, expected 400", status, recorder.Code)
		}
	}
}

// An opinion with no participant cannot be read: "validated" without saying by whom teaches
// nothing.
func TestAnOpinionWithNoRoleIsRefused(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"   ","status":"pass"}`)
	recorder := httptest.NewRecorder()
	handler.Set(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code %d, expected 400", recorder.Code)
	}
}

// An undeclared environment is a fault of the caller, not an outage: 404 with its name, and not
// a 500 surfacing from a foreign key.
func TestAnUndeclaredEnvironmentAnswers404(t *testing.T) {
	fake := newSignoffStore()
	fake.unknownEnv = true
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"Client","status":"pass"}`)
	recorder := httptest.NewRecorder()
	handler.Set(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code %d, expected 404", recorder.Code)
	}
}

// The decision is recorded: the table keeps only the last state, and "who had validated before
// retracting" has an answer only in the log.
func TestEveryDecisionIsRecorded(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodPut, "/releases/1.8.0-rc1/signoffs/qualif",
		`{"role":"Client","status":"fail","detail":"broken cart"}`)
	handler.Set(httptest.NewRecorder(), request)

	if len(fake.audit) != 1 || fake.audit[0] != AuditSignoffSet+":1.8.0-rc1" {
		t.Errorf("journal: %v", fake.audit)
	}

	removal := signoffRequestFor(t, http.MethodDelete, "/releases/1.8.0-rc1/signoffs/qualif?role=Client", "")
	handler.Clear(httptest.NewRecorder(), removal)

	if len(fake.audit) != 2 || fake.audit[1] != AuditSignoffClear+":1.8.0-rc1" {
		t.Errorf("log after removal: %v", fake.audit)
	}
}

// Removal requires the role: without it, one would not know which opinion to erase, and erasing
// every opinion of an environment would be far worse than a 400.
func TestRemovalRequiresARole(t *testing.T) {
	fake := newSignoffStore()
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodDelete, "/releases/1.8.0-rc1/signoffs/qualif", "")
	recorder := httptest.NewRecorder()
	handler.Clear(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code %d, expected 400", recorder.Code)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("removal carried out with no role: %v", fake.deleted)
	}
}

// Every environment is returned, including those with no opinion. Returning only the validated
// ones would leave the interface unable to tell "nobody has given an opinion" from "this
// environment does not exist".
func TestEveryEnvironmentIsReturnedEvenWithNoOpinion(t *testing.T) {
	fake := newSignoffStore()
	fake.signoffs = []storage.Signoff{
		{EnvironmentID: "qualif", Role: "Client", Status: "pass", DecidedBy: "121"},
	}
	handler := NewSignoffsHandler(fake, quietLogger())

	request := signoffRequestFor(t, http.MethodGet, "/releases/1.8.0-rc1/signoffs", "")
	recorder := httptest.NewRecorder()
	handler.List(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}

	var response struct {
		Environments []struct {
			Environment string `json:"environment"`
			Signoffs    []struct {
				Role string `json:"role"`
			} `json:"signoffs"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}

	if len(response.Environments) != 2 {
		t.Fatalf("%d environment(s), expected 2", len(response.Environments))
	}
	if response.Environments[1].Environment != "prod" {
		t.Errorf("second environment %q", response.Environments[1].Environment)
	}
	// An empty array and not `null`: the interface iterates over it with no guard.
	if response.Environments[1].Signoffs == nil {
		t.Error("the opinions of an environment with none must be an empty array")
	}
}

// ProjectBySlug resolves the project named in the path. One only here: these tests bear on what
// the handler does with the project, not on resolving it.
func (f *fakeSignoffStore) ProjectBySlug(_ context.Context, slug string) (host.Project, error) {
	if slug != projectSlug {
		return host.Project{}, host.ErrProjectNotFound
	}
	return host.Project{ID: 1, Slug: slug, Repository: "vcarroy/test", Dockerfile: "Dockerfile"}, nil
}
