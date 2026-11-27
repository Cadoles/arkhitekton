package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cadoles/arkhitekton/internal/auth"
	"github.com/Cadoles/arkhitekton/internal/store"
)

// switchStore records the switches, and knows reaction 3 and no release automation unless told.
type switchStore struct {
	catalogueStore
	declared  bool
	reactions map[int64]bool
	automaton *bool
	audited   []string
}

func (s *switchStore) Audit(_ context.Context, _, action, _ string, _ map[string]any) error {
	s.audited = append(s.audited, action)
	return nil
}

func (s *switchStore) SetReactionEnabled(_ context.Context, _, id int64, enabled bool) error {
	if id != 3 {
		return store.ErrReactionNotFound
	}
	s.reactions[id] = enabled
	return nil
}

func (s *switchStore) SetReleaseAutomationEnabled(_ context.Context, _ int64, enabled bool) error {
	if !s.declared {
		return store.ErrNoAutomation
	}
	s.automaton = &enabled
	return nil
}

func switchRequestTo(handler stdhttp.HandlerFunc, path, body string, values map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(stdhttp.MethodPut, path, strings.NewReader(body))
	request.SetPathValue("project", "shop")
	request = request.WithContext(ContextWithIdentity(request.Context(),
		auth.Identity{Subject: "alice", Username: "alice", Role: auth.RoleAdmin}))
	for key, value := range values {
		request.SetPathValue(key, value)
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// A reaction is switched off by its switch alone: nothing else of it is read nor validated, so
// one whose fact is no longer produced can still be silenced.
func TestAReactionIsSwitchedWithoutBeingSavedAgain(t *testing.T) {
	fixture := &switchStore{reactions: map[int64]bool{}}
	handler := NewAutomationsHandler(fixture, roleCatalogue(), testRegistry, quietLogger())

	recorder := switchRequestTo(handler.SwitchReaction, "/projects/shop/reactions/3/enabled",
		`{"enabled": false}`, map[string]string{"reaction": "3"})
	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("%d: %s", recorder.Code, recorder.Body)
	}
	if enabled, found := fixture.reactions[3]; !found || enabled {
		t.Errorf("reactions = %v, expected 3 switched off", fixture.reactions)
	}
	if len(fixture.audited) != 1 || fixture.audited[0] != "reaction.disable" {
		t.Errorf("audited %v", fixture.audited)
	}

	recorder = switchRequestTo(handler.SwitchReaction, "/projects/shop/reactions/9/enabled",
		`{"enabled": true}`, map[string]string{"reaction": "9"})
	if recorder.Code != stdhttp.StatusNotFound {
		t.Errorf("unknown reaction: %d, expected 404", recorder.Code)
	}
}

// A body without the key is refused: read as false, it would switch off what nobody asked to.
func TestASwitchWithoutItsStateIsRefused(t *testing.T) {
	fixture := &switchStore{reactions: map[int64]bool{}}
	handler := NewAutomationsHandler(fixture, roleCatalogue(), testRegistry, quietLogger())

	for _, body := range []string{`{}`, `{"enabled": "no"}`, ``} {
		recorder := switchRequestTo(handler.SwitchReaction, "/projects/shop/reactions/3/enabled",
			body, map[string]string{"reaction": "3"})
		if recorder.Code != stdhttp.StatusBadRequest {
			t.Errorf("%q: %d, expected 400", body, recorder.Code)
		}
	}
	if len(fixture.reactions) != 0 {
		t.Errorf("switched %v", fixture.reactions)
	}
}

// An automation nobody declared cannot be switched: recording one, even empty, would stop the
// worker's catch-up from composing it.
func TestAnUndeclaredAutomationIsNotSwitched(t *testing.T) {
	fixture := &switchStore{}
	handler := NewAutomationsHandler(fixture, roleCatalogue(), testRegistry, quietLogger())

	recorder := switchRequestTo(handler.SwitchAutomation, "/projects/shop/automation/enabled",
		`{"enabled": false}`, nil)
	if recorder.Code != stdhttp.StatusConflict {
		t.Errorf("%d, expected 409: %s", recorder.Code, recorder.Body)
	}

	fixture.declared = true
	recorder = switchRequestTo(handler.SwitchAutomation, "/projects/shop/automation/enabled",
		`{"enabled": false}`, nil)
	if recorder.Code != stdhttp.StatusOK || fixture.automaton == nil || *fixture.automaton {
		t.Errorf("%d, switched %v", recorder.Code, fixture.automaton)
	}
	if len(fixture.audited) != 1 || fixture.audited[0] != "automation.disable" {
		t.Errorf("audited %v", fixture.audited)
	}
}

// Switched back on, a schedule fires next from now, without the ticks it slept through; switched
// off, it has no next firing.
func TestASwitchedScheduleFiresNextFromNow(t *testing.T) {
	fixture := &scheduleStore{}
	handler := NewAutomationsHandler(fixture, roleCatalogue(), testRegistry, quietLogger()).WithSchedules(fixture)

	recorder := switchRequestTo(handler.SwitchSchedule, "/projects/shop/schedules/4/enabled",
		`{"enabled": true}`, map[string]string{"schedule": "4"})
	if recorder.Code != stdhttp.StatusOK || len(fixture.next) != 1 {
		t.Fatalf("%d: %s", recorder.Code, recorder.Body)
	}
	if next := fixture.next[0]; next == nil || !next.After(time.Now()) {
		t.Errorf("next = %v", next)
	}

	switchRequestTo(handler.SwitchSchedule, "/projects/shop/schedules/4/enabled",
		`{"enabled": false}`, map[string]string{"schedule": "4"})
	if len(fixture.next) != 2 || fixture.next[1] != nil || fixture.switched[1] {
		t.Errorf("switched %v, next %v", fixture.switched, fixture.next)
	}

	recorder = switchRequestTo(handler.SwitchSchedule, "/projects/shop/schedules/8/enabled",
		`{"enabled": true}`, map[string]string{"schedule": "8"})
	if recorder.Code != stdhttp.StatusNotFound {
		t.Errorf("unknown schedule: %d, expected 404", recorder.Code)
	}
}
