package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

/*
 * The queue, one per job type.
 *
 * The previous model grouped the jobs into exclusion classes: `parallel` excluded nothing,
 * `exclusive-<name>` excluded its own class, `exclusive-cluster` excluded everything. Three
 * regimes for a single question — how many of these runs can run together — and an answer one had
 * to reconstruct by looking for which types shared a class.
 *
 * These tests describe the model that replaced it: one cap per type, one global cap above it,
 * nothing else.
 */

func naming(kind string, id int64) string { return fmt.Sprintf("%s-run-%d", kind, id) }

func enqueue(t *testing.T, db *DB, kind string, priority int) int64 {
	t.Helper()

	id, err := db.Enqueue(context.Background(), JobRequest{
		Kind:        kind,
		Priority:    priority,
		Params:      map[string]string{"revision": "abc"},
		RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("queueing %s: %v", kind, err)
	}
	return id
}

// caps composes ceilings: the named ones, and a wide default for the rest.
func caps(global int, perKind map[string]int) Limits {
	if perKind == nil {
		perKind = map[string]int{}
	}
	return Limits{PerKind: perKind, Default: 5, Global: global}
}

func claim(t *testing.T, db *DB, limits Limits) (Job, error) {
	t.Helper()
	return db.ClaimNext(context.Background(), limits, naming)
}

func TestClaimServesByPriorityThenArrival(t *testing.T) {
	db := migrated(t)

	enqueue(t, db, "build", 100)
	urgent := enqueue(t, db, "promote", 10)
	enqueue(t, db, "audit", 100)

	job, err := claim(t, db, caps(4, nil))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if job.ID != urgent {
		t.Errorf("job claimed = %d, expected the highest priority %d", job.ID, urgent)
	}
	if job.RunName != naming("promote", urgent) {
		t.Errorf("run name = %q", job.RunName)
	}
	if job.State != JobRunning {
		t.Errorf("state = %q", job.State)
	}
}

// A full queue sets aside only its own kind. That is the model's central rule: a
// security audit must be able to run during end-to-end tests, two end-to-end campaigns
// not.
func TestAFullQueueSetsAsideOnlyItsOwnKind(t *testing.T) {
	db := migrated(t)

	limits := caps(4, map[string]int{"e2e": 1})

	enqueue(t, db, "e2e", 100)
	enqueue(t, db, "e2e", 100)
	enqueue(t, db, "audit", 100)

	first, err := claim(t, db, limits)
	if err != nil {
		t.Fatalf("premier: %v", err)
	}
	if first.Kind != "e2e" {
		t.Fatalf("premier job = %q", first.Kind)
	}

	second, err := claim(t, db, limits)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// The second end-to-end run is skipped, the audit goes through. A job whose queue is
	// full must not block those behind it — otherwise an integration run would hold every
	// unit test behind it.
	if second.Kind != "audit" {
		t.Errorf("second job = %q, expected the audit: a full queue blocks beyond itself", second.Kind)
	}

	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("the second end-to-end run should not have been served: %v", err)
	}
}

// A ceiling above one allows several runs of the same kind, and stops at its value.
// That is what the previous model could not express: a "parallel" class was bounded
// only by the global ceiling.
func TestAQueueServesUpToItsCeiling(t *testing.T) {
	db := migrated(t)

	limits := caps(10, map[string]int{"unit-tests": 2})

	for range 3 {
		enqueue(t, db, "unit-tests", 100)
	}

	for i := range 2 {
		if _, err := claim(t, db, limits); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}

	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("the ceiling of 2 was not honoured: %v", err)
	}
}

// A kind absent from the ceilings — a local task, or a kind retired from the catalogue
// since the job was queued — takes the default. Making it wait for a setting that will
// never exist would immobilise it for ever.
func TestAnUnknownKindTakesTheDefault(t *testing.T) {
	db := migrated(t)

	limits := Limits{PerKind: map[string]int{"build": 5}, Default: 1, Global: 10}

	enqueue(t, db, "delivery-note", 100)
	enqueue(t, db, "delivery-note", 100)

	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("premier: %v", err)
	}
	// Default of 1: the second one waits, which proves the default really applies rather
	// than there being no limit.
	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("the default was not applied to the unknown kind: %v", err)
	}
}

// The global ceiling prevails: it protects the cluster, and no queue may exceed it even
// when its own has room.
func TestTheGlobalCeilingPrevailsOverTheQueues(t *testing.T) {
	db := migrated(t)

	limits := caps(2, map[string]int{"build": 5, "audit": 5})

	enqueue(t, db, "build", 100)
	enqueue(t, db, "build", 100)
	enqueue(t, db, "audit", 100)

	for i := range 2 {
		if _, err := claim(t, db, limits); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}

	if _, err := claim(t, db, limits); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("the global ceiling of 2 was not honoured: %v", err)
	}
}

// Observed exclusivity is no longer a held barrier but a queue of one place. It is
// recorded at claim time because that is the only instant it exists, and it serves to
// judge whether a load measurement is comparable.
func TestAQueueOfOnePlaceIsObservedAsExclusive(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	limits := caps(10, map[string]int{"load-test": 1, "build": 5})

	seul := enqueue(t, db, "load-test", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("claim: %v", err)
	}

	exclusive, concurrent, err := db.StartConditions(ctx, seul)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if !exclusive {
		t.Error("a queue of one place must be observed as exclusive")
	}
	// Nothing was running: the measurement is comparable. It is that second condition that
	// now carries the weight, the barrier having gone.
	if concurrent != 0 {
		t.Errorf("%d concurrent run(s), expected 0", concurrent)
	}

	partage := enqueue(t, db, "build", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("second claim: %v", err)
	}
	exclusive, concurrent, err = db.StartConditions(ctx, partage)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if exclusive {
		t.Error("a queue of five places is not exclusive")
	}
	if concurrent != 1 {
		t.Errorf("%d concurrent run(s), expected 1", concurrent)
	}
}

// The run's name is decided before creation in the cluster. If that fails, the job must
// return to the queue with one more attempt — and without keeping a name that names
// nothing.
func TestRequeueClearsTheRunNameAndCountsTheAttempt(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	again, err := db.Requeue(ctx, id, "creation refused by the cluster")
	if err != nil {
		t.Fatalf("remise en file: %v", err)
	}
	if !again {
		t.Error("a first retry must be allowed")
	}

	job, err := db.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.State != JobQueued {
		t.Errorf("state = %q, expected queued", job.State)
	}
	if job.Attempt != 2 {
		t.Errorf("attempts = %d, expected 2", job.Attempt)
	}
	if job.RunName != "" {
		t.Errorf("run name kept after failure: %q", job.RunName)
	}
}

// A queue that does not explain its waiting produces needless retries.
func TestExplainQueueNamesTheFullQueue(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	limits := caps(4, map[string]int{"e2e": 1})

	enqueue(t, db, "e2e", 100)
	blocked := enqueue(t, db, "e2e", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := db.ExplainQueue(ctx, limits); err != nil {
		t.Fatalf("explication: %v", err)
	}

	job, err := db.GetJob(ctx, blocked)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.QueueReason != ReasonKindBusy {
		t.Errorf("reason = %q, expected %q", job.QueueReason, ReasonKindBusy)
	}
}

// The global ceiling prevails in the explanation as in the service: telling a job held
// by the global ceiling that "the queue is full" would send someone looking for room in
// the wrong place, and adjusting the queue would change nothing.
func TestExplainQueuePrefersTheGlobalCeiling(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	limits := caps(1, map[string]int{"audit": 1, "build": 5})

	enqueue(t, db, "audit", 100)
	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("claim: %v", err)
	}
	blocked := enqueue(t, db, "build", 100)

	if err := db.ExplainQueue(ctx, limits); err != nil {
		t.Fatalf("explication: %v", err)
	}

	job, err := db.GetJob(ctx, blocked)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.QueueReason != ReasonCapacity {
		t.Errorf("reason = %q, expected %q", job.QueueReason, ReasonCapacity)
	}
}

// A setting replaces the catalogue's ceiling, and removing it gives it back.
func TestASettingReplacesAndIsRemoved(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.SetQueueLimit(ctx, "unit-tests", 2, "vcarroy"); err != nil {
		t.Fatalf("setting: %v", err)
	}
	// The same kind set twice does not create two rows: it is a setting, not a log.
	if err := db.SetQueueLimit(ctx, "unit-tests", 3, "vcarroy"); err != nil {
		t.Fatalf("second setting: %v", err)
	}

	limits, err := db.QueueLimits(ctx)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(limits) != 1 || limits["unit-tests"] != 3 {
		t.Errorf("settings = %v, expected unit-tests at 3", limits)
	}

	// A zero or negative ceiling is refused: it would immobilise the queue for ever, with
	// nothing saying so.
	if err := db.SetQueueLimit(ctx, "unit-tests", 0, "vcarroy"); err == nil {
		t.Error("a zero ceiling should be refused")
	}

	if err := db.ClearQueueLimit(ctx, "unit-tests"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	limits, _ = db.QueueLimits(ctx)
	if len(limits) != 0 {
		t.Errorf("settings after removal = %v", limits)
	}
}

// Occupancy is counted in the database: a count made over a truncated list would be
// wrong, and it is the number of places taken that decides the waiting.
func TestOccupancyIsCountedPerKind(t *testing.T) {
	db := migrated(t)

	limits := caps(10, map[string]int{"build": 1})

	enqueue(t, db, "build", 100)
	enqueue(t, db, "build", 100)
	enqueue(t, db, "audit", 100)

	if _, err := claim(t, db, limits); err != nil {
		t.Fatalf("claim: %v", err)
	}

	counts, err := db.QueueOccupancy(context.Background())
	if err != nil {
		t.Fatalf("occupation: %v", err)
	}
	if counts["build"].Running != 1 || counts["build"].Queued != 1 {
		t.Errorf("build = %+v, expected 1 running and 1 waiting", counts["build"])
	}
	if counts["audit"].Running != 0 || counts["audit"].Queued != 1 {
		t.Errorf("audit = %+v, expected 1 waiting", counts["audit"])
	}
}

func TestCancelQueuedJobOnlyTouchesQueuedOnes(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)

	cancelled, err := db.CancelQueuedJob(ctx, id, "demande utilisateur")
	if err != nil {
		t.Fatalf("annulation: %v", err)
	}
	if !cancelled {
		t.Fatal("a queued job must be cancellable")
	}

	running := enqueue(t, db, "audit", 100)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A running job is not cancelled in the database: it has to go through the cluster, and
	// the caller must be able to tell the difference.
	cancelled, err = db.CancelQueuedJob(ctx, running, "demande utilisateur")
	if err != nil {
		t.Fatalf("annulation: %v", err)
	}
	if cancelled {
		t.Error("a running job must not be cancelled this way")
	}
}

// The order displayed must be the order of service: a queue displayed otherwise makes
// people wait for the wrong job.
func TestListQueueShowsRunningFirstThenServiceOrder(t *testing.T) {
	db := migrated(t)

	enqueue(t, db, "build", 100)
	enqueue(t, db, "promote", 10)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	enqueue(t, db, "audit", 50)

	jobs, err := db.ListQueue(context.Background())
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("%d job(s), expected 3", len(jobs))
	}

	if jobs[0].State != JobRunning || jobs[0].Kind != "promote" {
		t.Errorf("first = %s/%s, expected the running job", jobs[0].Kind, jobs[0].State)
	}
	if jobs[1].Kind != "audit" || jobs[2].Kind != "build" {
		t.Errorf("waiting order = %s then %s, expected audit then build", jobs[1].Kind, jobs[2].Kind)
	}
}

func TestFinishJobIsIdempotentOnTerminalJobs(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)
	if _, err := claim(t, db, caps(4, nil)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := db.FinishJob(ctx, id, JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	// A second informer event must not rewrite the outcome.
	if err := db.FinishJob(ctx, id, JobFailed, "trop tard"); err != nil {
		t.Fatalf("seconde conclusion: %v", err)
	}

	job, err := db.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.State != JobSucceeded {
		t.Errorf("state = %q, expected succeeded: a terminal outcome was rewritten", job.State)
	}
}

// An unsatisfiable job must not be retried indefinitely: it would immobilise the queue,
// the scheduler always picking the oldest before considering the others. Observed in
// real conditions at two hundred and thirty attempts.
func TestARetryStopsAtTheCeiling(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)

	// `attempt` counts the current attempt, not the retries: a queued job is already at one
	// attempt. The ceiling therefore bounds the total, and the loop must stop on its own.
	reprises := 0
	for reprises < 50 {
		if _, err := claim(t, db, caps(4, nil)); err != nil {
			t.Fatalf("claim: %v", err)
		}
		again, err := db.Requeue(ctx, id, "a cause that will not go away")
		if err != nil {
			t.Fatalf("reprise %d: %v", reprises, err)
		}
		reprises++
		if !again {
			break
		}
	}

	if reprises >= 50 {
		t.Fatal("the retry never stops: the queue would stay immobilised")
	}

	job, err := db.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if job.Attempt != MaxJobAttempts {
		t.Errorf("%d attempts in total, expected the ceiling %d", job.Attempt, MaxJobAttempts)
	}
}

// A job that is no longer `running` — cancelled meanwhile — is not a retry error: there
// is simply nothing left to retry.
func TestARetryOnANonRunningJobIsNotAnError(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := enqueue(t, db, "build", 100)

	again, err := db.Requeue(ctx, id, "never started")
	if err != nil {
		t.Fatalf("remise en file: %v", err)
	}
	if again {
		t.Error("no retry should be announced on a job that is not running")
	}
}
