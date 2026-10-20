package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeRedmine struct {
	versions []provider.Version
	issues   []provider.Issue
	queries  []provider.IssueQuery
	err      error
}

func (f *fakeRedmine) ListVersions(context.Context, string) ([]provider.Version, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.versions, nil
}

func (f *fakeRedmine) ListIssues(_ context.Context, query provider.IssueQuery) ([]provider.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.queries = append(f.queries, query)
	return f.issues, nil
}

// fakeRedmineStore now carries only the projection.
//
// Five methods disappeared from this fixture along the way — tracked projects, plugin settings,
// cursor, its two outcomes. They live in `multiStore`, which drives the loop. It is the most
// concrete measure of what the migration moved.
type fakeRedmineStore struct {
	versions  []store.TrackedVersion
	issues    []store.TrackedIssue
	mapping   map[string]string
	state     store.SyncState
	adoptions []store.Adoption
	adopted   int
	// locals assigns one local key per identity, as the real store's uniqueness does. See the
	// comment on `UpsertRedmineVersion` below.
	locals map[string]int64
}

// UpsertRedmineVersion returns a local key **per identity**, and that is what makes this
// fixture a faithful model.
//
// It returned the tool's identifier reinterpreted as an integer. Version 805 of two projects
// therefore received the same local key — impossible in the database, where uniqueness bears on
// `(project_id, tool, external_id)`. Since the `version.created` fact is now deduplicated on
// that key, the fixture would have made a fact the real store tells apart look shared.
func (f *fakeRedmineStore) UpsertRedmineVersion(_ context.Context, version store.TrackedVersion) (int64, error) {
	f.versions = append(f.versions, version)

	if f.locals == nil {
		f.locals = map[string]int64{}
	}
	identity := fmt.Sprintf("%d/%s/%s", version.ProjectID, version.Tool, version.ExternalID)
	if id, known := f.locals[identity]; known {
		return id, nil
	}
	id := int64(len(f.locals) + 1)
	f.locals[identity] = id
	return id, nil
}

func (f *fakeRedmineStore) UpsertRedmineIssue(_ context.Context, issue store.TrackedIssue) error {
	f.issues = append(f.issues, issue)
	return nil
}

func (f *fakeRedmineStore) AdoptPendingVersions(context.Context) ([]store.Adoption, error) {
	f.adopted++
	return f.adoptions, nil
}

func (f *fakeRedmineStore) StatusMapping(context.Context, int64) (map[string]string, error) {
	return f.mapping, nil
}

// testOpener opens by stripping the sealing prefix: enough to check that the unsealed value is
// indeed the one reaching the client, without making these tests depend on real encryption.
type testOpener struct{ err error }

func (o testOpener) Open(sealed string) (string, error) {
	if o.err != nil {
		return "", o.err
	}
	return strings.TrimPrefix(sealed, "sealed:"), nil
}

func (o testOpener) Configured() bool { return true }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newSync(t *testing.T, client *fakeRedmine, db *fakeRedmineStore) *TrackerSync {
	t.Helper()
	if db.mapping == nil {
		db.mapping = map[string]string{"En cours": "doing", "Fermé": "done", "Rejeté": "rejected"}
	}
	connect := func(TrackerConnection) (TrackerReader, error) { return client, nil }
	return NewTrackerSync(db, connect, quietLogger())
}

// pass runs one projection pass, as the probe would.
//
// It replaces the old `Sync`: the loop went into `probes.go`, and what remains here is the
// projection alone. The assertions have not changed — that is the whole point.
func pass(t *testing.T, client *fakeRedmine, db *fakeRedmineStore) ([]event.Event, string, error) {
	t.Helper()
	sync := newSync(t, client, db)
	return sync.Observe(context.Background(), 1, "mse-dev", client, db.state.Cursor)
}

// The versions before the requests: a request attaches to its version by its external
// identifier, and the reverse would leave unattached every request targeting a newly created
// version.
func TestVersionsAreDepositedBeforeRequests(t *testing.T) {
	version := "805"
	client := &fakeRedmine{
		versions: []provider.Version{{ID: "805", Name: "v3.2.0", Status: "open"}},
		issues: []provider.Issue{
			{ID: "1", VersionID: &version, Tracker: "Évolution", Status: "En cours"},
		},
	}
	db := &fakeRedmineStore{}

	if _, _, err := pass(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.versions) != 1 || db.versions[0].Normalized != "3.2.0" {
		t.Errorf("versions deposited: %+v", db.versions)
	}
	if len(db.issues) != 1 || db.issues[0].VersionExternalID == nil {
		t.Fatalf("requests deposited: %+v", db.issues)
	}
	// The adoption happens after the versions are deposited, and only once.
	if db.adopted != 1 {
		t.Errorf("%d adoptions, expected 1", db.adopted)
	}
}

// The name is normalised for matching, never rewritten: a version's identity is its Redmine
// identifier.
func TestTheNameIsNormalisedForMatchingWithoutBeingRewritten(t *testing.T) {
	client := &fakeRedmine{versions: []provider.Version{
		{ID: "803", Name: "MSE 3.1.2", Status: "closed"},
		{ID: "805", Name: "v3.2.0", Status: "open"},
		{ID: "801", Name: "MSE3-1.5", Status: "closed"},
	}}
	db := &fakeRedmineStore{}

	if _, _, err := pass(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	expected := map[string]string{
		"v3.2.0": "3.2.0",
		// Only `v` is stripped: the tracker's historical conventions stay intact, and attach to no tag.
		"MSE 3.1.2": "MSE 3.1.2",
		"MSE3-1.5":  "MSE3-1.5",
	}
	for _, version := range db.versions {
		if want := expected[version.Name]; version.Normalized != want {
			t.Errorf("%q normalised to %q, expected %q", version.Name, version.Normalized, want)
		}
	}
}

// An unknown status falls into `todo` and does not interrupt the pass: the instance's statuses
// change without warning, and refusing the request would lose the rest.
func TestAnUnknownStatusFallsIntoTodoWithoutInterrupting(t *testing.T) {
	client := &fakeRedmine{issues: []provider.Issue{
		{ID: "1", Tracker: "Évolution", Status: "Statut Inédit"},
		{ID: "2", Tracker: "Évolution", Status: "En cours"},
	}}
	db := &fakeRedmineStore{}

	if _, _, err := pass(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.issues) != 2 {
		t.Fatalf("%d requests deposited", len(db.issues))
	}
	byID := map[string]store.TrackedIssue{}
	for _, issue := range db.issues {
		byID[issue.ExternalID] = issue
	}
	if byID["1"].StatusBucket != "todo" {
		t.Errorf("unknown status mapped to %q, expected todo", byID["1"].StatusBucket)
	}
	if byID["2"].StatusBucket != "doing" {
		t.Errorf("known status mapped to %q", byID["2"].StatusBucket)
	}
}

// The cursor is taken **before** the read. Taking it afterwards would make the reading time a
// blind spot: the requests modified during it would be neither in this pass nor in the next.
func TestTheCursorIsTakenBeforeTheRead(t *testing.T) {
	client := &fakeRedmine{}
	db := &fakeRedmineStore{}
	sync := newSync(t, client, db)

	start := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	sync.now = func() time.Time { return start }

	// The cursor is **returned** rather than written: since the migration, it is the probe loop
	// that persists it. What the projection must hold is the instant it chooses — and it stays the
	// one from before the read.
	_, cursor, err := sync.Observe(context.Background(), 1, "mse-dev", client, "")
	if err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if cursor != start.Format(time.RFC3339) {
		t.Errorf("cursor %q, expected %q", cursor, start.Format(time.RFC3339))
	}
}

// An overlap margin is taken off the cursor: Redmine filters `updated_on` to the second and its
// clock is not ours. Starting again from the exact instant would miss any request modified in
// the same second as the end of the previous pass.
func TestTheCursorResumesWithAnOverlapMargin(t *testing.T) {
	precedent := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	client := &fakeRedmine{}
	db := &fakeRedmineStore{state: store.SyncState{Cursor: precedent.Format(time.RFC3339)}}

	if _, _, err := pass(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(client.queries) != 1 {
		t.Fatalf("%d requests", len(client.queries))
	}
	since := client.queries[0].Since
	if !since.Equal(precedent.Add(-time.Minute)) {
		t.Errorf("resume at %v, expected %v (one minute of overlap)",
			since, precedent.Add(-time.Minute))
	}
}

// An unreadable cursor leads to re-reading everything: expensive but correct, where starting
// again from a wrong instant would lose requests silently.
func TestAnUnreadableCursorMakesEverythingBeReadAgain(t *testing.T) {
	for _, cursor := range []string{"", "not-a-date", "17/08/2026"} {
		client := &fakeRedmine{}
		db := &fakeRedmineStore{state: store.SyncState{Cursor: cursor}}

		if _, _, err := pass(t, client, db); err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		if len(client.queries) != 1 {
			t.Fatalf("cursor %q: %d requests", cursor, len(client.queries))
		}
		if !client.queries[0].Since.IsZero() {
			t.Errorf("cursor %q: resume at %v, expected a complete read",
				cursor, client.queries[0].Since)
		}
	}
}

// A failure is recorded and the cursor does not advance: advancing it would skip the unread
// window.
func TestAFailureDoesNotAdvanceTheCursor(t *testing.T) {
	client := &fakeRedmine{err: errors.New("Redmine injoignable")}
	db := &fakeRedmineStore{}

	facts, cursor, err := newSync(t, client, db).
		Observe(context.Background(), 1, "mse-dev", client, "")

	if err == nil {
		t.Fatal("a tracker outage must fail the pass")
	}
	// No cursor returned: the loop therefore has nothing to advance, and the next pass will see the
	// unread window again. It is the same guarantee as before the migration, obtained by returning
	// nothing rather than by writing nothing.
	if cursor != "" {
		t.Errorf("cursor returned despite the failure: %q", cursor)
	}
	if len(facts) != 0 {
		t.Errorf("%d facts returned despite the failure", len(facts))
	}
}

// A Redmine outage must not carry off the loop: it is a third-party service, and its outage has
// nothing to do with scheduling or observation.
//
// # This test changed subject, and that is the sign the migration took
//
// It drove `TrackerSync.Run`, its own loop, its own isolation of failures. That loop no longer
// exists: `ProbeRunner` holds it, for every source, and
// `TestAnUnreachableForgeDoesNotStopTheOtherProjects` exercises it.
//
// What remains the tracker's business is what is checked here: **reporting** the error, so that
// the loop has something to isolate.
func TestARedmineOutageIsReportedToTheLoop(t *testing.T) {
	client := &fakeRedmine{err: errors.New("502 Bad Gateway")}
	db := &fakeRedmineStore{}

	_, _, err := newSync(t, client, db).
		Observe(context.Background(), 1, "mse-dev", client, "")

	if err == nil {
		t.Fatal("the outage must be reported, otherwise the loop would believe the pass succeeded")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("message %q — it must carry what the tracker answered", err)
	}
}

// Redmine's closed flag is passed through as it is, independently of the bucket: the instance
// declares `Ré-ouvert` as closed, and the two pieces of information are distinct.
func TestTheClosedFlagIsPassedIndependentlyOfTheBucket(t *testing.T) {
	client := &fakeRedmine{issues: []provider.Issue{
		{ID: "1", Tracker: "Anomalie", Status: "Ré-ouvert", StatusClosed: true},
	}}
	db := &fakeRedmineStore{mapping: map[string]string{"Ré-ouvert": "doing"}}

	if _, _, err := pass(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.issues) != 1 {
		t.Fatalf("%d issues", len(db.issues))
	}
	if !db.issues[0].IsClosed {
		t.Error("Redmine's flag must be kept")
	}
	if db.issues[0].StatusBucket != "doing" {
		t.Errorf("bucket %q, expected doing", db.issues[0].StatusBucket)
	}
}
