package schedule

import (
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

func valid() Schedule {
	return Schedule{
		ID: 3, Name: "Intégration trois fois par jour", Cron: "0 8,12,16 * * 1-5", Timezone: "Europe/Paris",
		Ref: "migration-symfony", Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "mse/integration-tests"}}}}},
	}
}

func TestAScheduleThatCannotFireIsRefused(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid schedule refused: %v", err)
	}
	for name, breakIt := range map[string]func(*Schedule){
		"no name":      func(s *Schedule) { s.Name = " " },
		"long name":    func(s *Schedule) { s.Name = strings.Repeat("a", 121) },
		"bad cron":     func(s *Schedule) { s.Cron = "every day" },
		"never fires":  func(s *Schedule) { s.Cron = "0 0 31 2 *" },
		"unknown zone": func(s *Schedule) { s.Timezone = "Europe/Lutece" },
		"empty plan":   func(s *Schedule) { s.Plan = plan.Plan{} },
		"invalid plan": func(s *Schedule) { s.Plan.Stages[0].Gestes[0].Kind = "teleport" },
	} {
		schedule := valid()
		breakIt(&schedule)
		if err := schedule.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The expression is read in the schedule's zone: 8 o'clock in Paris is 7 o'clock UTC in winter.
func TestAScheduleFiresInItsZone(t *testing.T) {
	next, err := valid().Next(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if err != nil || !next.Equal(time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("next = %v, %v", next, err)
	}
	utc := valid()
	utc.Timezone = ""
	if next, _ := utc.Next(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)); next.Hour() != 8 {
		t.Errorf("no zone is UTC: %v", next)
	}
}

func TestATickIsKeyedOnTheScheduleAndTheInstant(t *testing.T) {
	tick := time.Date(2026, 11, 2, 8, 0, 0, 0, paris)
	if key := valid().TickKey(tick); key != "schedule:3:2026-11-02T07:00:00Z" {
		t.Errorf("key = %q", key)
	}
	if trigger := valid().Trigger(); trigger != "schedule:3" {
		t.Errorf("trigger = %q", trigger)
	}
}
