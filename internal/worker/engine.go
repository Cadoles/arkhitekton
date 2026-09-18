package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The engine that runs the plans.
 *
 * # Driven by signals, not by a clock
 *
 * A loop consuming the fact feed at intervals was imagined. That is not what is written here,
 * and the gap deserves a reason.
 *
 * The observer already delivers **exactly** the two signals the engine needs: a build
 * concluded, and a job finished. A loop at intervals would add a cursor to keep, a latency, and
 * a second source of truth about what has been handled — in order to rediscover facts the
 * observer has just passed it.
 *
 * The engine is therefore a **component**, called at the two places where something happens.
 * The day facts come from probes rather than from the observer, it is the probe that will call
 * it, through the same entry point.
 *
 * # Two entry points, and nothing else
 *
 *   - `Trigger` opens a run on a fact, and starts its first stage.
 *   - `Advance` concludes a step on the end of its job, and starts what follows.
 *
 * Both are **callable again without effect**: `CreatePlanRun` returns the existing run rather
 * than opening a second one, and `plan.Next` offers only what is still waiting. That is what
 * makes the engine tolerant of the redelivery of finished runs, which is the rule and not the
 * exception.
 */

// PlanStore is what the engine needs from the database.
//
// A narrow interface rather than the whole store: the engine only opens, reads and marks, and
// giving it the rest would invite it to decide things that are not its own — a release's
// state, in particular, which the plan never writes.
type PlanStore interface {
	CreatePlanRun(ctx context.Context, run store.PlanRun) (store.PlanRun, bool, error)
	PlanRunByID(ctx context.Context, id int64) (store.PlanRun, error)
	PlanSteps(ctx context.Context, runID int64) ([]store.PlanStep, error)
	MarkPlanStep(ctx context.Context, runID int64, stage, position int,
		state string, job *int64, detail string) error
	PlanStepForJob(ctx context.Context, jobID int64) (int64, store.PlanStep, error)
	FinishPlanRun(ctx context.Context, runID int64, state string) (bool, error)
}

// ArtifactStore is what the engine needs in order to carry the artifacts.
//
// Separate from [PlanStore] and **optional**: an engine without it runs plans exactly as
// before, and that is what lets the model's tests exercise a chain without standing up a
// database.
type ArtifactStore interface {
	// RunResults returns what a job's pipeline returned as results.
	RunResults(ctx context.Context, jobID int64) (map[string]string, error)
	RecordPlanArtifacts(ctx context.Context, runID int64,
		releaseID *int64, artifacts []store.PlanArtifact) error
	PlanArtifacts(ctx context.Context, runID int64) ([]store.PlanArtifact, error)
}

// GesteRunner carries out a step.
//
// Extracted into an interface because the two kinds of step have nothing in common: a
// pipeline becomes a job in the queue, an action leaves the cluster. The engine has no
// business knowing the difference — it asks, and keeps what it is told.
type GesteRunner interface {
	// Run carries the step out. Returns the job identifier for a pipeline step, nil for an
	// action; and a reason when it did not start.
	Run(ctx context.Context, run store.PlanRun, geste plan.Geste) (*int64, error)
}

// Engine advances the plan runs.
type Engine struct {
	store  PlanStore
	runner GesteRunner
	// stages is told when a stage with a role settles. Nil: plans run without anything outside
	// depending on them.
	stages StageSink
	// artifacts carries what the steps produce. Nil: no artifact is kept, and `{artifacts.…}`
	// stays literal in the steps that follow.
	artifacts ArtifactStore
	// products says what a catalogue kind leaves behind it.
	products func(ref string) string
	logger   *slog.Logger
}

func NewEngine(plans PlanStore, runner GesteRunner, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: plans, runner: runner, logger: logger}
}

// Trigger opens a run on a fact, and starts what can start.
//
// Returns the number of steps actually started. The caller depends on it: a release for which
// no verification could start moves to `ready` rather than waiting for a verdict nobody will
// give.
//
// An empty plan opens **no** run: a row with no step would be a run nothing advances, and
// which would take a sweep to find again.
func (e *Engine) Trigger(ctx context.Context, request store.PlanRun) int {
	if request.Plan.Empty() {
		return 0
	}
	if err := request.Plan.Validate(); err != nil {
		// Refused here rather than half unrolled: an invalid plan would leave a run open on steps the
		// model cannot chain.
		e.logger.Error("plan refused", "trigger", request.Trigger,
			"fact", request.EventKey, "error", err)
		return 0
	}

	run, created, err := e.store.CreatePlanRun(ctx, request)
	if err != nil {
		e.logger.Error("opening a plan run",
			"trigger", request.Trigger, "fact", request.EventKey, "error", err)
		return 0
	}
	if !created {
		// The nominal case of a redelivery. It still advances: a run opened then interrupted by a
		// restart must start again.
		e.logger.Debug("plan run already open",
			"run", run.ID, "fact", request.EventKey)
	}

	return e.advance(ctx, run)
}

// Advance concludes the step a job was carrying out, and starts what follows.
//
// Without effect when the job comes from no plan, which is the common case: most jobs are
// launched by hand or by the creation of a release.
func (e *Engine) Advance(ctx context.Context, jobID int64, state plan.State) {
	runID, step, err := e.store.PlanStepForJob(ctx, jobID)
	if err != nil {
		if !errors.Is(err, store.ErrPlanRunNotFound) {
			e.logger.Error("step of a concluded job", "job", jobID, "error", err)
		}
		return
	}

	// An already concluded step is not concluded again. The observer redelivers finished runs,
	// and rewriting the state would restart the next stage at every pass — the exact defect
	// `advanceRelease` documents for the verifications.
	if plan.State(step.State).Terminal() {
		e.logger.Debug("step already concluded, redelivery ignored",
			"run", runID, "job", jobID, "state", step.State)
		return
	}

	if err := e.store.MarkPlanStep(ctx, runID, step.Stage, step.Position,
		string(state), nil, ""); err != nil {
		e.logger.Error("concluding a step", "run", runID, "job", jobID, "error", err)
		return
	}

	run, err := e.store.PlanRunByID(ctx, runID)
	if err != nil {
		e.logger.Error("running a concluded step", "run", runID, "error", err)
		return
	}

	// What the step produced, **before** starting what follows: the next stage may target it, and
	// collecting it afterwards would make that stage start on an artifact that does not exist yet.
	if state == plan.Succeeded {
		e.collectArtifacts(ctx, run, step, jobID)
	}

	e.advance(ctx, run)
}

// collectArtifacts keeps what a step has just produced.
//
// # The pipeline's results, as they are
//
// Each Tekton result becomes an artifact carrying its name: `image`, `tag`. No translation,
// no selection — the pipeline says what it returns, and the plan keeps it. Choosing here
// which are "real artifacts" would need a second declaration to keep in agreement with the
// pipeline's.
//
// # A failed collection does not stop the plan
//
// The step succeeded; losing that fact because its results could not be read would be worse
// than continuing without them. The next stage will then fail by name on the missing
// artifact, which is a diagnosis, where a run stopped here would not be one.
func (e *Engine) collectArtifacts(
	ctx context.Context, run store.PlanRun, step store.PlanStep, jobID int64,
) {
	if e.artifacts == nil {
		return
	}

	results, err := e.artifacts.RunResults(ctx, jobID)
	if err != nil {
		e.logger.Error("results of a concluded step",
			"run", run.ID, "job", jobID, "error", err)
		return
	}
	if len(results) == 0 {
		return
	}

	kind := ""
	if e.products != nil {
		kind = e.products(step.Ref)
	}

	produced := make([]store.PlanArtifact, 0, len(results))
	for name, reference := range results {
		produced = append(produced, store.PlanArtifact{
			Stage: step.Stage, Position: step.Position,
			Name: name, Kind: kind, Reference: reference,
		})
	}

	if err := e.artifacts.RecordPlanArtifacts(ctx, run.ID, run.ReleaseID, produced); err != nil {
		e.logger.Error("artifacts of a concluded step",
			"run", run.ID, "job", jobID, "error", err)
		return
	}
	e.logger.Info("artifacts collected", "run", run.ID,
		"step", step.Ref, "artifacts", len(produced))
}

// advance starts what can start, and concludes the run if it has settled. Returns the number
// of steps started.
func (e *Engine) advance(ctx context.Context, run store.PlanRun) int {
	progress, err := e.progressOf(ctx, run)
	if err != nil {
		e.logger.Error("advancing a run", "run", run.ID, "error", err)
		return 0
	}

	// Before starting what follows: announce what has just settled.
	//
	// The order counts. A release must move to `verifying` **before** its tests are queued,
	// otherwise the first test concluded would find a release still building and its transition
	// would be refused.
	e.notifyStages(ctx, run, progress)

	// What the stages already concluded produced, added to the context. That is what lets a test
	// step target the image the build has just published: `{artifacts.portal.image}`.
	run.Context = e.withArtifacts(ctx, run, progress.Plan)

	started := 0
	for _, step := range progress.Next() {
		if !step.Run {
			// Its condition is not met. Marked `skipped` and not left waiting: a stage one of whose steps
			// stays waiting never settles, and the run would stop there saying nothing.
			e.mark(ctx, run.ID, step.Position, plan.Skipped, nil,
				"condition non remplie")
			continue
		}

		job, err := e.runner.Run(ctx, run, step.Geste)
		if err != nil {
			e.logger.Error("carrying out a step", "run", run.ID,
				"step", step.Geste.Ref, "error", err)
			e.mark(ctx, run.ID, step.Position, plan.Failed, nil, err.Error())
			continue
		}

		started++
		if job == nil {
			// An action is concluded the moment it is done: nothing will arrive later to conclude it.
			e.mark(ctx, run.ID, step.Position, plan.Succeeded, nil, "")
			continue
		}
		// A pipeline step stays **in progress**: its job's conclusion is what will settle it. Marking
		// it succeeded here was the stopgap of synchronous execution, and is precisely what this
		// slice fixes.
		e.mark(ctx, run.ID, step.Position, plan.Running, job, "")
	}

	// Read again after the writes: `Next` may have settled everything — a stage skipped entirely,
	// an action that concludes — and the run is then finished.
	if started == 0 || len(progress.Next()) > 0 {
		e.concludeIfSettled(ctx, run)
	}
	return started
}

// StageSink is told that a stage carrying a role has just settled.
//
// It is where a release's life cycle is anchored in the plan. The engine knows nothing of
// releases: it says "the build stage settled thus", and what is made of that is none of its
// business.
type StageSink interface {
	StageSettled(ctx context.Context, run store.PlanRun,
		role plan.Role, outcome plan.State)
}

// WithArtifacts wires the collection of what the steps produce.
//
// `products` translates a catalogue reference into an artifact kind — a pipeline's
// `produces`. The engine does not know the catalogue, and giving it to it would invite it to
// decide what is launched.
func (e *Engine) WithArtifacts(artifacts ArtifactStore, products func(ref string) string) *Engine {
	e.artifacts, e.products = artifacts, products
	return e
}

// WithStages wires the observer of the stages with a role.
func (e *Engine) WithStages(sink StageSink) *Engine {
	e.stages = sink
	return e
}

// notifyStages announces every stage with a role that has settled.
//
// # With no memory of what was already announced
//
// The same stage is therefore announced at every advance. That is deliberate: keeping a
// "already told" mark would need a column, and forgetting it once would leave a release stuck
// with nothing to say so.
//
// The repetition has no effect because the recipient writes **conditionally** —
// `AdvanceReleaseState(from, to)` succeeds only once. It is the same idempotence as the old
// build conclusion, obtained in the same place.
func (e *Engine) notifyStages(ctx context.Context, run store.PlanRun, progress plan.Progress) {
	if e.stages == nil {
		return
	}
	for index, stage := range progress.Plan.Stages {
		if stage.Role == "" {
			// A stage with no role touches nothing. That is what makes a notification stage harmless to a
			// release's life cycle.
			continue
		}
		outcome, settled := progress.StageOutcome(index)
		if !settled {
			continue
		}
		e.stages.StageSettled(ctx, run, stage.Role, outcome)
	}
}

// withArtifacts adds to the context what the concluded steps produced.
//
// # The shape: `artifacts.<step>.<name>`
//
// The key is the producing step's **label**, not its position: the label is what one reads in
// the editor, and `{artifacts.portal.image}` is understood where `{artifacts.0.0.image}`
// would require counting the stages.
//
// Three build steps carrying the same catalogue reference are thus told apart by their label
// — which is exactly why composing a release plan fills it with the image's name.
//
// # A lookup, not a language
//
// The context gains keys; `action.Expand` does the rest. No operator, no condition, no call.
func (e *Engine) withArtifacts(
	ctx context.Context, run store.PlanRun, sujet plan.Plan,
) map[string]string {
	if e.artifacts == nil {
		return run.Context
	}

	produced, err := e.artifacts.PlanArtifacts(ctx, run.ID)
	if err != nil {
		// Without them, a step targeting an artifact will fail by name. That is a better diagnosis
		// than a run stopped here.
		e.logger.Error("artifacts of a run", "run", run.ID, "error", err)
		return run.Context
	}
	if len(produced) == 0 {
		return run.Context
	}

	enrichi := make(map[string]string, len(run.Context)+len(produced))
	maps.Copy(enrichi, run.Context)
	for _, artifact := range produced {
		libelle := stepLabelAt(sujet, artifact.Stage, artifact.Position)
		if libelle == "" {
			continue
		}
		enrichi["artifacts."+libelle+"."+artifact.Name] = artifact.Reference
	}
	return enrichi
}

// stepLabelAt reads a step's label in the frozen plan.
//
// The **frozen** plan, and not the current catalogue: a step renamed since must not make what
// it produced under its old name impossible to find.
func stepLabelAt(sujet plan.Plan, stage, position int) string {
	if stage < 0 || stage >= len(sujet.Stages) {
		return ""
	}
	gestes := sujet.Stages[stage].Gestes
	if position < 0 || position >= len(gestes) {
		return ""
	}
	return gestes[position].Label
}

// mark writes a step's outcome without interrupting the others' advance.
func (e *Engine) mark(
	ctx context.Context, runID int64, position plan.Position,
	state plan.State, job *int64, detail string,
) {
	if err := e.store.MarkPlanStep(ctx, runID, position.Stage, position.Geste,
		string(state), job, detail); err != nil {
		e.logger.Error("marking a step", "run", runID,
			"stage", position.Stage, "step", position.Geste, "error", err)
	}
}

// concludeIfSettled concludes a run in which nothing moves any more.
func (e *Engine) concludeIfSettled(ctx context.Context, run store.PlanRun) {
	progress, err := e.progressOf(ctx, run)
	if err != nil {
		e.logger.Error("concluding a run", "run", run.ID, "error", err)
		return
	}
	if !progress.Done() {
		return
	}

	state := store.PlanSucceeded
	if progress.Outcome() == plan.Failed {
		state = store.PlanFailed
	}

	concluded, err := e.store.FinishPlanRun(ctx, run.ID, state)
	if err != nil {
		e.logger.Error("concluding a run", "run", run.ID, "error", err)
		return
	}
	if concluded {
		e.logger.Info("plan run concluded",
			"run", run.ID, "trigger", run.Trigger, "outcome", state)
	}
}

// progressOf recomposes the progress from the database.
//
// Read again at each pass rather than kept in memory: steps are concluded from other calls —
// a job's end arrives through the observer — and progress kept in memory would be wrong from
// the second one on.
func (e *Engine) progressOf(ctx context.Context, run store.PlanRun) (plan.Progress, error) {
	steps, err := e.store.PlanSteps(ctx, run.ID)
	if err != nil {
		return plan.Progress{}, err
	}

	progress := plan.Start(run.Plan)
	for _, step := range steps {
		if step.Stage >= len(progress.States) ||
			step.Position >= len(progress.States[step.Stage]) {
			// A step outside the frozen plan. Should not happen — the plan is copied into its run
			// precisely for that — but keeping quiet about it would advance on an incomplete state.
			return plan.Progress{}, fmt.Errorf(
				"step %d/%d outside the plan of run %d",
				step.Stage, step.Position, run.ID)
		}
		progress.States[step.Stage][step.Position] = plan.State(step.State)
	}
	return progress, nil
}
