package store

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
)

/*
 * The facts, and the only property that counts at this stage: a redelivery writes nothing.
 *
 * The observer goes over the finished runs again — informer resynchronisation, worker restart.
 * Without that bound, every pass would produce a duplicate. Today nobody listens and it would
 * cost nothing; the day a reaction sends a notification, it would send one per restart.
 */

func testFact(key string) event.Event {
	return event.Event{
		Type: event.ReleaseCreated, ProjectID: ptr(testProjectID),
		Subject:    "1.8.0-rc1",
		Payload:    map[string]string{"tag": "1.8.0-rc1", "kind": "pre"},
		OccurredAt: time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC),
		DedupKey:   key,
	}
}

func ptr(value int64) *int64 { return &value }

// The fact that carries the whole mechanism: re-emitting writes nothing, and says so.
func TestARedeliveryWritesNothing(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	recorded, err := db.RecordEvent(ctx, testFact("release.created:42"))
	if err != nil {
		t.Fatalf("first emission: %v", err)
	}
	if !recorded {
		t.Fatal("the first observation of a fact must be written")
	}

	// The redelivery. The fact is the same; only the reading time changed.
	repeated := testFact("release.created:42")
	repeated.OccurredAt = repeated.OccurredAt.Add(time.Hour)
	recorded, err = db.RecordEvent(ctx, repeated)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if recorded {
		t.Error("a redelivery produced a second fact")
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d facts recorded, expected 1", len(events))
	}
	// And it is the first that survives: `do nothing` does not replace. An overwrite would move
	// the fact's instant forward at every restart, and the history would no longer be dated by
	// what happened.
	//
	// Compared in UTC: PostgreSQL returns a `timestamptz` in the session's time zone, and
	// comparing a local time would fail this test depending on the machine running it.
	if got := events[0].OccurredAt.UTC(); !got.Equal(testFact("").OccurredAt) {
		t.Errorf("instant = %v, expected the first observation's", got)
	}
}

// Two distinct facts coexist: the bound is on the key, not on the type.
func TestTwoDistinctFactsCoexist(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	for _, key := range []string{"release.created:42", "release.created:43"} {
		if _, err := db.RecordEvent(ctx, testFact(key)); err != nil {
			t.Fatalf("emission %q: %v", key, err)
		}
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("%d facts, expected 2 — two releases are two facts", len(events))
	}
}

// An invalid fact is refused before reaching the database.
//
// The missing key in particular: the insert would have succeeded on an empty column, and
// the flaw would only have shown once a reaction was connected.
func TestAnInvalidFactDoesNotReachTheDatabase(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	noKey := testFact("")
	if _, err := db.RecordEvent(ctx, noKey); err == nil {
		t.Fatal("a fact with no key was accepted")
	}

	events, _ := db.ListEvents(ctx, testProjectID, 0)
	if len(events) != 0 {
		t.Errorf("%d facts written despite the refusal", len(events))
	}
}

// The feed is returned newest first: that is the order in which one comes looking for what
// just happened.
func TestTheFeedReturnsTheMostRecentFirst(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	for index, offset := range []time.Duration{0, 2 * time.Hour, time.Hour} {
		event := testFact("fact-" + string(rune('a'+index)))
		event.OccurredAt = base.Add(offset)
		if _, err := db.RecordEvent(ctx, event); err != nil {
			t.Fatalf("emission: %v", err)
		}
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("%d facts", len(events))
	}
	for index := 1; index < len(events); index++ {
		if events[index].OccurredAt.After(events[index-1].OccurredAt) {
			t.Errorf("order broken at position %d: %v after %v",
				index, events[index].OccurredAt, events[index-1].OccurredAt)
		}
	}
}

// The payload crosses the database without deforming, and the feed is bounded to the
// project.
func TestThePayloadCrossesAndTheFeedStaysWithinTheProject(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.RecordEvent(ctx, testFact("release.created:42")); err != nil {
		t.Fatalf("emission: %v", err)
	}

	events, err := db.ListEvents(ctx, testProjectID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d facts", len(events))
	}
	if events[0].Payload["tag"] != "1.8.0-rc1" || events[0].Payload["kind"] != "pre" {
		t.Errorf("payload = %v", events[0].Payload)
	}
	if events[0].Source != "" {
		t.Errorf("source = %q, expected empty for the core", events[0].Source)
	}

	// Another project sees nothing: the feed answers "nothing happened here", which is the
	// question asked.
	other, err := db.CreateProject(ctx, Project{
		Repository: "vcarroy/other", Slug: "other", Label: "Autre",
		BuildContext: ".",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	neighbours, err := db.ListEvents(ctx, other, 0)
	if err != nil {
		t.Fatalf("reading the neighbour: %v", err)
	}
	if len(neighbours) != 0 {
		t.Errorf("%d facts spilled onto another project", len(neighbours))
	}
}
