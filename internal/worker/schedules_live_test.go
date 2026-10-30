package worker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/connect"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// branchForge resolves branches the way a forge would, and records what it was asked.
type branchForge struct {
	provider.Forge
	commits map[string]string
	asked   *[]string
}

func (f branchForge) DefaultBranch(context.Context) (string, error) { return "develop", nil }

func (f branchForge) ResolveCommit(_ context.Context, ref string) (string, error) {
	*f.asked = append(*f.asked, ref)
	commit, found := f.commits[ref]
	if !found {
		return "", errors.New("reference not found")
	}
	return commit, nil
}

type scheduleForges struct {
	forge provider.Forge
	err   error
}

func (f scheduleForges) Forge(context.Context, int64) (provider.Forge, error) { return f.forge, f.err }

type scheduleHarness struct {
	db       *store.DB
	project  int64
	counter  *countingRunner
	runner   *ScheduleRunner
	now      time.Time
	asked    []string
	schedule schedule.Schedule
}

func newScheduleHarness(t *testing.T) *scheduleHarness {
	t.Helper()
	db := throwawayDatabase(t)
	ctx := context.Background()
	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: "acme/shop", Label: "Shop", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	h := &scheduleHarness{db: db, project: projectID, counter: &countingRunner{},
		now: time.Date(2026, 11, 2, 7, 0, 30, 0, time.UTC)}
	forge := branchForge{commits: map[string]string{
		"migration-symfony": "1111111111111111111111111111111111111111",
		"develop":           "2222222222222222222222222222222222222222",
	}, asked: &h.asked}
	h.runner = NewScheduleRunner(db, NewEngine(db, h.counter, quietLogger()), scheduleForges{forge: forge}, quietLogger())
	h.runner.now = func() time.Time { return h.now }

	h.schedule = schedule.Schedule{
		ProjectID: projectID, Name: "Intégration", Cron: "0 8,12,16 * * 1-5", Timezone: "Europe/Paris",
		Ref: "migration-symfony", Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}}}}},
	}
	h.save(t, time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)) // 8 o'clock in Paris
	return h
}

func (h *scheduleHarness) save(t *testing.T, next time.Time) {
	t.Helper()
	id, err := h.db.SaveSchedule(context.Background(), h.schedule, &next, "test")
	if err != nil {
		t.Fatal(err)
	}
	h.schedule.ID = id
}

func (h *scheduleHarness) recorded(t *testing.T) store.RecordedSchedule {
	t.Helper()
	all, err := h.db.ListSchedules(context.Background(), h.project)
	if err != nil || len(all) != 1 {
		t.Fatalf("schedules %v, %v", all, err)
	}
	return all[0]
}

func (h *scheduleHarness) runs(t *testing.T) []store.PlanRunSummary {
	t.Helper()
	runs, err := h.db.ListPlanRuns(context.Background(), h.project, 0)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// A due tick opens one run, on the commit the branch points at, and the schedule moves to its next
// slot; passing again opens nothing.
func TestLiveATickOpensOneRunOnTheResolvedCommit(t *testing.T) {
	h := newScheduleHarness(t)
	ctx := context.Background()

	h.runner.Pass(ctx)
	h.runner.Pass(ctx)

	runs := h.runs(t)
	if len(runs) != 1 || len(h.counter.sent) != 1 {
		t.Fatalf("%d runs, %d steps", len(runs), len(h.counter.sent))
	}
	run := runs[0]
	if run.Trigger != "schedule:"+strconv.FormatInt(h.schedule.ID, 10) || run.EventKey != h.schedule.TickKey(time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("run %+v", run)
	}
	if run.Context["revision"] != "1111111111111111111111111111111111111111" || run.Context["schedule"] != "Intégration" {
		t.Errorf("context %v", run.Context)
	}
	recorded := h.recorded(t)
	if recorded.NextFireAt == nil || !recorded.NextFireAt.Equal(time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("next %v", recorded.NextFireAt)
	}
	if !strings.HasPrefix(recorded.LastOutcome, "launched on 111111111111") {
		t.Errorf("outcome %q", recorded.LastOutcome)
	}
}

// A tick due while the previous run still goes is skipped, and says so.
func TestLiveATickDoesNotOverlapThePreviousRun(t *testing.T) {
	h := newScheduleHarness(t)
	ctx := context.Background()
	// A pipeline step whose job stays queued: the first run stays open.
	h.runner.engine = NewEngine(h.db, enqueuingRunner{db: h.db}, quietLogger())
	h.schedule.Plan = plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "php/static-tests"}}}}}
	h.save(t, time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC))
	h.runner.Pass(ctx)

	h.now = time.Date(2026, 11, 2, 11, 0, 10, 0, time.UTC)
	h.runner.Pass(ctx)

	if runs := h.runs(t); len(runs) != 1 {
		t.Errorf("%d runs, expected the first one only", len(runs))
	}
	recorded := h.recorded(t)
	if recorded.LastOutcome != "skipped: the previous run is still going" ||
		!recorded.NextFireAt.Equal(time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("outcome %q, next %v", recorded.LastOutcome, recorded.NextFireAt)
	}
}

// After an outage, the tick missed is recorded as missed, not run late, and the schedule resumes at
// its next slot after now: no burst.
func TestLiveATickMissedDuringAnOutageIsNotRunLate(t *testing.T) {
	h := newScheduleHarness(t)
	h.now = time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC) // a day and three slots later

	h.runner.Pass(context.Background())

	if runs := h.runs(t); len(runs) != 0 {
		t.Errorf("%d runs after an outage", len(runs))
	}
	recorded := h.recorded(t)
	if !strings.HasPrefix(recorded.LastOutcome, "missed") ||
		!recorded.NextFireAt.Equal(time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("outcome %q, next %v", recorded.LastOutcome, recorded.NextFireAt)
	}
}

// No reference: the default branch. A reference the forge cannot resolve fails the tick rather than
// testing something else. No forge plugin: the reference as written.
func TestLiveTheRevisionAScheduleRuns(t *testing.T) {
	h := newScheduleHarness(t)
	ctx := context.Background()

	h.schedule.Ref = ""
	h.save(t, time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC))
	h.runner.Pass(ctx)
	if runs := h.runs(t); len(runs) != 1 || runs[0].Context["revision"] != "2222222222222222222222222222222222222222" {
		t.Errorf("default branch: %+v", runs)
	}

	h.schedule.Ref = "gone"
	h.save(t, time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC))
	h.now = time.Date(2026, 11, 2, 11, 0, 5, 0, time.UTC)
	forgetRuns(t, h.db)
	h.runner.Pass(ctx)
	if outcome := h.recorded(t).LastOutcome; !strings.Contains(outcome, `resolving "gone"`) || len(h.runs(t)) != 0 {
		t.Errorf("unresolvable ref: %q, %d runs", outcome, len(h.runs(t)))
	}

	h.runner.forges = scheduleForges{err: connect.ErrNotConfigured}
	h.schedule.Ref = "main"
	h.save(t, time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC))
	h.now = time.Date(2026, 11, 2, 15, 0, 5, 0, time.UTC)
	h.runner.Pass(ctx)
	if runs := h.runs(t); len(runs) != 1 || runs[0].Context["revision"] != "main" {
		t.Errorf("no forge: %+v", runs)
	}
}

func forgetRuns(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.Pool().Exec(context.Background(), "delete from plan_runs"); err != nil {
		t.Fatal(err)
	}
}
