package worker

import (
	"context"
	"sync"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * An in-memory plan store.
 *
 * It exists so that the judge of the verification chain — `verification_test.go` — keeps
 * running **without a database**. It is no convenience: those tests bear on what is queued and
 * on the idempotence of the chaining, two things one wants to be able to exercise in a few
 * milliseconds, over every combination, without standing up PostgreSQL.
 *
 * It reproduces the two properties the engine depends on, and nothing else: uniqueness per
 * (trigger, fact), and conclusion conditioned on the `running` state. A fake store that relaxed
 * them would make the tests greener than the code.
 */
type fakePlanStore struct {
	mu    sync.Mutex
	runs  map[int64]*store.PlanRun
	steps map[int64][]store.PlanStep
	next  int64
	// byEvent holds the uniqueness the database carries by index.
	byEvent map[string]int64
}

func newFakePlanStore() *fakePlanStore {
	return &fakePlanStore{
		runs: map[int64]*store.PlanRun{}, steps: map[int64][]store.PlanStep{},
		byEvent: map[string]int64{}, next: 1,
	}
}

func (f *fakePlanStore) CreatePlanRun(_ context.Context, run store.PlanRun) (store.PlanRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := run.Trigger + "\x00" + run.EventKey
	if id, known := f.byEvent[key]; known {
		return *f.runs[id], false, nil
	}

	id := f.next
	f.next++
	run.ID = id
	run.State = store.PlanRunning
	f.runs[id] = &run
	f.byEvent[key] = id

	for stage, step := range run.Plan.Stages {
		for position, geste := range step.Gestes {
			f.steps[id] = append(f.steps[id], store.PlanStep{
				Stage: stage, Position: position,
				Kind: string(geste.Kind), Ref: geste.Ref, State: "pending",
			})
		}
	}
	return run, true, nil
}

func (f *fakePlanStore) PlanRunByID(_ context.Context, id int64) (store.PlanRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	run, found := f.runs[id]
	if !found {
		return store.PlanRun{}, store.ErrPlanRunNotFound
	}
	return *run, nil
}

func (f *fakePlanStore) PlanSteps(_ context.Context, runID int64) ([]store.PlanStep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]store.PlanStep(nil), f.steps[runID]...), nil
}

func (f *fakePlanStore) MarkPlanStep(
	_ context.Context, runID int64, stage, position int,
	state string, job *int64, detail string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for index, step := range f.steps[runID] {
		if step.Stage != stage || step.Position != position {
			continue
		}
		f.steps[runID][index].State = state
		f.steps[runID][index].Detail = detail
		if job != nil {
			f.steps[runID][index].JobID = job
		}
		return nil
	}
	return store.ErrPlanRunNotFound
}

func (f *fakePlanStore) PlanStepForJob(_ context.Context, jobID int64) (int64, store.PlanStep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for runID, steps := range f.steps {
		for _, step := range steps {
			if step.JobID != nil && *step.JobID == jobID {
				return runID, step, nil
			}
		}
	}
	return 0, store.PlanStep{}, store.ErrPlanRunNotFound
}

func (f *fakePlanStore) FinishPlanRun(_ context.Context, runID int64, state string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	run, found := f.runs[runID]
	if !found || run.State != store.PlanRunning {
		// The same conditional write as the database: two concurrent conclusions produce only one.
		return false, nil
	}
	run.State = state
	return true, nil
}
