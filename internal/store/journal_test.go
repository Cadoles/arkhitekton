package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Cadoles/arkhitekton/internal/event"
	"github.com/Cadoles/arkhitekton/internal/reaction"
)

/*
 * The journal: actions and events in one feed, newest first, each event with the automations it
 * set off.
 */

func TestTheJournalMergesActionsAndEvents(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// An event, then an action on the project, then an instance action: newest first.
	fact := testFact("release.created:7")
	fact.OccurredAt = time.Now().Add(-time.Hour)
	if _, err := db.RecordEvent(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if err := db.Audit(WithProject(ctx, testProjectID), "vcarroy", AuditReleaseCreate, "1.8.0-rc1", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Audit(ctx, "vcarroy", AuditRoleGrant, "alice", map[string]any{"role": "operator"}); err != nil {
		t.Fatal(err)
	}

	all, err := db.ListJournal(ctx, JournalQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Action != AuditRoleGrant || all[1].Action != AuditReleaseCreate ||
		all[2].Nature != JournalEvent || all[2].Subject != "1.8.0-rc1" || all[2].Project != "test" {
		t.Fatalf("journal = %+v", all)
	}
	if all[0].Payload["role"] != "operator" || all[2].Payload["tag"] != "1.8.0-rc1" {
		t.Errorf("payloads = %v, %v", all[0].Payload, all[2].Payload)
	}

	project := int64(testProjectID)
	scoped, _ := db.ListJournal(ctx, JournalQuery{ProjectID: &project})
	if len(scoped) != 2 || scoped[0].Project != "test" {
		t.Errorf("the project's journal = %+v", scoped)
	}
	actions, _ := db.ListJournal(ctx, JournalQuery{Nature: JournalAction})
	events, _ := db.ListJournal(ctx, JournalQuery{Nature: JournalEvent})
	if len(actions) != 2 || len(events) != 1 {
		t.Errorf("actions %d, events %d", len(actions), len(events))
	}
	found, _ := db.ListJournal(ctx, JournalQuery{Search: "ALICE"})
	if len(found) != 1 || found[0].Target != "alice" {
		t.Errorf("searching a target = %+v", found)
	}
	if limited, _ := db.ListJournal(ctx, JournalQuery{Limit: 1}); len(limited) != 1 || limited[0].Action != AuditRoleGrant {
		t.Errorf("limited = %+v", limited)
	}
}

// An event says which automations it set off, by the reaction's name; one that set off none
// says nothing.
func TestAnEventCarriesTheAutomationsItSetOff(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	for _, key := range []string{"release.created:1", "release.created:2"} {
		if _, err := db.RecordEvent(ctx, testFact(key)); err != nil {
			t.Fatal(err)
		}
	}
	reactionID, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: testProjectID, Name: "Vérifier la RC", EventType: string(event.ReleaseCreated),
		Enabled: true, Plan: buildPlan(),
	}, "vcarroy")
	if err != nil {
		t.Fatal(err)
	}
	project := int64(testProjectID)
	if _, _, err := db.CreatePlanRun(ctx, PlanRun{
		Trigger: reaction.Reaction{ID: reactionID}.Trigger(), EventKey: "release.created:1",
		ProjectID: &project, Plan: buildPlan(), Context: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := db.ListJournal(ctx, JournalQuery{})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]JournalEntry{}
	for _, entry := range entries {
		byKey[entry.DedupKey] = entry
	}
	set := byKey["release.created:1"].Automations
	if len(set) != 1 || set[0].Name != "Vérifier la RC" || set[0].State != PlanRunning {
		t.Errorf("automations = %+v", set)
	}
	if none := byKey["release.created:2"].Automations; len(none) != 0 {
		t.Errorf("an event nothing reacted to: %+v", none)
	}
	if steps := set[0].Steps; len(steps) == 0 {
		t.Errorf("a run comes with its steps: %+v", set[0])
	}

	// A schedule's tick: no event opened it, it is a line of its own.
	if _, _, err := db.CreatePlanRun(ctx, PlanRun{
		Trigger: "schedule:9", EventKey: "schedule:9:2026-11-20T03:00:00Z",
		ProjectID: &project, Plan: buildPlan(), Context: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
	natures := func(query JournalQuery) []string {
		t.Helper()
		entries, err := db.ListJournal(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		var seen []string
		for _, entry := range entries {
			switch entry.Nature {
			case JournalRun:
				if len(entry.Automations) != 1 || entry.Automations[0].Trigger != "schedule:9" || entry.Project == "" {
					t.Errorf("run line = %+v", entry)
				}
				seen = append(seen, "run")
			case JournalEvent:
				seen = append(seen, entry.DedupKey)
			}
		}
		slices.Sort(seen)
		return seen
	}
	if got := natures(JournalQuery{}); !slices.Equal(got, []string{"release.created:1", "release.created:2", "run"}) {
		t.Errorf("everything = %v", got)
	}
	// What automations did: the run, and the event that set one off; not the event nothing reacted to.
	if got := natures(JournalQuery{Nature: JournalRun}); !slices.Equal(got, []string{"release.created:1", "run"}) {
		t.Errorf("automations = %v", got)
	}
	if got := natures(JournalQuery{Nature: JournalEvent}); !slices.Equal(got, []string{"release.created:1", "release.created:2"}) {
		t.Errorf("events = %v", got)
	}
}
