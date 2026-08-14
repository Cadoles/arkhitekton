package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LoadResult est une campagne de charge enregistrée.
type LoadResult struct {
	ID        int64
	RunID     int64
	ReleaseID *int64
	Scenario  string
	// Exclusive et ConcurrentJobs sont les conditions **constatées** au démarrage,
	// non celles demandées. C'est ce qui décide si la campagne entre dans la
	// comparaison à la baseline (B16).
	Exclusive      bool
	ConcurrentJobs int
	VUs            int
	DurationMS     int64
	Summary        string
	RawObject      string
	CreatedAt      time.Time
}

// BenchMeasure est une mesure d'une campagne.
type BenchMeasure struct {
	ID           int64
	ResultID     int64
	Scenario     string
	Metric       string
	Label        string
	Unit         string
	Value        float64
	LowerBetter  bool
	Volume       bool
	ThresholdOK  *bool
	CreatedAt    time.Time
	Exclusive    bool
	ConcurrentAt int
}

// ErrBaselineNotFound signale une baseline absente.
var ErrBaselineNotFound = errors.New("baseline introuvable")

// SaveLoadResult enregistre une campagne et ses mesures.
//
// En une transaction : une campagne dont la moitié des mesures manque produirait
// un verdict d'ensemble faux — `Worst` retiendrait le pire de ce qui a été
// enregistré, sans savoir qu'il en manque.
func (db *DB) SaveLoadResult(ctx context.Context, result LoadResult, measures []BenchMeasure) (int64, error) {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("campagne de charge: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var id int64
	err = transaction.QueryRow(ctx,
		`insert into test_results (
		     run_id, release_id, family, tool, status, duration_ms, summary, raw_object,
		     exclusive, concurrent_jobs
		 ) values ($1, $2, 'load', 'k6', $3, $4, $5, nullif($6, ''), $7, $8)
		 returning id`,
		result.RunID, result.ReleaseID, loadStatus(measures), result.DurationMS,
		result.Summary, result.RawObject, result.Exclusive, result.ConcurrentJobs).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("campagne de charge du scénario %q: %w", result.Scenario, err)
	}

	for _, measure := range measures {
		_, err := transaction.Exec(ctx,
			`insert into bench_measures (
			     test_result_id, scenario, metric, label, unit, value,
			     lower_better, volume, threshold_ok
			 ) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, result.Scenario, measure.Metric, measure.Label, measure.Unit,
			measure.Value, measure.LowerBetter, measure.Volume, measure.ThresholdOK)
		if err != nil {
			return 0, fmt.Errorf("mesure %q du scénario %q: %w", measure.Metric, result.Scenario, err)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("campagne de charge: %w", err)
	}
	return id, nil
}

// loadStatus rend le statut de la campagne au sens de `test_results`.
//
// Fondé sur les seuils déclarés dans k6, et non sur la comparaison à la
// baseline : le statut est enregistré une fois pour toutes, alors qu'une
// baseline peut être ré-épinglée plus tard. Faire dépendre une colonne figée
// d'une référence mobile la rendrait fausse au premier ré-épinglage.
func loadStatus(measures []BenchMeasure) string {
	declared := false
	for _, measure := range measures {
		if measure.ThresholdOK == nil {
			continue
		}
		declared = true
		if !*measure.ThresholdOK {
			return "fail"
		}
	}
	if !declared {
		// Aucun seuil déclaré : la campagne a tourné, mais elle ne prononce
		// aucun verdict sur elle-même.
		return "na"
	}
	return "pass"
}

// ListScenarioMeasures rend l'historique d'une métrique, du plus récent au plus
// ancien.
//
// Les conditions de mesure accompagnent chaque point : une tendance qui mêle des
// mesures prises dans des conditions différentes est une tendance inventée, et
// c'est à l'appelant de trancher ce qu'il en fait.
func (db *DB) ListScenarioMeasures(
	ctx context.Context, scenario, metric string, limit int,
) ([]BenchMeasure, error) {
	if limit <= 0 || limit > 100 {
		limit = 6
	}

	rows, err := db.pool.Query(ctx,
		`select m.id, m.test_result_id, m.scenario, m.metric, m.label, m.unit, m.value,
		        m.lower_better, m.volume, m.threshold_ok, m.created_at,
		        coalesce(r.exclusive, false), coalesce(r.concurrent_jobs, 0)
		 from bench_measures m
		 join test_results r on r.id = m.test_result_id
		 where m.scenario = $1 and m.metric = $2
		 order by m.created_at desc, m.id desc
		 limit $3`, scenario, metric, limit)
	if err != nil {
		return nil, fmt.Errorf("historique de %q/%q: %w", scenario, metric, err)
	}
	defer rows.Close()

	return scanMeasures(rows, fmt.Sprintf("historique de %q/%q", scenario, metric))
}

// ListResultMeasures rend les mesures d'une campagne.
func (db *DB) ListResultMeasures(ctx context.Context, resultID int64) ([]BenchMeasure, error) {
	rows, err := db.pool.Query(ctx,
		`select m.id, m.test_result_id, m.scenario, m.metric, m.label, m.unit, m.value,
		        m.lower_better, m.volume, m.threshold_ok, m.created_at,
		        coalesce(r.exclusive, false), coalesce(r.concurrent_jobs, 0)
		 from bench_measures m
		 join test_results r on r.id = m.test_result_id
		 where m.test_result_id = $1
		 order by m.id`, resultID)
	if err != nil {
		return nil, fmt.Errorf("mesures de la campagne %d: %w", resultID, err)
	}
	defer rows.Close()

	return scanMeasures(rows, fmt.Sprintf("mesures de la campagne %d", resultID))
}

// ListReleaseLoadResults rend les campagnes rattachées à une release.
func (db *DB) ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]LoadResult, error) {
	rows, err := db.pool.Query(ctx,
		`select r.id, r.run_id, r.release_id, coalesce(m.scenario, ''),
		        coalesce(r.exclusive, false), coalesce(r.concurrent_jobs, 0),
		        coalesce(r.duration_ms, 0), coalesce(r.summary, ''),
		        coalesce(r.raw_object, ''), r.created_at
		 from test_results r
		 -- Le scénario vit sur les mesures : une campagne sans mesure n'en a pas,
		 -- et le distinct l'évite en double.
		 left join lateral (
		     select scenario from bench_measures where test_result_id = r.id limit 1
		 ) m on true
		 where r.release_id = $1 and r.family = 'load'
		 order by r.created_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("campagnes de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var results []LoadResult
	for rows.Next() {
		var result LoadResult
		if err := rows.Scan(&result.ID, &result.RunID, &result.ReleaseID, &result.Scenario,
			&result.Exclusive, &result.ConcurrentJobs, &result.DurationMS,
			&result.Summary, &result.RawObject, &result.CreatedAt); err != nil {
			return nil, fmt.Errorf("campagnes de la release %d: %w", releaseID, err)
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// Baseline est une mesure de référence épinglée.
type Baseline struct {
	Scenario string
	Metric   string
	Value    float64
	// MeasuredAt est la date de la campagne de référence. Rendue parce qu'une
	// baseline vieillit : savoir de quand elle date fait partie du verdict.
	MeasuredAt time.Time
	PinnedBy   string
	PinnedAt   time.Time
}

// PinBaseline désigne une mesure comme référence.
//
// Ré-épingler est permis et volontairement simple : une baseline vieillit, et le
// refus de la déplacer forcerait à comparer indéfiniment à une campagne dont les
// conditions n'existent plus. Le geste est en revanche tracé — c'est une décision
// qui déplace la référence de tous les verdicts suivants.
func (db *DB) PinBaseline(ctx context.Context, measureID int64, pinnedBy string) error {
	tally, err := db.pool.Exec(ctx,
		`insert into bench_baselines (scenario, metric, measure_id, pinned_by)
		 select scenario, metric, id, $2 from bench_measures where id = $1
		 on conflict (scenario, metric) do update set
		     measure_id = excluded.measure_id,
		     pinned_by = excluded.pinned_by,
		     pinned_at = now()`, measureID, pinnedBy)
	if err != nil {
		return fmt.Errorf("épinglage de la baseline %d: %w", measureID, err)
	}
	if tally.RowsAffected() == 0 {
		return ErrBaselineNotFound
	}
	return nil
}

// Baselines rend les références d'un scénario, indexées par métrique.
func (db *DB) Baselines(ctx context.Context, scenario string) (map[string]Baseline, error) {
	rows, err := db.pool.Query(ctx,
		`select b.scenario, b.metric, m.value, m.created_at, b.pinned_by, b.pinned_at
		 from bench_baselines b
		 join bench_measures m on m.id = b.measure_id
		 where b.scenario = $1`, scenario)
	if err != nil {
		return nil, fmt.Errorf("baselines du scénario %q: %w", scenario, err)
	}
	defer rows.Close()

	baselines := map[string]Baseline{}
	for rows.Next() {
		var baseline Baseline
		if err := rows.Scan(&baseline.Scenario, &baseline.Metric, &baseline.Value,
			&baseline.MeasuredAt, &baseline.PinnedBy, &baseline.PinnedAt); err != nil {
			return nil, fmt.Errorf("baselines du scénario %q: %w", scenario, err)
		}
		baselines[baseline.Metric] = baseline
	}
	return baselines, rows.Err()
}

// RecordStartConditions relève ce qui tournait à la prise d'un job.
//
// Écrit à la réservation et non à l'arrivée du résultat : minutes plus tard, plus
// rien ne dit ce qui tournait au moment de la mesure.
func (db *DB) RecordStartConditions(ctx context.Context, jobID int64, exclusive bool, concurrent int) error {
	_, err := db.pool.Exec(ctx,
		`update jobs set exclusive_at_start = $2, concurrent_at_start = $3, updated_at = now()
		 where id = $1`, jobID, exclusive, concurrent)
	if err != nil {
		return fmt.Errorf("conditions de démarrage du job %d: %w", jobID, err)
	}
	return nil
}

// StartConditions rend les conditions constatées au démarrage d'un job.
func (db *DB) StartConditions(ctx context.Context, jobID int64) (bool, int, error) {
	var exclusive *bool
	var concurrent *int
	err := db.pool.QueryRow(ctx,
		`select exclusive_at_start, concurrent_at_start from jobs where id = $1`, jobID).
		Scan(&exclusive, &concurrent)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, ErrJobNotFound
		}
		return false, 0, fmt.Errorf("conditions de démarrage du job %d: %w", jobID, err)
	}

	// Non relevées : la mesure est traitée comme non exclusive. Se tromper dans ce
	// sens écarte une mesure valide de la comparaison ; dans l'autre, il ferait
	// entrer une mesure faussée dans la baseline.
	if exclusive == nil || concurrent == nil {
		return false, 0, nil
	}
	return *exclusive, *concurrent, nil
}

func scanMeasures(rows pgx.Rows, context string) ([]BenchMeasure, error) {
	var measures []BenchMeasure
	for rows.Next() {
		var measure BenchMeasure
		if err := rows.Scan(&measure.ID, &measure.ResultID, &measure.Scenario, &measure.Metric,
			&measure.Label, &measure.Unit, &measure.Value, &measure.LowerBetter,
			&measure.Volume, &measure.ThresholdOK, &measure.CreatedAt,
			&measure.Exclusive, &measure.ConcurrentAt); err != nil {
			return nil, fmt.Errorf("%s: %w", context, err)
		}
		measures = append(measures, measure)
	}
	return measures, rows.Err()
}

// LoadResultExists dit si une campagne est déjà enregistrée pour ce run et ce
// scénario.
//
// L'ingestion doit être idempotente : l'observateur revoit un run terminé au
// redémarrage, ou quand un informer resynchronise son cache. Réenregistrer
// produirait des points d'historique en double, c'est-à-dire une tendance fausse.
func (db *DB) LoadResultExists(ctx context.Context, runID int64, scenario string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`select exists(
		     select 1 from test_results r
		     join bench_measures m on m.test_result_id = r.id
		     where r.run_id = $1 and r.family = 'load' and m.scenario = $2
		 )`, runID, scenario).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("campagne %q du run %d: %w", scenario, runID, err)
	}
	return exists, nil
}
