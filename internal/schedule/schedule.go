package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

// Schedule runs a plan at the times a cron expression names.
//
// Declared per project, like a reaction, and carrying its plan: a clock firing a fact for a
// reaction to hear would make two things to write for one wish.
type Schedule struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"-"`
	Name      string `json:"name"`
	Cron      string `json:"cron"`
	// Timezone is the zone the expression is read in: `Europe/Paris`. Empty, UTC.
	Timezone string `json:"timezone"`
	// Ref is the branch or tag the plan's pipelines take as their revision, resolved to its commit
	// when the schedule fires. Empty, the repository's default branch.
	Ref     string    `json:"ref"`
	Enabled bool      `json:"enabled"`
	Plan    plan.Plan `json:"plan"`
}

// ErrInvalid reports a schedule we refuse to save.
var ErrInvalid = errors.New("invalid schedule")

const maxNameLength = 120

// Validate refuses a schedule that could not fire as it reads.
func (s Schedule) Validate() error {
	var problems []error
	if name := strings.TrimSpace(s.Name); name == "" {
		problems = append(problems, fmt.Errorf("%w: a name is required", ErrInvalid))
	} else if len(name) > maxNameLength {
		problems = append(problems, fmt.Errorf("%w: name of %d characters, bounded to %d",
			ErrInvalid, len(name), maxNameLength))
	}
	if _, err := s.Location(); err != nil {
		problems = append(problems, fmt.Errorf("%w: %v", ErrInvalid, err))
	}
	if cron, err := ParseCron(s.Cron); err != nil {
		problems = append(problems, fmt.Errorf("%w: %v", ErrInvalid, err))
	} else if _, err := cron.Next(time.Now(), time.UTC); err != nil {
		problems = append(problems, fmt.Errorf("%w: %v", ErrInvalid, err))
	}
	if s.Plan.Empty() {
		problems = append(problems, fmt.Errorf("%w: the plan runs nothing", ErrInvalid))
	} else if err := s.Plan.Validate(); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

// Location returns the zone the expression is read in.
func (s Schedule) Location() (*time.Location, error) {
	if strings.TrimSpace(s.Timezone) == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil, fmt.Errorf("time zone %q unknown", s.Timezone)
	}
	return location, nil
}

// Next returns the schedule's first firing strictly after `after`.
func (s Schedule) Next(after time.Time) (time.Time, error) {
	cron, err := ParseCron(s.Cron)
	if err != nil {
		return time.Time{}, err
	}
	location, err := s.Location()
	if err != nil {
		return time.Time{}, err
	}
	return cron.Next(after, location)
}

// Refs lists what the plan's steps point at, by kind.
func (s Schedule) Refs() (pipelines, actions []string) {
	return plan.Refs(s.Plan)
}

// Trigger names the trigger of a run this schedule opens: `schedule:<id>`, beside a reaction's
// `reaction:<id>`.
func (s Schedule) Trigger() string {
	return fmt.Sprintf("schedule:%d", s.ID)
}

// TickKey identifies one firing. A plan run is unique per trigger and key, so a tick two workers
// both see, or one worker sees again after a restart, opens one run.
func (s Schedule) TickKey(tick time.Time) string {
	return fmt.Sprintf("schedule:%d:%s", s.ID, tick.UTC().Format(time.RFC3339))
}
