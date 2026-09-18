package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

/*
 * L'exécution des plans.
 *
 * Ce magasin ne décide de rien : `internal/plan` dit quel geste part ensuite, et
 * cette couche se contente de retenir ce qui a été décidé. La séparation est la
 * même que pour `internal/release` et pour la même raison — l'avancement est de la
 * logique, et une logique mêlée à du SQL ne s'éprouve qu'avec une base.
 */

// PlanRun is a run of a plan.
type PlanRun struct {
	ID int64
	// Trigger names what triggered it: `builtin:<name>` or `reaction:<id>`.
	Trigger string
	// EventKey is the key of the triggering fact.
	EventKey  string
	ProjectID *int64
	ReleaseID *int64
	Plan      plan.Plan
	// Context is the triggering fact's payload. It binds the steps' parameters: the plan
	// names, the run resolves.
	Context   map[string]string
	State     string
	CreatedAt time.Time
}

// PlanStep is a step's fate.
type PlanStep struct {
	ID       int64
	Stage    int
	Position int
	Kind     string
	Ref      string
	State    string
	JobID    *int64
	Detail   string
}

// States of a plan run. The same values as internal/plan's, written here because the
// database does not depend on that package for its constraints.
const (
	PlanRunning   = "running"
	PlanSucceeded = "succeeded"
	PlanFailed    = "failed"
)

// ErrPlanRunNotFound reports a missing run.
var ErrPlanRunNotFound = errors.New("plan run not found")

// CreatePlanRun opens a run, or returns the one that already exists.
//
// The second return says which: false for a run that already existed. That is not an
// error — it is the nominal case of a redelivery, and treating it as a failure would
// log a fault at every worker restart.
//
// The steps are created in the same transaction as the run: a run without its steps
// would be a row nothing advances, and finding it again would take a sweep.
func (db *DB) CreatePlanRun(ctx context.Context, run PlanRun) (PlanRun, bool, error) {
	encoded, err := json.Marshal(run.Plan)
	if err != nil {
		return PlanRun{}, false, fmt.Errorf("plan illisible: %w", err)
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return PlanRun{}, false, fmt.Errorf("opening a plan run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`insert into plan_runs (trigger, event_key, project_id, release_id, plan, context)
		 values ($1, $2, $3, $4, $5, $6)
		 on conflict (trigger, event_key) do nothing
		 returning id`,
		run.Trigger, run.EventKey, run.ProjectID, run.ReleaseID, encoded,
		orEmptyContext(run.Context)).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Already open. The existing one is returned, so the caller can advance it rather
		// than start over.
		existing, err := db.PlanRunByEvent(ctx, run.Trigger, run.EventKey)
		return existing, false, err
	case err != nil:
		return PlanRun{}, false, fmt.Errorf("opening a plan run: %w", err)
	}

	for stage, step := range run.Plan.Stages {
		for position, geste := range step.Gestes {
			if _, err := tx.Exec(ctx,
				`insert into plan_steps (run_id, stage, position, kind, ref)
				 values ($1, $2, $3, $4, $5)`,
				id, stage, position, string(geste.Kind), geste.Ref); err != nil {
				return PlanRun{}, false, fmt.Errorf("steps of run %d: %w", id, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return PlanRun{}, false, fmt.Errorf("opening a plan run: %w", err)
	}

	run.ID = id
	run.State = PlanRunning
	return run, true, nil
}

// PlanRunByEvent returns the run opened for a fact.
func (db *DB) PlanRunByEvent(ctx context.Context, trigger, eventKey string) (PlanRun, error) {
	const query = `
		select id, trigger, event_key, project_id, release_id, plan, context, state, created_at
		from plan_runs where trigger = $1 and event_key = $2`

	return db.scanPlanRun(ctx, query, trigger, eventKey)
}

// PlanRunByID returns a run by its identifier.
func (db *DB) PlanRunByID(ctx context.Context, id int64) (PlanRun, error) {
	const query = `
		select id, trigger, event_key, project_id, release_id, plan, context, state, created_at
		from plan_runs where id = $1`

	return db.scanPlanRun(ctx, query, id)
}

func (db *DB) scanPlanRun(ctx context.Context, query string, args ...any) (PlanRun, error) {
	var (
		run     PlanRun
		encoded []byte
	)
	err := db.pool.QueryRow(ctx, query, args...).Scan(
		&run.ID, &run.Trigger, &run.EventKey, &run.ProjectID, &run.ReleaseID,
		&encoded, &run.Context, &run.State, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PlanRun{}, ErrPlanRunNotFound
		}
		return PlanRun{}, fmt.Errorf("reading a plan run: %w", err)
	}
	if err := json.Unmarshal(encoded, &run.Plan); err != nil {
		return PlanRun{}, fmt.Errorf("plan of run %d unreadable: %w", run.ID, err)
	}
	return run, nil
}

// PlanSteps returns a run's steps, in the plan's order.
func (db *DB) PlanSteps(ctx context.Context, runID int64) ([]PlanStep, error) {
	const query = `
		select id, stage, position, kind, ref, state, job_id, detail
		from plan_steps where run_id = $1
		order by stage, position`

	rows, err := db.pool.Query(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("steps of run %d: %w", runID, err)
	}
	defer rows.Close()

	var steps []PlanStep
	for rows.Next() {
		var step PlanStep
		if err := rows.Scan(&step.ID, &step.Stage, &step.Position, &step.Kind,
			&step.Ref, &step.State, &step.JobID, &step.Detail); err != nil {
			return nil, fmt.Errorf("reading a step: %w", err)
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// MarkPlanStep records a step's fate.
//
// `job` attaches the step to the queued job, and is set only when a pipeline step
// leaves. That attachment is what will allow finding, when the job concludes, the
// step that was waiting for it.
func (db *DB) MarkPlanStep(
	ctx context.Context, runID int64, stage, position int, state string,
	job *int64, detail string,
) error {
	const query = `
		update plan_steps
		set state = $4,
		    job_id = coalesce($5, job_id),
		    detail = $6,
		    started_at  = case when $4 = 'running' then now() else started_at end,
		    finished_at = case when $4 in ('succeeded','failed','skipped') then now() else finished_at end
		where run_id = $1 and stage = $2 and position = $3`

	tag, err := db.pool.Exec(ctx, query, runID, stage, position, state, job, detail)
	if err != nil {
		return fmt.Errorf("marking step %d/%d of run %d: %w",
			stage, position, runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: step %d/%d missing from run %d",
			ErrPlanRunNotFound, stage, position, runID)
	}
	return nil
}

// PlanStepForJob finds the step a job carries out.
//
// Returns [ErrPlanRunNotFound] when no step awaits this job — the common case: most
// jobs come from no plan.
func (db *DB) PlanStepForJob(ctx context.Context, jobID int64) (int64, PlanStep, error) {
	const query = `
		select run_id, id, stage, position, kind, ref, state, job_id, detail
		from plan_steps where job_id = $1`

	var (
		runID int64
		step  PlanStep
	)
	err := db.pool.QueryRow(ctx, query, jobID).Scan(
		&runID, &step.ID, &step.Stage, &step.Position, &step.Kind,
		&step.Ref, &step.State, &step.JobID, &step.Detail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, PlanStep{}, ErrPlanRunNotFound
		}
		return 0, PlanStep{}, fmt.Errorf("step of job %d: %w", jobID, err)
	}
	return runID, step, nil
}

func orEmptyContext(context map[string]string) map[string]string {
	if context == nil {
		return map[string]string{}
	}
	return context
}

// FinishPlanRun concludes a run.
//
// The write is conditioned on the `running` state: two concurrent conclusions — two
// steps finishing at once — must not produce two conclusions, and the database
// settles it rather than an in-memory lock.
func (db *DB) FinishPlanRun(ctx context.Context, runID int64, state string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update plan_runs set state = $2, finished_at = now()
		 where id = $1 and state = $3`, runID, state, PlanRunning)
	if err != nil {
		return false, fmt.Errorf("concluding run %d: %w", runID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// PlanRunSummary is a run and its steps, as the screen reads it.
type PlanRunSummary struct {
	PlanRun
	Steps []PlanStep
}

// maxPlanRunPage bounds the read.
//
// A runs view is browsed: one comes to see what just happened, not the whole history.
// The bound keeps an old project from returning a page the browser takes a second to
// paint.
const maxPlanRunPage = 100

// ListPlanRuns returns a project's runs, newest first, with their steps.
//
// Two queries and not a join: a join would return one row per step, and recomposing
// the runs in Go from a flattened product would cost more code than the second query
// saves in round trips.
func (db *DB) ListPlanRuns(ctx context.Context, projectID int64, limit int) ([]PlanRunSummary, error) {
	if limit <= 0 || limit > maxPlanRunPage {
		limit = maxPlanRunPage
	}

	const query = `
		select id, trigger, event_key, project_id, release_id, plan, context, state, created_at
		from plan_runs
		where project_id = $1
		order by created_at desc, id desc
		limit $2`

	rows, err := db.pool.Query(ctx, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("runs of project %d: %w", projectID, err)
	}
	defer rows.Close()

	summaries := make([]PlanRunSummary, 0, limit)
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var (
			run     PlanRun
			encoded []byte
		)
		if err := rows.Scan(&run.ID, &run.Trigger, &run.EventKey, &run.ProjectID,
			&run.ReleaseID, &encoded, &run.Context, &run.State, &run.CreatedAt); err != nil {
			return nil, fmt.Errorf("reading a run: %w", err)
		}
		if err := json.Unmarshal(encoded, &run.Plan); err != nil {
			return nil, fmt.Errorf("plan of run %d unreadable: %w", run.ID, err)
		}
		summaries = append(summaries, PlanRunSummary{PlanRun: run})
		ids = append(ids, run.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return summaries, nil
	}

	steps, err := db.planStepsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range summaries {
		summaries[index].Steps = steps[summaries[index].ID]
	}
	return summaries, nil
}

// planStepsFor returns the steps of several runs, indexed by run.
func (db *DB) planStepsFor(ctx context.Context, runIDs []int64) (map[int64][]PlanStep, error) {
	const query = `
		select run_id, id, stage, position, kind, ref, state, job_id, detail
		from plan_steps where run_id = any($1)
		order by run_id, stage, position`

	rows, err := db.pool.Query(ctx, query, runIDs)
	if err != nil {
		return nil, fmt.Errorf("steps of the runs: %w", err)
	}
	defer rows.Close()

	steps := map[int64][]PlanStep{}
	for rows.Next() {
		var (
			runID int64
			step  PlanStep
		)
		if err := rows.Scan(&runID, &step.ID, &step.Stage, &step.Position, &step.Kind,
			&step.Ref, &step.State, &step.JobID, &step.Detail); err != nil {
			return nil, fmt.Errorf("reading a step: %w", err)
		}
		steps[runID] = append(steps[runID], step)
	}
	return steps, rows.Err()
}

/*
 * Les artefacts qu'une exécution a produits.
 *
 * # D'où ils viennent
 *
 * Des **résultats Tekton** du run qui a mis le geste en œuvre. L'observation les
 * projette déjà sur `runs` ; cette table les rattache au geste qui les a rendus,
 * ce que la projection d'un run ne sait pas faire — elle ignore les plans.
 *
 * # Pourquoi les recopier plutôt que les joindre
 *
 * Un PipelineRun est ramassé par Tekton, et sa projection peut être purgée avec
 * lui. Les artefacts d'une release livrée doivent survivre à ce ménage : ce sont
 * eux qui disent quelle image porte le numéro qu'on a communiqué.
 */

// PlanArtifact is what a step left behind.
type PlanArtifact struct {
	// RunID is the run that produced it. Set when reading by release, where several runs
	// coexist: a rebuilt release has two, and the screen must be able to say which
	// produced what.
	RunID    int64
	Stage    int
	Position int
	// Name is the result name the pipeline returned: `image`, `tag`.
	Name string
	// Kind reuses the catalogue's `produces` vocabulary. Empty when the pipeline declares
	// none.
	Kind      string
	Reference string
}

// RecordPlanArtifacts records what a step produced.
//
// Callable again with no effect: a redelivery rewrites the same reference rather than
// stacking. The observer redelivers finished runs, and that is the rule rather than
// the exception.
func (db *DB) RecordPlanArtifacts(
	ctx context.Context, runID int64, releaseID *int64, artifacts []PlanArtifact,
) error {
	if len(artifacts) == 0 {
		return nil
	}

	const query = `
		insert into plan_artifacts (run_id, stage, position, name, kind, reference, release_id)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (run_id, stage, position, name) do update
		set kind = excluded.kind, reference = excluded.reference`

	batch := &pgx.Batch{}
	for _, artifact := range artifacts {
		batch.Queue(query, runID, artifact.Stage, artifact.Position,
			artifact.Name, artifact.Kind, artifact.Reference, releaseID)
	}

	results := db.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range artifacts {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("recording the artifacts of run %d: %w", runID, err)
		}
	}
	return nil
}

// PlanArtifacts returns what a run has produced so far.
func (db *DB) PlanArtifacts(ctx context.Context, runID int64) ([]PlanArtifact, error) {
	rows, err := db.pool.Query(ctx,
		`select stage, position, name, kind, reference
		 from plan_artifacts where run_id = $1
		 order by stage, position, name`, runID)
	if err != nil {
		return nil, fmt.Errorf("artifacts of run %d: %w", runID, err)
	}
	defer rows.Close()

	var artifacts []PlanArtifact
	for rows.Next() {
		var entry PlanArtifact
		if err := rows.Scan(&entry.Stage, &entry.Position,
			&entry.Name, &entry.Kind, &entry.Reference); err != nil {
			return nil, fmt.Errorf("artifacts of run %d: %w", runID, err)
		}
		artifacts = append(artifacts, entry)
	}
	return artifacts, rows.Err()
}

// RunResults returns the Tekton results of a job's run.
//
// Empty when the job has no run, or when its pipeline returned nothing: both are
// normal cases, and telling them apart would add nothing for the caller — it has
// artifacts to record in neither.
func (db *DB) RunResults(ctx context.Context, jobID int64) (map[string]string, error) {
	rows, err := db.pool.Query(ctx,
		`select coalesce(results, '{}'::jsonb) from runs where job_id = $1`, jobID)
	if err != nil {
		return nil, fmt.Errorf("results of job %d: %w", jobID, err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, fmt.Errorf("results of job %d: %w", jobID, err)
	}
	results := map[string]string{}
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("results of job %d: %w", jobID, err)
	}
	return results, nil
}

/*
 * Le geste qui a produit un résultat.
 *
 * Le score se pèse désormais par geste et non par signal (doc 15 §15.6). Il faut
 * donc savoir, pour chaque rapport d'une release, de quel geste il vient — et la
 * chaîne existe déjà, sans rien ajouter au schéma : un résultat porte son run, un
 * run porte son job, et un geste est le seul à porter ce job.
 */

// GesteRef names a step by its place in the frozen plan.
//
// Its position, not its label: two steps may share a label, and a renamed plan must
// not orphan what it measured. It is the same key as `plan_steps` and
// `plan_artifacts`.
type GesteRef struct {
	Stage    int
	Position int
}

// ResultGestes returns, for each result of a release, the step that produced it.
//
// Indexed by the result's identifier and not by run: that is the key security audits
// and load campaigns carry, and they must attach to the same step as the test
// reports.
//
// A result with no step does not appear: it comes from a manual launch from the
// queue, which no plan caused. It counts in the test screens, not in a release's
// score — the score covers what the automation measured.
func (db *DB) ResultGestes(ctx context.Context, releaseID int64) (map[int64]GesteRef, error) {
	const query = `
		select r.id, s.stage, s.position
		from test_results r
		join runs p on p.id = r.run_id
		join plan_steps s on s.job_id = p.job_id
		where r.release_id = $1`

	rows, err := db.pool.Query(ctx, query, releaseID)
	if err != nil {
		return nil, fmt.Errorf("steps of the results of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	gestes := map[int64]GesteRef{}
	for rows.Next() {
		var id int64
		var ref GesteRef
		if err := rows.Scan(&id, &ref.Stage, &ref.Position); err != nil {
			return nil, err
		}
		gestes[id] = ref
	}
	return gestes, rows.Err()
}

// ReleaseArtifacts returns what a release's automation produced.
//
// A release's screen asks "what was produced for this tag?", not "what did run 6
// produce?". A rebuilt release has two runs, and the question covers both — which is
// why `plan_artifacts` carries `release_id` as well as `run_id`.
func (db *DB) ReleaseArtifacts(ctx context.Context, releaseID int64) ([]PlanArtifact, error) {
	rows, err := db.pool.Query(ctx,
		`select run_id, stage, position, name, kind, reference
		 from plan_artifacts where release_id = $1
		 order by created_at desc, stage, position, name`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("artifacts of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var artifacts []PlanArtifact
	for rows.Next() {
		var entry PlanArtifact
		if err := rows.Scan(&entry.RunID, &entry.Stage, &entry.Position,
			&entry.Name, &entry.Kind, &entry.Reference); err != nil {
			return nil, fmt.Errorf("artifacts of release %d: %w", releaseID, err)
		}
		artifacts = append(artifacts, entry)
	}
	return artifacts, rows.Err()
}
