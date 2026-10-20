package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LoadResult is a recorded load campaign.
type LoadResult struct {
	ID        int64
	RunID     int64
	ReleaseID *int64
	Scenario  string
	// Group, Tool and Producer describe the result like any other: where it is shown, what measured
	// it, which plugin version read it.
	Group      string
	Tool       string
	Producer   string
	Capability string
	// Exclusive and ConcurrentJobs are the conditions observed at start-up, not those
	// asked for. They are what decides whether the campaign enters the comparison with the
	// baseline.
	Exclusive      bool
	ConcurrentJobs int
	VUs            int
	DurationMS     int64
	Summary        string
	RawObject      string
	CreatedAt      time.Time
}

// BenchMeasure is one measure of a campaign.
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

// ErrBaselineNotFound reports a missing baseline.
var ErrBaselineNotFound = errors.New("baseline not found")

// SaveLoadResult records a campaign and its measures.
//
// In one transaction: a campaign missing half its measures would produce a false
// overall verdict — the worst value would be taken from what was recorded, without
// knowing some is missing.
func (db *DB) SaveLoadResult(ctx context.Context, result LoadResult, measures []BenchMeasure) (int64, error) {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("load campaign: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	id, err := insertLoadResult(ctx, transaction, result, measures, loadStatus(measures))
	if err != nil {
		return 0, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("load campaign: %w", err)
	}
	return id, nil
}

// insertLoadResult writes a campaign and its measures within a transaction.
func insertLoadResult(
	ctx context.Context, transaction pgx.Tx, result LoadResult, measures []BenchMeasure, status string,
) (int64, error) {
	var id int64
	err := transaction.QueryRow(ctx,
		`insert into test_results (
		     run_id, release_id, tool, status, duration_ms, summary, raw_object,
		     exclusive, concurrent_jobs, producer, shape, fact_group, capability
		 ) values ($1, $2, $10, $3, $4, $5, nullif($6, ''), $7, $8, $11, 'measures', $9,
		           coalesce(nullif($12, ''),
		               (select j.capability from runs r join jobs j on j.id = r.job_id where r.id = $1)))
		 returning id`,
		result.RunID, result.ReleaseID, status, result.DurationMS,
		result.Summary, result.RawObject, result.Exclusive, result.ConcurrentJobs,
		result.Group, result.Tool, result.Producer, result.Capability).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("load campaign of scenario %q: %w", result.Scenario, err)
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
			return 0, fmt.Errorf("measure %q of scenario %q: %w", measure.Metric, result.Scenario, err)
		}
	}
	return id, nil
}

// loadStatus returns the campaign's status in the sense of `test_results`.
//
// Based on the thresholds declared in the load tool, and not on the comparison with
// the baseline: the status is recorded once and for all, whereas a baseline can be
// re-pinned later. Making a frozen column depend on a moving reference would make it
// false at the first re-pinning.
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
		// No threshold declared: the campaign ran, but it pronounces no verdict on itself.
		return "na"
	}
	return "pass"
}

// ListScenarioMeasures returns a metric's history, newest first.
//
// The measurement conditions accompany each point: a trend mixing measures taken under
// different conditions is an invented trend, and it is for the caller to decide what to
// do with them.
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
		return nil, fmt.Errorf("history of %q/%q: %w", scenario, metric, err)
	}
	defer rows.Close()

	return scanMeasures(rows, fmt.Sprintf("history of %q/%q", scenario, metric))
}

// ListResultMeasures returns a campaign's measures.
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
		return nil, fmt.Errorf("measures of campaign %d: %w", resultID, err)
	}
	defer rows.Close()

	return scanMeasures(rows, fmt.Sprintf("measures of campaign %d", resultID))
}

// ListReleaseLoadResults returns the campaigns attached to a release.
func (db *DB) ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]LoadResult, error) {
	rows, err := db.pool.Query(ctx,
		`select r.id, r.run_id, r.release_id, coalesce(m.scenario, ''),
		        coalesce(r.exclusive, false), coalesce(r.concurrent_jobs, 0),
		        coalesce(r.duration_ms, 0), coalesce(r.summary, ''),
		        coalesce(r.raw_object, ''), r.created_at
		 from test_results r
		 -- The scenario lives on the measures: a campaign with no measure has
		 -- none, and the distinct avoids duplicating it.
		 left join lateral (
		     select scenario from bench_measures where test_result_id = r.id limit 1
		 ) m on true
		 where r.release_id = $1 and r.shape = 'measures'
		 order by r.created_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("campaigns of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var results []LoadResult
	for rows.Next() {
		var result LoadResult
		if err := rows.Scan(&result.ID, &result.RunID, &result.ReleaseID, &result.Scenario,
			&result.Exclusive, &result.ConcurrentJobs, &result.DurationMS,
			&result.Summary, &result.RawObject, &result.CreatedAt); err != nil {
			return nil, fmt.Errorf("campaigns of release %d: %w", releaseID, err)
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// Baseline is a pinned reference measure.
type Baseline struct {
	Scenario string
	Metric   string
	Value    float64
	// MeasuredAt is the reference campaign's date. Returned because a baseline ages:
	// knowing when it dates from is part of the verdict.
	MeasuredAt time.Time
	PinnedBy   string
	PinnedAt   time.Time
}

// PinBaseline designates a measure as the reference.
//
// Re-pinning is allowed and deliberately simple: a baseline ages, and refusing to move
// it would force comparing forever with a campaign whose conditions no longer exist.
// The gesture is recorded, though — it is a decision that moves the reference of every
// later verdict.
func (db *DB) PinBaseline(ctx context.Context, measureID int64, pinnedBy string) error {
	tally, err := db.pool.Exec(ctx,
		`insert into bench_baselines (scenario, metric, measure_id, pinned_by)
		 select scenario, metric, id, $2 from bench_measures where id = $1
		 on conflict (scenario, metric) do update set
		     measure_id = excluded.measure_id,
		     pinned_by = excluded.pinned_by,
		     pinned_at = now()`, measureID, pinnedBy)
	if err != nil {
		return fmt.Errorf("pinning baseline %d: %w", measureID, err)
	}
	if tally.RowsAffected() == 0 {
		return ErrBaselineNotFound
	}
	return nil
}

// Baselines returns a scenario's references, indexed by metric.
func (db *DB) Baselines(ctx context.Context, scenario string) (map[string]Baseline, error) {
	rows, err := db.pool.Query(ctx,
		`select b.scenario, b.metric, m.value, m.created_at, b.pinned_by, b.pinned_at
		 from bench_baselines b
		 join bench_measures m on m.id = b.measure_id
		 where b.scenario = $1`, scenario)
	if err != nil {
		return nil, fmt.Errorf("baselines of scenario %q: %w", scenario, err)
	}
	defer rows.Close()

	baselines := map[string]Baseline{}
	for rows.Next() {
		var baseline Baseline
		if err := rows.Scan(&baseline.Scenario, &baseline.Metric, &baseline.Value,
			&baseline.MeasuredAt, &baseline.PinnedBy, &baseline.PinnedAt); err != nil {
			return nil, fmt.Errorf("baselines of scenario %q: %w", scenario, err)
		}
		baselines[baseline.Metric] = baseline
	}
	return baselines, rows.Err()
}

// RecordStartConditions records what was running when a job was claimed.
//
// Written at claim time and not when the result arrives: minutes later, nothing says
// what was running at the moment of the measurement.
func (db *DB) RecordStartConditions(ctx context.Context, jobID int64, exclusive bool, concurrent int) error {
	_, err := db.pool.Exec(ctx,
		`update jobs set exclusive_at_start = $2, concurrent_at_start = $3, updated_at = now()
		 where id = $1`, jobID, exclusive, concurrent)
	if err != nil {
		return fmt.Errorf("start conditions of job %d: %w", jobID, err)
	}
	return nil
}

// StartConditions returns the conditions observed when a job started.
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
		return false, 0, fmt.Errorf("start conditions of job %d: %w", jobID, err)
	}

	// Not recorded: the measure is treated as non-exclusive. Erring this way keeps a valid
	// measure out of the comparison; erring the other way would let a skewed measure into
	// the baseline.
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

// LoadResultExists says whether a campaign is already recorded for this run and
// scenario.
//
// Ingestion must be idempotent: the observer sees a finished run again at restart, or
// when an informer resyncs its cache. Recording it twice would produce duplicate
// history points, that is, a false trend.
func (db *DB) LoadResultExists(ctx context.Context, runID int64, scenario string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`select exists(
		     select 1 from test_results r
		     join bench_measures m on m.test_result_id = r.id
		     where r.run_id = $1 and r.shape = 'measures' and m.scenario = $2
		 )`, runID, scenario).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("campaign %q of run %d: %w", scenario, runID, err)
	}
	return exists, nil
}
