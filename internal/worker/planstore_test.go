package worker

import (
	"context"
	"sync"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Un magasin de plans en mémoire.
 *
 * Il existe pour que le juge de la chaîne de vérification — `verification_test.go`
 * — continue de s'exécuter **sans base**. Ce n'est pas une commodité : ces tests
 * portent sur ce qui est enfilé et sur l'idempotence de l'enchaînement, deux
 * choses qu'on veut pouvoir éprouver en quelques millisecondes, sur toutes les
 * combinaisons, sans monter PostgreSQL.
 *
 * Il reproduit les deux propriétés dont le moteur dépend, et rien d'autre :
 * l'unicité par (déclencheur, fait), et la conclusion conditionnée à l'état
 * `running`. Un faux magasin qui les relâcherait rendrait les tests plus verts
 * que le code.
 */
type fakePlanStore struct {
	mu    sync.Mutex
	runs  map[int64]*store.PlanRun
	steps map[int64][]store.PlanStep
	next  int64
	// byEvent tient l'unicité que la base porte par index.
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
		// La même écriture conditionnelle que la base : deux conclusions
		// concurrentes n'en produisent qu'une.
		return false, nil
	}
	run.State = state
	return true, nil
}
