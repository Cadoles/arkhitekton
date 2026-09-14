package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
)

/*
 * L'exécution des plans.
 *
 * Ce magasin ne décide de rien : `internal/plan` dit quel geste part ensuite, et
 * cette couche se contente de retenir ce qui a été décidé. La séparation est la
 * même que pour `internal/release` et pour la même raison — l'avancement est de la
 * logique, et une logique mêlée à du SQL ne s'éprouve qu'avec une base.
 */

// PlanRun est une exécution de plan.
type PlanRun struct {
	ID int64
	// Trigger nomme ce qui a déclenché : `builtin:<nom>` ou `reaction:<id>`.
	Trigger string
	// EventKey est la clé du fait déclencheur.
	EventKey  string
	ProjectID *int64
	ReleaseID *int64
	Plan      plan.Plan
	// Context est la charge du fait déclencheur. Elle lie les paramètres des
	// gestes : le plan nomme, l'exécution résout.
	Context   map[string]string
	State     string
	CreatedAt time.Time
}

// PlanStep est le sort d'un geste.
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

// États d'une exécution de plan. Les mêmes valeurs que celles de `internal/plan`,
// écrites ici parce que la base ne dépend pas de ce paquet pour ses contraintes.
const (
	PlanRunning   = "running"
	PlanSucceeded = "succeeded"
	PlanFailed    = "failed"
)

// ErrPlanRunNotFound signale une exécution absente.
var ErrPlanRunNotFound = errors.New("exécution de plan introuvable")

// CreatePlanRun ouvre une exécution, ou rend celle qui existe déjà.
//
// Le second retour dit laquelle des deux : `false` pour une exécution qui
// existait. Ce n'est **pas une erreur** — c'est le cas nominal d'une
// redélivrance, et le traiter comme un échec ferait journaliser une panne à
// chaque redémarrage du worker.
//
// Les gestes sont créés dans la même transaction que l'exécution : une exécution
// sans ses gestes serait une ligne que rien ne fait avancer, et il faudrait un
// balai pour la retrouver.
func (db *DB) CreatePlanRun(ctx context.Context, run PlanRun) (PlanRun, bool, error) {
	encoded, err := json.Marshal(run.Plan)
	if err != nil {
		return PlanRun{}, false, fmt.Errorf("plan illisible: %w", err)
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return PlanRun{}, false, fmt.Errorf("ouverture d'une exécution de plan: %w", err)
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
		// Déjà ouverte. On rend celle qui existe, pour que l'appelant puisse la
		// faire avancer plutôt que de repartir de zéro.
		existing, err := db.PlanRunByEvent(ctx, run.Trigger, run.EventKey)
		return existing, false, err
	case err != nil:
		return PlanRun{}, false, fmt.Errorf("ouverture d'une exécution de plan: %w", err)
	}

	for stage, step := range run.Plan.Stages {
		for position, geste := range step.Gestes {
			if _, err := tx.Exec(ctx,
				`insert into plan_steps (run_id, stage, position, kind, ref)
				 values ($1, $2, $3, $4, $5)`,
				id, stage, position, string(geste.Kind), geste.Ref); err != nil {
				return PlanRun{}, false, fmt.Errorf("gestes de l'exécution %d: %w", id, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return PlanRun{}, false, fmt.Errorf("ouverture d'une exécution de plan: %w", err)
	}

	run.ID = id
	run.State = PlanRunning
	return run, true, nil
}

// PlanRunByEvent rend l'exécution ouverte pour un fait.
func (db *DB) PlanRunByEvent(ctx context.Context, trigger, eventKey string) (PlanRun, error) {
	const query = `
		select id, trigger, event_key, project_id, release_id, plan, context, state, created_at
		from plan_runs where trigger = $1 and event_key = $2`

	return db.scanPlanRun(ctx, query, trigger, eventKey)
}

// PlanRunByID rend une exécution par son identifiant.
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
		return PlanRun{}, fmt.Errorf("lecture d'une exécution de plan: %w", err)
	}
	if err := json.Unmarshal(encoded, &run.Plan); err != nil {
		return PlanRun{}, fmt.Errorf("plan de l'exécution %d illisible: %w", run.ID, err)
	}
	return run, nil
}

// PlanSteps rend les gestes d'une exécution, dans l'ordre du plan.
func (db *DB) PlanSteps(ctx context.Context, runID int64) ([]PlanStep, error) {
	const query = `
		select id, stage, position, kind, ref, state, job_id, detail
		from plan_steps where run_id = $1
		order by stage, position`

	rows, err := db.pool.Query(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("gestes de l'exécution %d: %w", runID, err)
	}
	defer rows.Close()

	var steps []PlanStep
	for rows.Next() {
		var step PlanStep
		if err := rows.Scan(&step.ID, &step.Stage, &step.Position, &step.Kind,
			&step.Ref, &step.State, &step.JobID, &step.Detail); err != nil {
			return nil, fmt.Errorf("lecture d'un geste: %w", err)
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// MarkPlanStep écrit le sort d'un geste.
//
// `job` rattache le geste au job enfilé, et n'est renseigné qu'au départ d'un
// geste de type pipeline. C'est ce rattachement qui permettra, à la conclusion du
// job, de retrouver le geste qui l'attendait.
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
		return fmt.Errorf("marquage du geste %d/%d de l'exécution %d: %w",
			stage, position, runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: geste %d/%d absent de l'exécution %d",
			ErrPlanRunNotFound, stage, position, runID)
	}
	return nil
}

// PlanStepForJob retrouve le geste qu'un job met en œuvre.
//
// Rend [ErrPlanRunNotFound] quand aucun geste n'attend ce job — le cas courant :
// la plupart des jobs ne viennent d'aucun plan.
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
		return 0, PlanStep{}, fmt.Errorf("geste du job %d: %w", jobID, err)
	}
	return runID, step, nil
}

func orEmptyContext(context map[string]string) map[string]string {
	if context == nil {
		return map[string]string{}
	}
	return context
}

// FinishPlanRun conclut une exécution.
//
// Écriture conditionnée à l'état `running` : deux conclusions concurrentes — deux
// gestes terminant en même temps — ne doivent pas produire deux conclusions, et
// c'est la base qui tranche plutôt qu'un verrou en mémoire.
func (db *DB) FinishPlanRun(ctx context.Context, runID int64, state string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update plan_runs set state = $2, finished_at = now()
		 where id = $1 and state = $3`, runID, state, PlanRunning)
	if err != nil {
		return false, fmt.Errorf("conclusion de l'exécution %d: %w", runID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// PlanRunSummary est une exécution et ses gestes, telle que l'écran la lit.
type PlanRunSummary struct {
	PlanRun
	Steps []PlanStep
}

// maxPlanRunPage borne la lecture.
//
// Une vue d'exécutions se parcourt : on vient y chercher ce qui vient de se
// passer, pas l'historique entier. La borne évite qu'un projet ancien rende une
// page que le navigateur met une seconde à peindre.
const maxPlanRunPage = 100

// ListPlanRuns rend les exécutions d'un projet, de la plus récente à la plus
// ancienne, avec leurs gestes.
//
// Deux requêtes et non une jointure : une jointure rendrait une ligne par geste,
// et recomposer les exécutions côté Go depuis un produit aplati coûterait plus de
// code que la seconde requête n'économise d'aller-retour.
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
		return nil, fmt.Errorf("exécutions du projet %d: %w", projectID, err)
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
			return nil, fmt.Errorf("lecture d'une exécution: %w", err)
		}
		if err := json.Unmarshal(encoded, &run.Plan); err != nil {
			return nil, fmt.Errorf("plan de l'exécution %d illisible: %w", run.ID, err)
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

// planStepsFor rend les gestes de plusieurs exécutions, indexés par exécution.
func (db *DB) planStepsFor(ctx context.Context, runIDs []int64) (map[int64][]PlanStep, error) {
	const query = `
		select run_id, id, stage, position, kind, ref, state, job_id, detail
		from plan_steps where run_id = any($1)
		order by run_id, stage, position`

	rows, err := db.pool.Query(ctx, query, runIDs)
	if err != nil {
		return nil, fmt.Errorf("gestes des exécutions: %w", err)
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
			return nil, fmt.Errorf("lecture d'un geste: %w", err)
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

// PlanArtifact est ce qu'un geste a laissé derrière lui.
type PlanArtifact struct {
	Stage    int
	Position int
	// Name est le nom du résultat rendu par le pipeline : `image`, `tag`.
	Name string
	// Kind reprend le vocabulaire de `produces` du catalogue. Vide quand le
	// pipeline n'en déclare aucun.
	Kind      string
	Reference string
}

// RecordPlanArtifacts enregistre ce qu'un geste a produit.
//
// Rappelable sans effet : une redélivrance réécrit la même référence plutôt que
// d'empiler. L'observateur redélivre les runs terminés, et c'est la règle plutôt
// que l'exception.
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
			return fmt.Errorf("enregistrement des artefacts de l'exécution %d: %w", runID, err)
		}
	}
	return nil
}

// PlanArtifacts rend ce qu'une exécution a produit jusqu'ici.
func (db *DB) PlanArtifacts(ctx context.Context, runID int64) ([]PlanArtifact, error) {
	rows, err := db.pool.Query(ctx,
		`select stage, position, name, kind, reference
		 from plan_artifacts where run_id = $1
		 order by stage, position, name`, runID)
	if err != nil {
		return nil, fmt.Errorf("artefacts de l'exécution %d: %w", runID, err)
	}
	defer rows.Close()

	var artifacts []PlanArtifact
	for rows.Next() {
		var entry PlanArtifact
		if err := rows.Scan(&entry.Stage, &entry.Position,
			&entry.Name, &entry.Kind, &entry.Reference); err != nil {
			return nil, fmt.Errorf("artefacts de l'exécution %d: %w", runID, err)
		}
		artifacts = append(artifacts, entry)
	}
	return artifacts, rows.Err()
}

// RunResults rend les résultats Tekton du run d'un job.
//
// Vide quand le job n'a pas de run, ou que son pipeline n'a rien rendu : les deux
// sont des cas normaux, et les distinguer n'apporterait rien à l'appelant — il
// n'a d'artefacts à enregistrer ni dans un cas ni dans l'autre.
func (db *DB) RunResults(ctx context.Context, jobID int64) (map[string]string, error) {
	rows, err := db.pool.Query(ctx,
		`select coalesce(results, '{}'::jsonb) from runs where job_id = $1`, jobID)
	if err != nil {
		return nil, fmt.Errorf("résultats du job %d: %w", jobID, err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, fmt.Errorf("résultats du job %d: %w", jobID, err)
	}
	results := map[string]string{}
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("résultats du job %d: %w", jobID, err)
	}
	return results, nil
}
