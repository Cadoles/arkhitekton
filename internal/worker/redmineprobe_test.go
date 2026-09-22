package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les faits que le suivi constate.
 *
 * Ce que la migration ajoute au produit — jusqu'ici, la synchronisation projetait
 * en base et personne ne pouvait s'y brancher.
 */

func instant(moment time.Time) *time.Time { return &moment }

// Versions and closed requests become facts; the rest do not.
func TestTheTrackerObservesVersionsAndClosedRequests(t *testing.T) {
	echeance := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	client := &fakeRedmine{
		versions: []provider.Version{
			{ID: "805", Name: "v3.2.0", Status: "open", DueDate: &echeance,
				UpdatedOn: instant(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC))},
		},
		issues: []provider.Issue{
			{ID: "1", Tracker: "Anomalie", Status: "Fermé", StatusClosed: true,
				UpdatedOn: instant(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC))},
			// Open: it is projected, but observes nothing. One fact per status change would turn the fact
			// feed into a typing log.
			{ID: "2", Tracker: "Évolution", Status: "En cours"},
		},
	}
	db := &fakeRedmineStore{}

	facts, _, err := pass(t, client, db)
	if err != nil {
		t.Fatalf("%v", err)
	}

	parType := map[string]int{}
	for _, fact := range facts {
		parType[string(fact.Type)]++
		if err := fact.Validate(); err != nil {
			t.Errorf("fait %q invalide: %v", fact.Type, err)
		}
		// The **fact's** date, the tracker's — never the read's. A probe catching up three days must
		// produce facts dated across those three days, otherwise the order is lost.
		if fact.OccurredAt.After(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("fact %q dated from the read: %v", fact.Type, fact.OccurredAt)
		}
	}

	if parType[string(event.VersionCreated)] != 1 {
		t.Errorf("%d version facts, expected 1", parType[string(event.VersionCreated)])
	}
	if parType[redmineIssueClosed] != 1 {
		t.Errorf("%d closure facts, expected 1 — the open request produces none",
			parType[redmineIssueClosed])
	}
}

// The deduplication key does not move from one pass to the next.
//
// It is what carries the meaning of "created": every pass re-reads all the versions, and only
// the key's uniqueness stops a version already seen being observed again. A key that varied —
// because it carried the update date, say — would re-trigger at every modification.
func TestATrackerFactsKeyDoesNotMoveFromOnePassToTheNext(t *testing.T) {
	version := provider.Version{ID: "805", Name: "v3.2.0", Status: "open"}
	client := &fakeRedmine{versions: []provider.Version{version}}
	// **The same store** for both passes, as in production: the key now carries the local row, and
	// two fresh stores would prove only that two counters starting from zero give the same number.
	db := &fakeRedmineStore{}

	premiere, _, err := pass(t, client, db)
	if err != nil {
		t.Fatalf("%v", err)
	}

	// The version changes status and date: it stays the same version.
	client.versions[0].Status = "closed"
	client.versions[0].UpdatedOn = instant(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	seconde, _, err := pass(t, client, db)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(premiere) != 1 || len(seconde) != 1 {
		t.Fatalf("faits = %d puis %d", len(premiere), len(seconde))
	}
	if premiere[0].DedupKey != seconde[0].DedupKey {
		t.Errorf("keys %q then %q — the version would be observed again",
			premiere[0].DedupKey, seconde[0].DedupKey)
	}
}

// Two projects do not share the key, and no longer for the same reason.
//
// # What the move into the core shifted
//
// The key carried the tracker's project, because two Redmine instances each number from 1. It
// now carries the **local row**, whose uniqueness is `(project_id, tool, external_id)` — the
// project is therefore still there, but once, and on the dashboard's side.
//
// What that gains: `version.created` has become a core fact, and its key no longer depends on
// the tool that observed it. An instance changing tracker would not observe its whole history
// again.
func TestTwoProjectsDoNotShareAVersionsKey(t *testing.T) {
	client := &fakeRedmine{versions: []provider.Version{{ID: "805", Name: "v3.2.0"}}}
	sync := newSync(t, client, &fakeRedmineStore{})

	un, _, err := sync.Observe(context.Background(), 1, "project-one", client, "cursor")
	if err != nil {
		t.Fatalf("%v", err)
	}
	deux, _, err := sync.Observe(context.Background(), 2, "project-two", client, "cursor")
	if err != nil {
		t.Fatalf("%v", err)
	}

	if un[0].DedupKey == deux[0].DedupKey {
		t.Errorf("two projects share the key %q", un[0].DedupKey)
	}
	// And the project's name in the tracker has nothing to do with it: putting it in the key would
	// mean a project renamed at the client's would have its versions observed again.
	for _, nom := range []string{"project-one", "project-two"} {
		if strings.Contains(un[0].DedupKey, nom) || strings.Contains(deux[0].DedupKey, nom) {
			t.Errorf("keys %q / %q — they carry the tracker's project",
				un[0].DedupKey, deux[0].DedupKey)
		}
	}
}

// The payload names the tool and the identifier it uses.
//
// That is what the key stopped carrying, and it has to live somewhere: without them, a core
// fact would no longer allow tracing back to the record it speaks of.
func TestAVersionsPayloadNamesTheToolAndItsIdentifier(t *testing.T) {
	client := &fakeRedmine{versions: []provider.Version{{ID: "805", Name: "v3.2.0"}}}

	facts, _, err := pass(t, client, &fakeRedmineStore{})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("%d faits", len(facts))
	}
	if facts[0].Payload["tool"] != trackerTool || facts[0].Payload["external_id"] != "805" {
		t.Errorf("charge = %v", facts[0].Payload)
	}
}

// A project with no tracker identifier is ignored, silently.
//
// # A correction of judgement
//
// This test required the opposite: that the probe **refuse** by name, on the grounds that a
// plugin switched on for a project is an intention. The production log showed otherwise — the
// plugin is enabled at the instance level, and most projects have no counterpart in the
// tracker. The normal case therefore shouted every five minutes, for each of them.
func TestAProjectWithNoTrackerIdentifierIsIgnored(t *testing.T) {
	sonde := NewRedmineProbe(NewTrackerSync(&fakeRedmineStore{},
		func(TrackerConnection) (TrackerReader, error) {
			t.Error("no connection must be opened")
			return &fakeRedmine{}, nil
		},
		quietLogger()))

	facts, cursor, err := sonde.Poll(context.Background(),
		store.Project{ID: 1, Slug: "mse"}, map[string]string{"url": "https://r.fr"}, "cursor")

	if err != nil {
		t.Fatalf("%v — the absence of a tracker is not an outage", err)
	}
	if len(facts) != 0 {
		t.Errorf("%d fait(s) rendus", len(facts))
	}
	// The cursor is returned unchanged: nothing was read, so nothing advanced.
	if cursor != "cursor" {
		t.Errorf("cursor = %q, expected unchanged", cursor)
	}
}
