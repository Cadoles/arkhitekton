package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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
			Automations: []store.JournalAutomation{{Trigger: "reaction:3", Name: "Vérifier", State: "succeeded"}}},
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
	if received := entries[2]; received.RecordedAt != nil || !received.Pending {
		t.Errorf("an event recorded when it happened, not routed yet = %+v", received)
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
