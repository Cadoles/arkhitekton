package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// scheduleStore keeps what is saved, with its next firing.
type scheduleStore struct {
	catalogueStore
	saved []schedule.Schedule
	next  []*time.Time
}

func (s *scheduleStore) Audit(context.Context, string, string, string, map[string]any) error {
	return nil
}

func (s *scheduleStore) ListSchedules(context.Context, int64) ([]store.RecordedSchedule, error) {
	return nil, nil
}

func (s *scheduleStore) SaveSchedule(_ context.Context, entry schedule.Schedule, next *time.Time, _ string) (int64, error) {
	s.saved, s.next = append(s.saved, entry), append(s.next, next)
	return 4, nil
}

func (s *scheduleStore) DeleteSchedule(context.Context, int64, int64) error { return nil }

func saveSchedule(t *testing.T, body string) (*scheduleStore, *httptest.ResponseRecorder) {
	t.Helper()
	fixture := &scheduleStore{}
	handler := NewAutomationsHandler(fixture, roleCatalogue(), testRegistry, quietLogger()).WithSchedules(fixture)
	request := httptest.NewRequest(stdhttp.MethodPost, "/projects/mse/schedules", strings.NewReader(body))
	request.SetPathValue("project", "mse")
	recorder := httptest.NewRecorder()
	handler.SaveSchedule(recorder, request)
	return fixture, recorder
}

const integrationSchedule = `{"name": "Intégration", "cron": "0 8,12,16 * * 1-5", "timezone": "Europe/Paris",
  "ref": "migration-symfony", "enabled": true,
  "plan": {"stages": [{"gestes": [{"kind": "pipeline", "ref": "unit-tests"}]}]}}`

// A schedule saved switched on gets its next firing from now, in its zone; switched off, none, so
// switching it back on does not fire the ticks it slept through.
func TestASavedScheduleFiresNextFromNow(t *testing.T) {
	fixture, recorder := saveSchedule(t, integrationSchedule)
	if recorder.Code != stdhttp.StatusOK || len(fixture.saved) != 1 {
		t.Fatalf("%d: %s", recorder.Code, recorder.Body)
	}
	next := fixture.next[0]
	if next == nil || !next.After(time.Now()) || next.In(mustZone(t, "Europe/Paris")).Minute() != 0 {
		t.Errorf("next = %v", next)
	}
	if saved := fixture.saved[0]; saved.ProjectID != 7 || saved.Ref != "migration-symfony" {
		t.Errorf("saved %+v", saved)
	}

	fixture, _ = saveSchedule(t, strings.Replace(integrationSchedule, `"enabled": true`, `"enabled": false`, 1))
	if fixture.next[0] != nil {
		t.Errorf("a schedule switched off fires at %v", fixture.next[0])
	}
}

func TestAScheduleThatCannotRunIsRefused(t *testing.T) {
	for name, edit := range map[string][2]string{
		"bad cron":      {`"0 8,12,16 * * 1-5"`, `"tous les jours"`},
		"unknown zone":  {`"Europe/Paris"`, `"Europe/Lutece"`},
		"unknown kind":  {`"ref": "unit-tests"`, `"ref": "teleport"`},
		"role on stage": {`{"gestes"`, `{"role": "test", "gestes"`},
	} {
		fixture, recorder := saveSchedule(t, strings.Replace(integrationSchedule, edit[0], edit[1], 1))
		if recorder.Code != stdhttp.StatusBadRequest || len(fixture.saved) != 0 {
			t.Errorf("%s: %d, saved %d", name, recorder.Code, len(fixture.saved))
		}
	}
}

// The list carries the keys the screen reads.
func TestTheScheduleListCarriesWhatTheScreenReads(t *testing.T) {
	next := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(schedulePayload{
		Schedule:   schedule.Schedule{ID: 4, Name: "Intégration", Cron: "0 8 * * *"},
		NextFireAt: &next, LastOutcome: "launched",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(payload, &body)
	for _, key := range []string{"id", "name", "cron", "timezone", "ref", "enabled", "plan", "next_fire_at", "last_outcome"} {
		if _, found := body[key]; !found {
			t.Errorf("key %q missing: %s", key, payload)
		}
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return zone
}
