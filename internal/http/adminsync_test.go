package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeSyncStore struct {
	// tracked says whether the project declares a tracker. That is what "configured" means now
	// that the connection is a project setting: a global flag answered for everyone.
	tracked   bool
	state     store.SyncState
	unmapped  []store.UnmappedStatus
	ambiguous []store.AmbiguousVersion
	pending   []string
	mapping   map[string]string
	set       []string
	reset     int
	moved     int64
	audit     []string
}

func (f *fakeSyncStore) GetSyncState(context.Context, string, int64) (store.SyncState, error) {
	return f.state, nil
}

func (f *fakeSyncStore) ResetSyncCursor(context.Context, string, int64) error {
	f.reset++
	return nil
}

func (f *fakeSyncStore) UnmappedStatuses(context.Context, int64) ([]store.UnmappedStatus, error) {
	return f.unmapped, nil
}

func (f *fakeSyncStore) AmbiguousVersions(context.Context, int64) ([]store.AmbiguousVersion, error) {
	return f.ambiguous, nil
}

func (f *fakeSyncStore) PendingVersions(context.Context, int64) ([]string, error) {
	return f.pending, nil
}

func (f *fakeSyncStore) StatusMapping(context.Context, int64) (map[string]string, error) {
	return f.mapping, nil
}

func (f *fakeSyncStore) SetStatusBucket(_ context.Context, _ int64, statusName, bucket string) error {
	f.set = append(f.set, statusName+"="+bucket)
	return nil
}

func (f *fakeSyncStore) ReprojectIssues(context.Context, int64) (int64, error) {
	return f.moved, nil
}

// ProjectBySlug resolves the path's project. The tracker it declares — or does not — is what
// the screen now reads to say "configured".
func (f *fakeSyncStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "mse" {
		return store.Project{}, store.ErrProjectNotFound
	}
	project := store.Project{ID: 7, Slug: "mse"}
	if f.tracked {
		project.TrackerProject = "mse-dev"
	}
	return project, nil
}

// syncRequest composes a request carrying the project in its path.
func syncRequest(method, path, body string) *http.Request {
	request := adminRequest(method, path, body)
	request.SetPathValue("project", "mse")
	return request
}

func (f *fakeSyncStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

// The complete mapping must be sent, and not only the unknown statuses.
//
// That is the contract the setting in the interface depends on. It was returned by the API
// and never displayed: the screen exposed only the unmapped statuses, hence nothing at all
// once the bootstrap covered them all — which is the case on the real instance. The statuses
// awaiting the deployment's arbitration were therefore unreachable.
func TestTheStateCarriesTheWholeMappingAndNotOnlyTheUnknowns(t *testing.T) {
	fake := &fakeSyncStore{
		tracked: true,
		mapping: map[string]string{
			"Livré":         "doing",
			"Prêt à livrer": "doing",
			"Fermé":         "done",
			"Rejeté":        "rejected",
			"Nouveau":       "todo",
		},
		// No unknown status: the real situation after the bootstrap.
		unmapped: nil,
	}
	handler := NewSyncAdminHandler(fake, fixedTracker{id: "redmine"})

	recorder := httptest.NewRecorder()
	handler.State(recorder, syncRequest(http.MethodGet, "/projects/mse/sync", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}

	var response struct {
		Configured bool              `json:"configured"`
		Mapping    map[string]string `json:"mapping"`
		Unmapped   []any             `json:"unmapped"`
		Pending    []string          `json:"pending"`
		Ambiguous  []any             `json:"ambiguous"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}

	if len(response.Mapping) != 5 {
		t.Errorf("%d statuses returned, expected 5 — the complete mapping is what makes the setting possible",
			len(response.Mapping))
	}
	// The statuses awaiting arbitration must be part of it.
	for _, status := range []string{"Livré", "Prêt à livrer"} {
		if _, ok := response.Mapping[status]; !ok {
			t.Errorf("status %q missing from the returned mapping", status)
		}
	}
	// Empty arrays and not null: the interface iterates over them without a guard.
	if response.Unmapped == nil || response.Pending == nil || response.Ambiguous == nil {
		t.Error("empty lists must be arrays, not null")
	}
	if !response.Configured {
		t.Error("configured should be true")
	}
}

// A project with no declared tracker must say "not configured" and not "never
// synchronised", which would send someone hunting for a fault.
//
// The question changed nature: it used to be about the instance's key, it is now about
// what the project declares. A project with no tracker is not failing to synchronise — it
// expects no synchronisation.
func TestAProjectWithNoTrackerSaysNotConfigured(t *testing.T) {
	handler := NewSyncAdminHandler(&fakeSyncStore{}, fixedTracker{id: "redmine"})

	recorder := httptest.NewRecorder()
	handler.State(recorder, syncRequest(http.MethodGet, "/projects/mse/sync", ""))

	var response struct {
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if response.Configured {
		t.Error("configured should be false without a key")
	}
}

// Declaring a status reapplies the rule in the same gesture, and returns how many issues
// moved: without that the new rule would hold only for issues modified afterwards in the
// tracker, and the screen would mix two rules without saying so.
func TestDeclaringAStatusReappliesAndReturnsTheNumberMoved(t *testing.T) {
	fake := &fakeSyncStore{tracked: true, moved: 17}
	handler := NewSyncAdminHandler(fake, fixedTracker{id: "redmine"})

	request := syncRequest(http.MethodPut, "/projects/mse/sync/tracker/mapping/Livr%C3%A9", `{"bucket":"done"}`)
	request.SetPathValue("status", "Livré")
	recorder := httptest.NewRecorder()
	handler.SetMapping(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.set) != 1 || fake.set[0] != "Livré=done" {
		t.Errorf("mapping recorded: %v", fake.set)
	}

	var response struct {
		IssuesMoved int64 `json:"issues_moved"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if response.IssuesMoved != 17 {
		t.Errorf("%d moved requests reported, expected 17", response.IssuesMoved)
	}

	// Changing a mapping moves a rate displayed to the client: it is recorded.
	if len(fake.audit) != 1 || !strings.HasPrefix(fake.audit[0], "sync.tracker.mapping:") {
		t.Errorf("journal: %v", fake.audit)
	}
}

// An invented bucket is refused: the database constraint would forbid it, but a 500 raised
// from a constraint violation does not tell the caller what is accepted.
func TestAnInventedBucketIsRefused(t *testing.T) {
	for _, bucket := range []string{"", "termine", "DONE", "en-cours"} {
		fake := &fakeSyncStore{}
		handler := NewSyncAdminHandler(fake, fixedTracker{id: "redmine"})

		request := syncRequest(http.MethodPut, "/projects/mse/sync/tracker/mapping/X", `{"bucket":"`+bucket+`"}`)
		request.SetPathValue("status", "X")
		recorder := httptest.NewRecorder()
		handler.SetMapping(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("bucket %q: code %d, expected 400", bucket, recorder.Code)
		}
		if len(fake.set) != 0 {
			t.Errorf("bucket %q recorded anyway: %v", bucket, fake.set)
		}
	}
}

// Resynchronising does not query the tracker: it clears the cursor, and the worker's next
// pass re-reads everything. The message must say so, otherwise a user seeing nothing change
// would keep relaunching.
func TestResyncClearsTheCursorAndAnnouncesTheWait(t *testing.T) {
	fake := &fakeSyncStore{}
	handler := NewSyncAdminHandler(fake, fixedTracker{id: "redmine"})

	recorder := httptest.NewRecorder()
	handler.Resync(recorder, syncRequest(http.MethodPost, "/projects/mse/sync/tracker/resync", ""))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code %d", recorder.Code)
	}
	if fake.reset != 1 {
		t.Errorf("%d resets", fake.reset)
	}

	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if !strings.Contains(response.Message, "five minutes") {
		t.Errorf("the message must announce the wait: %q", response.Message)
	}
	if len(fake.audit) != 1 {
		t.Errorf("journal: %v", fake.audit)
	}
}

// A project with no tracker has no cursor to reset, and is told so rather than reset nothing.
func TestResyncWithoutATrackerIsRefused(t *testing.T) {
	handler := NewSyncAdminHandler(&fakeSyncStore{}, nil)

	recorder := httptest.NewRecorder()
	handler.Resync(recorder, syncRequest(http.MethodPost, "/projects/mse/sync/tracker/resync", ""))

	if recorder.Code != http.StatusConflict {
		t.Errorf("code %d, expected 409", recorder.Code)
	}
}
