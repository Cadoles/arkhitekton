package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Cadoles/arkhitekton/internal/plan"
	"github.com/Cadoles/arkhitekton/internal/store"
)

type fakeJournal struct {
	entries []store.JournalEntry
	query   store.JournalQuery
}

func (f *fakeJournal) ListJournal(_ context.Context, query store.JournalQuery) ([]store.JournalEntry, error) {
	f.query = query
	return f.entries, nil
}

func (f *fakeJournal) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "shop" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 4, Slug: slug}, nil
}

func (f *fakeJournal) Directory(context.Context) (map[string]string, error) {
	return map[string]string{"121": "vcarroy"}, nil
}

func journalCall(t *testing.T, fake *fakeJournal, path string) (int, []journalEntry) {
	t.Helper()
	recorder := httptest.NewRecorder()
	NewJournalHandler(fake, quietLogger()).List(recorder, adminRequest(http.MethodGet, path, ""))
	var body struct {
		Entries []journalEntry `json:"entries"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder.Code, body.Entries
}

// Both natures come back, each with its own fields: an action its actor named, an event the
// automations it set off and, discovered late, its recording date.
func TestTheJournalCarriesBothNatures(t *testing.T) {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	fake := &fakeJournal{entries: []store.JournalEntry{
		{Nature: store.JournalAction, ID: 1, At: at, Actor: "121", Action: store.AuditReleaseCreate, Target: "1.8.0", Project: "shop"},
		{Nature: store.JournalEvent, ID: 2, At: at, RecordedAt: at.Add(time.Hour), Type: "release.created", Subject: "1.8.0",
			Automations: []store.PlanRunSummary{automationRun()}},
		{Nature: store.JournalEvent, ID: 3, At: at, RecordedAt: at, Type: "gitea.push", Pending: true},
	}}
	code, entries := journalCall(t, fake, "/admin/journal?nature=event&project=shop&q=1.8&limit=50")
	if code != http.StatusOK || len(entries) != 3 {
		t.Fatalf("code %d, entries %+v", code, entries)
	}
	if fake.query.Nature != store.JournalEvent || fake.query.ProjectID == nil || *fake.query.ProjectID != 4 ||
		fake.query.Search != "1.8" || fake.query.Limit != 50 {
		t.Errorf("query = %+v", fake.query)
	}
	if entries[0].Actor != "vcarroy" || entries[0].Action != store.AuditReleaseCreate {
		t.Errorf("action = %+v", entries[0])
	}
	if event := entries[1]; event.RecordedAt == nil || len(event.Automations) != 1 || event.Automations[0].Name != "Vérifier" {
		t.Errorf("event = %+v", event)
	}
	// The journal is read by administrators: a step's detail is shown, not withheld.
	if steps := entries[1].Automations[0].Stages[0].Steps; len(steps) != 1 || steps[0].Detail != "HTTP 502" ||
		steps[0].Label != "Prévenir" {
		t.Errorf("steps = %+v", steps)
	}
	if received := entries[2]; received.RecordedAt != nil || !received.Pending {
		t.Errorf("an event recorded when it happened, not routed yet = %+v", received)
	}
}

func automationRun() store.PlanRunSummary {
	return store.PlanRunSummary{
		PlanRun: store.PlanRun{ID: 7, Trigger: "reaction:3", EventKey: "release:1.8.0", State: "failed",
			Plan: plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: "action", Ref: "webhook", Label: "Prévenir"}}}}}},
		Name:  "Vérifier",
		Steps: []store.PlanStep{{Stage: 0, Position: 0, Kind: "action", Ref: "webhook", State: "failed", Detail: "HTTP 502"}},
	}
}

// A schedule's tick, which no event opened, is a line of its own; the nature selects it.
func TestTheJournalCarriesTheRunsNoEventOpened(t *testing.T) {
	run := automationRun()
	run.Trigger, run.Name = "schedule:2", "Nuit"
	fake := &fakeJournal{entries: []store.JournalEntry{
		{Nature: store.JournalRun, ID: 7, At: time.Now(), Automations: []store.PlanRunSummary{run}},
	}}
	code, entries := journalCall(t, fake, "/admin/journal?nature=automation")
	if code != http.StatusOK || fake.query.Nature != store.JournalRun {
		t.Fatalf("code %d, query %+v", code, fake.query)
	}
	if len(entries) != 1 || entries[0].Nature != "automation" || len(entries[0].Automations) != 1 ||
		entries[0].Automations[0].Name != "Nuit" || entries[0].Automations[0].Trigger != "schedule:2" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestTheJournalRefusesWhatItCannotRead(t *testing.T) {
	if code, _ := journalCall(t, &fakeJournal{}, "/admin/journal?nature=other"); code != http.StatusBadRequest {
		t.Errorf("unknown nature: %d", code)
	}
	if code, _ := journalCall(t, &fakeJournal{}, "/admin/journal?project=nowhere"); code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
}
