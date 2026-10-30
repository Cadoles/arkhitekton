package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/connect"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * The schedules: plans a clock opens.
 *
 * What they replace are CronJobs creating PipelineRuns directly, outside the queue: no ceiling, no
 * reading, nothing on screen. A schedule opens a plan, and its pipelines go through the queue.
 *
 * Three rules, held here:
 *
 *  1. **A tick opens one run.** The schedule is moved past its tick by a conditional write before
 *     anything starts, and the run is keyed on the tick: a worker restarting, or a second one taking
 *     the lease, does not open it twice.
 *  2. **No overlap.** A tick is skipped while the schedule's previous run is still going, and says
 *     so. The pipelines have their timeouts: a run stuck for good is not what a schedule must work
 *     around.
 *  3. **No burst after an outage.** The next tick is computed from now, never from the tick
 *     missed, and a tick older than the grace period is recorded as missed rather than run late. An
 *     integration campaign started three hours after its slot would land on someone's working day.
 */

// scheduleInterval spaces two passes: a schedule names minutes, and fires within this of its minute.
const scheduleInterval = 20 * time.Second

// scheduleGrace is how late a tick may still fire: a worker restarting or a lease changing hands
// takes well under it, an outage takes more.
const scheduleGrace = 15 * time.Minute

// ScheduleStore is what firing the schedules needs from the database.
type ScheduleStore interface {
	DueSchedules(ctx context.Context, now time.Time) ([]store.RecordedSchedule, error)
	ClaimTick(ctx context.Context, id int64, tick, next time.Time, outcome string) (bool, error)
	RecordTickOutcome(ctx context.Context, id int64, outcome string) error
	PlanRunInProgress(ctx context.Context, trigger string) (bool, error)
}

// ForgeSource connects to a project's forge, to resolve what a schedule names into a commit.
type ForgeSource interface {
	Forge(ctx context.Context, projectID int64) (provider.Forge, error)
}

// ScheduleRunner fires the schedules that are due.
type ScheduleRunner struct {
	store  ScheduleStore
	engine *Engine
	forges ForgeSource
	logger *slog.Logger
	now    func() time.Time
}

func NewScheduleRunner(schedules ScheduleStore, engine *Engine, forges ForgeSource, logger *slog.Logger) *ScheduleRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ScheduleRunner{store: schedules, engine: engine, forges: forges, logger: logger, now: time.Now}
}

// Run fires the schedules until cancellation.
func (r *ScheduleRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(scheduleInterval)
	defer ticker.Stop()
	for {
		r.Pass(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Pass fires what is due now.
func (r *ScheduleRunner) Pass(ctx context.Context) {
	now := r.now()
	due, err := r.store.DueSchedules(ctx, now)
	if err != nil {
		r.logger.Error("schedules due", "error", err)
		return
	}
	for _, entry := range due {
		r.fire(ctx, entry, now)
	}
}

func (r *ScheduleRunner) fire(ctx context.Context, entry store.RecordedSchedule, now time.Time) {
	tick := *entry.NextFireAt
	next, err := entry.Next(now)
	if err != nil {
		// Validated when saved: only a zone database or an expression changed under it gets here.
		r.logger.Error("next firing", "schedule", entry.Name, "error", err)
		return
	}

	missed := now.Sub(tick) > scheduleGrace
	outcome := "launched"
	if missed {
		outcome = fmt.Sprintf("missed: the worker was not running at %s", tick.UTC().Format(time.RFC3339))
	}
	claimed, err := r.store.ClaimTick(ctx, entry.ID, tick, next, outcome)
	if err != nil || !claimed {
		if err != nil {
			r.logger.Error("claiming a tick", "schedule", entry.Name, "error", err)
		}
		return
	}
	if missed {
		r.logger.Warn("tick missed, not run late", "schedule", entry.Name, "project", entry.ProjectID,
			"tick", tick, "next", next)
		return
	}

	outcome = r.launch(ctx, entry, tick)
	if err := r.store.RecordTickOutcome(ctx, entry.ID, outcome); err != nil {
		r.logger.Error("recording a tick", "schedule", entry.Name, "error", err)
	}
	r.logger.Info("schedule fired", "schedule", entry.Name, "project", entry.ProjectID,
		"tick", tick, "outcome", outcome, "next", next)
}

// launch opens the tick's run, and says what came of it.
func (r *ScheduleRunner) launch(ctx context.Context, entry store.RecordedSchedule, tick time.Time) string {
	running, err := r.store.PlanRunInProgress(ctx, entry.Trigger())
	if err != nil {
		return "failed: " + err.Error()
	}
	if running {
		return "skipped: the previous run is still going"
	}

	revision, err := r.revision(ctx, entry)
	if err != nil {
		return "failed: " + err.Error()
	}
	scope := map[string]string{"schedule": entry.Name, "tick": tick.UTC().Format(time.RFC3339)}
	if revision != "" {
		scope[contextRevision] = revision
	}
	projectID := entry.ProjectID
	started := r.engine.Trigger(ctx, store.PlanRun{
		Trigger: entry.Trigger(), EventKey: entry.TickKey(tick), ProjectID: &projectID,
		Plan: entry.Plan, Context: scope,
	})
	if started == 0 {
		return "failed: no step started, the log says why"
	}
	if revision != "" {
		return fmt.Sprintf("launched on %s", shortCommit(revision))
	}
	return "launched"
}

// revision resolves what the schedule names into the commit its pipelines build: a run tested on a
// branch says which commit it tested. A project with no forge plugin takes the reference as written,
// and a reference the forge cannot resolve fails the tick rather than testing something else.
func (r *ScheduleRunner) revision(ctx context.Context, entry store.RecordedSchedule) (string, error) {
	ref := strings.TrimSpace(entry.Ref)
	if r.forges == nil {
		return ref, nil
	}
	forge, err := r.forges.Forge(ctx, entry.ProjectID)
	if errors.Is(err, connect.ErrNotConfigured) || errors.Is(err, provider.ErrNotConfigured) {
		return ref, nil
	}
	if err != nil {
		return "", fmt.Errorf("the project's forge: %w", err)
	}
	if ref == "" {
		if ref, err = forge.DefaultBranch(ctx); err != nil {
			return "", fmt.Errorf("default branch: %w", err)
		}
	}
	commit, err := forge.ResolveCommit(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", ref, err)
	}
	return commit, nil
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
