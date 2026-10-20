package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/redmine"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The tracker probe against the real instance.
 *
 * Guarded behind `REDMINE_LIVE=1`, like the client's checks: the key is set in the current
 * development environment, and making the test depend on its mere presence would hit the
 * **production** instance at every `make test`.
 *
 *   REDMINE_LIVE=1 go test ./internal/worker/ -run Live -v
 *
 * # What it exercises, and what `internal/redmine` does not
 *
 * That one checks that the format **decodes**. This one checks that the real data, once
 * decoded, produce **valid** facts — and that is another risk. A version with no update date, a
 * request with no subject, an empty identifier: all things a fixture captured on a fine day
 * does not contain, and which would fail the probe every five minutes on a fact the database
 * refuses.
 *
 * It also measures what a **bootstrap pass** would cost: since the migration, it records the
 * facts without routing them, and the number of rows written deserves to be known before it is
 * known in production.
 *
 * # It writes nothing
 *
 * The projection goes into an in-memory db, and `internal/redmine`'s transport structurally
 * refuses any method other than GET and HEAD. A run has no effect, on either side.
 */

// inMemoryStore receives the projection without touching the database.
type inMemoryStore struct {
	versions []store.TrackedVersion
	issues   []store.TrackedIssue
}

func (m *inMemoryStore) UpsertRedmineVersion(_ context.Context, version store.TrackedVersion) (int64, error) {
	m.versions = append(m.versions, version)
	return int64(len(m.versions)), nil
}

func (m *inMemoryStore) UpsertRedmineIssue(_ context.Context, issue store.TrackedIssue) error {
	m.issues = append(m.issues, issue)
	return nil
}

func (m *inMemoryStore) AdoptPendingVersions(context.Context) ([]store.Adoption, error) {
	return nil, nil
}

func (m *inMemoryStore) StatusMapping(context.Context, int64) (map[string]string, error) {
	// Empty: this test bears on the facts, not on the status mapping. Every request will therefore
	// fall into `todo`, which is of no consequence here — the closed flag comes from Redmine, not
	// from the bucket.
	return nil, nil
}

func liveProbe(t *testing.T) (*TrackerSync, TrackerReader, string, *inMemoryStore) {
	t.Helper()
	if os.Getenv("REDMINE_LIVE") != "1" {
		t.Skip("REDMINE_LIVE not set: the check against the real instance is skipped")
	}

	project := os.Getenv("REDMINE_PROJECT")
	if project == "" {
		t.Fatal("REDMINE_PROJECT is required for the real check")
	}

	client, err := redmine.New(redmine.Options{
		URL:     os.Getenv("REDMINE_URL"),
		APIKey:  os.Getenv("REDMINE_API_KEY"),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		// The message does not quote the key, and has no business doing so: what is missing is read in
		// the variable's name.
		t.Fatalf("client Redmine: %v", err)
	}

	db := &inMemoryStore{}
	connect := func(TrackerConnection) (TrackerReader, error) { return client, nil }
	return NewTrackerSync(db, connect, quietLogger()), client, project, db
}

// Do the real data produce facts the database would accept?
//
// It is the only question fixtures cannot settle, since they come from it. An invalid fact
// would fail the probe at every pass, on one precise project, with nothing else moving.
func TestLiveTheTrackerFactsAreValid(t *testing.T) {
	sync, client, project, db := liveProbe(t)

	facts, cursor, err := sync.Observe(context.Background(), 1, project, client, "")
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if cursor == "" {
		t.Error("no cursor returned: the next pass would re-read everything")
	}

	parType := map[string]int{}
	keys := map[string]event.Event{}
	for _, fact := range facts {
		if err := fact.Validate(); err != nil {
			// The fact is described without its content: a request's subject may carry a client's name, and
			// a test log has no business keeping it.
			t.Errorf("fact %q invalid (key %q): %v", fact.Type, fact.DedupKey, err)
			continue
		}
		if precedent, seenParams := keys[fact.DedupKey]; seenParams {
			t.Errorf("key %q shared by two facts (%q and %q): one of the two "+
				"would never be recorded", fact.DedupKey, precedent.Type, fact.Type)
		}
		keys[fact.DedupKey] = fact
		parType[string(fact.Type)]++

		// A missing date would give a fact from year zero, buried under everything else in a feed
		// sorted by date.
		if fact.OccurredAt.IsZero() {
			t.Errorf("fact %q with no date (key %q)", fact.Type, fact.DedupKey)
		}
	}

	// What a bootstrap pass would cost in production: that many rows written into the fact feed,
	// with no reaction starting.
	t.Logf("projectRow %q: %d versions, %d issues read", project,
		len(db.versions), len(db.issues))
	t.Logf("facts of a bootstrap: %d in all — %d versions, %d closures",
		len(facts), parType[string(event.VersionCreated)], parType[redmineIssueClosed])
}

// A second pass does not observe again what the first saw.
//
// It is the property the migration fixed, exercised on real data rather than on a test probe:
// the versions are re-read whole at each pass, and only the stability of the keys stops the
// snapshot re-triggering everything.
func TestLiveASecondPassDoesNotObserveTheVersionsAgain(t *testing.T) {
	sync, client, project, _ := liveProbe(t)

	first, cursor, err := sync.Observe(context.Background(), 1, project, client, "")
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}

	second, _, err := sync.Observe(context.Background(), 1, project, client, cursor)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}

	seenItems := map[string]bool{}
	for _, fact := range first {
		seenItems[fact.DedupKey] = true
	}

	// Without this guard, the test would pass for a bad reason: a second pass returning **nothing**
	// would have no new key, and would prove nothing at all. What must be seen is that it observes
	// the same versions again — the snapshot is re-read whole — and that their keys coincide.
	if len(second) == 0 {
		t.Fatal("the second pass returned nothing: it can prove nothing " +
			"about the stability of the keys")
	}
	t.Logf("passes: %d facts then %d, of which %d keys already seen",
		len(first), len(second), sharedKeys(seenItems, second))

	newOnes := 0
	for _, fact := range second {
		if !seenItems[fact.DedupKey] {
			newOnes++
			t.Logf("new key at the second pass: %q", fact.DedupKey)
		}
	}

	// Zero, unless someone declares a version between the two calls — which would be a coincidence,
	// not a defect. The message says so rather than accusing the code.
	if newOnes > 0 {
		t.Errorf("%d new keys between two immediate passes: the versions "+
			"would be observed again at every round (or someone typed one in the meantime)",
			newOnes)
	}
}

// shared counts the facts whose key was already known.
func sharedKeys(seenItems map[string]bool, facts []event.Event) int {
	total := 0
	for _, fact := range facts {
		if seenItems[fact.DedupKey] {
			total++
		}
	}
	return total
}
