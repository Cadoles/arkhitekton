package store

import (
	"context"
	"fmt"
	"time"
)

// TestResult est le résultat d'un outil sur un run.
type TestResult struct {
	ID         int64
	RunID      int64
	ReleaseID  *int64
	Family     string
	Tool       string
	Status     string
	Total      int
	Passed     int
	Failed     int
	Skipped    int
	Flaky      int
	DurationMS int64
	Summary    string
	RawObject  string
	CreatedAt  time.Time
}

// TestCase est un cas individuel.
type TestCase struct {
	ID             int64
	TestResultID   int64
	Suite          string
	Name           string
	Status         string
	DurationMS     int
	FailureMessage string
	FailureTrace   string
}

// SaveTestResult enregistre un rapport et ses cas.
//
// En une transaction : un rapport dont la moitié des cas manque ferait conclure à
// des tests supprimés lors de la comparaison à la release précédente, ce qui est
// exactement le signal qu'on cherche à rendre fiable.
func (db *DB) SaveTestResult(ctx context.Context, result TestResult, cases []TestCase) (int64, error) {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("résultats de tests: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var id int64
	err = transaction.QueryRow(ctx,
		`insert into test_results (
		     run_id, release_id, family, tool, status, total, passed, failed, skipped,
		     flaky, duration_ms, summary, raw_object
		 ) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, nullif($12, ''), nullif($13, ''))
		 returning id`,
		result.RunID, result.ReleaseID, result.Family, result.Tool, result.Status,
		result.Total, result.Passed, result.Failed, result.Skipped, result.Flaky,
		result.DurationMS, result.Summary, result.RawObject).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("résultats %q du run %d: %w", result.Tool, result.RunID, err)
	}

	for _, testCase := range cases {
		_, err := transaction.Exec(ctx,
			`insert into test_cases (
			     test_result_id, suite, name, status, duration_ms, failure_message, failure_trace
			 ) values ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''))`,
			id, testCase.Suite, testCase.Name, testCase.Status, testCase.DurationMS,
			testCase.FailureMessage, testCase.FailureTrace)
		if err != nil {
			return 0, fmt.Errorf("cas %q/%q: %w", testCase.Suite, testCase.Name, err)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("résultats de tests: %w", err)
	}
	return id, nil
}

// TestResultExists dit si un outil est déjà enregistré pour ce run.
//
// L'ingestion doit être idempotente : l'observateur revoit un run terminé au
// redémarrage, et réenregistrer doublerait chaque cas — donc chaque comparaison.
func (db *DB) TestResultExists(ctx context.Context, runID int64, tool string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`select exists(select 1 from test_results where run_id = $1 and tool = $2)`,
		runID, tool).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("résultats %q du run %d: %w", tool, runID, err)
	}
	return exists, nil
}

// ListReleaseTestResults rend les rapports d'une release.
func (db *DB) ListReleaseTestResults(ctx context.Context, releaseID int64) ([]TestResult, error) {
	rows, err := db.pool.Query(ctx,
		`select id, run_id, release_id, family, tool, status,
		        coalesce(total, 0), coalesce(passed, 0), coalesce(failed, 0),
		        coalesce(skipped, 0), coalesce(flaky, 0), coalesce(duration_ms, 0),
		        coalesce(summary, ''), coalesce(raw_object, ''), created_at
		 from test_results
		 where release_id = $1 and family <> 'load'
		 order by created_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("résultats de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var results []TestResult
	for rows.Next() {
		var result TestResult
		if err := rows.Scan(&result.ID, &result.RunID, &result.ReleaseID, &result.Family,
			&result.Tool, &result.Status, &result.Total, &result.Passed, &result.Failed,
			&result.Skipped, &result.Flaky, &result.DurationMS, &result.Summary,
			&result.RawObject, &result.CreatedAt); err != nil {
			return nil, fmt.Errorf("résultats de la release %d: %w", releaseID, err)
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// ListTestCases rend les cas d'un rapport.
func (db *DB) ListTestCases(ctx context.Context, resultID int64, onlyFailures bool) ([]TestCase, error) {
	query := `select id, test_result_id, suite, name, status, coalesce(duration_ms, 0),
	                 coalesce(failure_message, ''), coalesce(failure_trace, '')
	          from test_cases where test_result_id = $1`
	if onlyFailures {
		query += ` and status in ('failed', 'flaky')`
	}
	query += ` order by suite, name`

	rows, err := db.pool.Query(ctx, query, resultID)
	if err != nil {
		return nil, fmt.Errorf("cas du rapport %d: %w", resultID, err)
	}
	defer rows.Close()

	var cases []TestCase
	for rows.Next() {
		var testCase TestCase
		if err := rows.Scan(&testCase.ID, &testCase.TestResultID, &testCase.Suite,
			&testCase.Name, &testCase.Status, &testCase.DurationMS,
			&testCase.FailureMessage, &testCase.FailureTrace); err != nil {
			return nil, fmt.Errorf("cas du rapport %d: %w", resultID, err)
		}
		cases = append(cases, testCase)
	}
	return cases, rows.Err()
}

// CaseDiff est la comparaison d'une famille à la release précédente.
//
// C'est la vue qui répond à la question réellement posée en recette — « qu'est-ce
// qui a changé ? » — et non « combien de tests passent ». Un compteur stable peut
// cacher un échec résolu et un échec apparu.
type CaseDiff struct {
	Suite  string
	Name   string
	Status string
	// Was est le statut dans la release comparée. Vide quand le cas n'y existait
	// pas — c'est-à-dire un test ajouté.
	Was string
	// Change vaut `new-failure`, `fixed`, `added`, `removed` ou `unchanged`.
	Change string
}

// Natures de changement entre deux releases.
const (
	ChangeNewFailure = "new-failure"
	ChangeFixed      = "fixed"
	ChangeAdded      = "added"
	ChangeRemoved    = "removed"
	ChangeUnchanged  = "unchanged"
)

// DiffFamily compare une famille de tests entre deux releases.
//
// Comparaison en base et non en Go : les deux jeux de cas peuvent compter des
// milliers d'entrées, et les rapatrier pour les apparier en mémoire ferait
// transiter tout l'historique pour n'en garder que les écarts.
//
// Le `full outer join` est ce qui rend les quatre natures de changement
// visibles d'un coup : un `inner join` perdrait les tests ajoutés et supprimés,
// qui sont précisément ceux qu'on ne remarque pas à l'œil.
func (db *DB) DiffFamily(ctx context.Context, releaseID, previousID int64, family string) ([]CaseDiff, error) {
	const query = `
		with current_cases as (
		    select distinct on (c.suite, c.name) c.suite, c.name, c.status
		    from test_cases c
		    join test_results r on r.id = c.test_result_id
		    where r.release_id = $1 and r.family = $3
		    -- Le rapport le plus récent fait foi : une release peut être testée
		    -- plusieurs fois, et c'est la dernière exécution qui la décrit.
		    order by c.suite, c.name, r.created_at desc
		),
		previous_cases as (
		    select distinct on (c.suite, c.name) c.suite, c.name, c.status
		    from test_cases c
		    join test_results r on r.id = c.test_result_id
		    where r.release_id = $2 and r.family = $3
		    order by c.suite, c.name, r.created_at desc
		)
		select coalesce(c.suite, p.suite), coalesce(c.name, p.name),
		       coalesce(c.status, ''), coalesce(p.status, ''),
		       case
		           when p.name is null then 'added'
		           when c.name is null then 'removed'
		           when c.status in ('failed', 'flaky') and p.status not in ('failed', 'flaky')
		               then 'new-failure'
		           when p.status in ('failed', 'flaky') and c.status not in ('failed', 'flaky')
		               then 'fixed'
		           else 'unchanged'
		       end as change
		from current_cases c
		full outer join previous_cases p on p.suite = c.suite and p.name = c.name
		order by 5, 1, 2`

	rows, err := db.pool.Query(ctx, query, releaseID, previousID, family)
	if err != nil {
		return nil, fmt.Errorf("comparaison de la famille %q: %w", family, err)
	}
	defer rows.Close()

	var diffs []CaseDiff
	for rows.Next() {
		var diff CaseDiff
		if err := rows.Scan(&diff.Suite, &diff.Name, &diff.Status, &diff.Was, &diff.Change); err != nil {
			return nil, fmt.Errorf("comparaison de la famille %q: %w", family, err)
		}
		diffs = append(diffs, diff)
	}
	return diffs, rows.Err()
}

// PreviousRelease rend la release précédente d'une version, par date de création.
//
// Comparée à la précédente et non à la dernière finale : une RC se compare à la
// RC qui la précède, sans quoi `rc2` serait confrontée à une release livrée des
// semaines plus tôt et tout apparaîtrait comme un changement.
func (db *DB) PreviousRelease(ctx context.Context, releaseID int64) (int64, error) {
	var previous int64
	err := db.pool.QueryRow(ctx,
		`select p.id from releases r
		 join releases p on p.created_at < r.created_at
		 where r.id = $1
		 order by p.created_at desc
		 limit 1`, releaseID).Scan(&previous)
	if err != nil {
		return 0, ErrReleaseNotFound
	}
	return previous, nil
}

// ReprocessTarget est un run dont les résultats sont rejouables.
type ReprocessTarget struct {
	RunID     int64
	RunName   string
	ReleaseID *int64
	// Tools nomme ce qui a déjà été projeté pour ce run. Sert au journal : savoir
	// ce qui disparaît avant de le reconstruire est le seul moyen de constater
	// qu'un parseur corrigé produit désormais moins, et non davantage.
	Tools []string
}

// ListReprocessTargets rend les runs dont les artefacts bruts sont connus.
//
// La sélection part de `raw_object` et non de la liste des runs : un run dont
// l'objet source n'est pas enregistré n'est pas rejouable, et le proposer
// donnerait un rejeu qui efface sans reconstruire.
func (db *DB) ListReprocessTargets(ctx context.Context, releaseID int64) ([]ReprocessTarget, error) {
	query := `select r.run_id, coalesce(runs.pipelinerun_name, ''), r.release_id,
	                 array_agg(distinct r.tool)
	          from test_results r
	          join runs on runs.id = r.run_id
	          where r.raw_object is not null`
	args := []any{}
	if releaseID > 0 {
		query += ` and r.release_id = $1`
		args = append(args, releaseID)
	}
	query += ` group by r.run_id, runs.pipelinerun_name, r.release_id
	           order by r.run_id`

	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("runs rejouables: %w", err)
	}
	defer rows.Close()

	var targets []ReprocessTarget
	for rows.Next() {
		var target ReprocessTarget
		if err := rows.Scan(&target.RunID, &target.RunName, &target.ReleaseID, &target.Tools); err != nil {
			return nil, fmt.Errorf("runs rejouables: %w", err)
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// DeleteTestResults efface les résultats d'un run.
//
// Effacer puis reconstruire, plutôt qu'ajouter : un rejeu qui empile
// dupliquerait chaque cas et fausserait toute comparaison inter-release, c'est-
// à-dire précisément ce que le rejeu sert à corriger.
//
// Les cas, findings et mesures partent avec, par cascade. C'est voulu : ils
// n'ont aucun sens détachés du rapport qui les a produits.
func (db *DB) DeleteTestResults(ctx context.Context, runID int64) (int64, error) {
	tally, err := db.pool.Exec(ctx, `delete from test_results where run_id = $1`, runID)
	if err != nil {
		return 0, fmt.Errorf("effacement des résultats du run %d: %w", runID, err)
	}
	return tally.RowsAffected(), nil
}
