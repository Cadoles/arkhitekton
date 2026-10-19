package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TestResult is one tool's result on a run.
type TestResult struct {
	ID        int64
	RunID     int64
	ReleaseID *int64
	Family    string
	// Producer is the plugin version that read the run, `plugin@version`. Shape is how the result is
	// stored and compared: `cases`, `findings` or `measures`. Group is where it is shown. Capability
	// is what the run instantiated, empty for a run the dashboard did not launch.
	Producer   string
	Shape      string
	Group      string
	Capability string
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
	// RunName is the name of the PipelineRun that produced this report. Empty if the run
	// was purged from the projection — the report outlives its run.
	RunName string
}

// TestCase is an individual case.
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

// SaveTestResult records a report and its cases.
//
// In one transaction: a report missing half its cases would make the comparison with
// the previous release conclude that tests were deleted, which is exactly the signal
// this is meant to make reliable.
func (db *DB) SaveTestResult(ctx context.Context, result TestResult, cases []TestCase) (int64, error) {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("test results: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if result.Group == "" {
		result.Group = result.Family
	}
	var id int64
	err = transaction.QueryRow(ctx,
		`insert into test_results (
		     run_id, release_id, family, tool, status, total, passed, failed, skipped,
		     flaky, duration_ms, summary, raw_object, producer, shape, fact_group, capability
		 ) values ($1, $2, nullif($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, nullif($12, ''), nullif($13, ''),
		           $14, $15, $16, coalesce(nullif($17, ''),
		               (select j.capability from runs r join jobs j on j.id = r.job_id where r.id = $1)))
		 returning id`,
		result.RunID, result.ReleaseID, result.Family, result.Tool, result.Status,
		result.Total, result.Passed, result.Failed, result.Skipped, result.Flaky,
		result.DurationMS, result.Summary, result.RawObject,
		result.Producer, result.Shape, result.Group, result.Capability).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("results %q of run %d: %w", result.Tool, result.RunID, err)
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
		return 0, fmt.Errorf("test results: %w", err)
	}
	return id, nil
}

// TestResultExists says whether a tool is already recorded for this run.
//
// Ingestion must be idempotent: the observer sees a finished run again at restart,
// and recording it twice would double every case — hence every comparison.
func (db *DB) TestResultExists(ctx context.Context, runID int64, tool string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`select exists(select 1 from test_results where run_id = $1 and tool = $2)`,
		runID, tool).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("results %q of run %d: %w", tool, runID, err)
	}
	return exists, nil
}

// RunHasResults says whether a run left any result at all.
//
// The question is about the whole run and not one tool, because what it observes is a
// breach of the contract: a pipeline announcing results and leaving none has not
// honoured it, and the screen will stay empty with nothing saying why.
//
// It covers the three entry points of a result — ingested reports, normalised audits,
// and the `na` values inferred from a marker — which a counter kept by a single
// ingester could not.
func (db *DB) RunHasResults(ctx context.Context, runID int64) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`select exists(select 1 from test_results where run_id = $1)`, runID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("results of run %d: %w", runID, err)
	}
	return exists, nil
}

// ListReleaseTestResults returns a release's reports.
func (db *DB) ListReleaseTestResults(ctx context.Context, releaseID int64) ([]TestResult, error) {
	rows, err := db.pool.Query(ctx,
		// The run's name is joined rather than resolved afterwards: it serves to send the
		// reader back to the run that produced the report, and a second query per report for
		// a single field is not justified.
		`select t.id, t.run_id, t.release_id, t.family, t.tool, t.status,
		        coalesce(t.total, 0), coalesce(t.passed, 0), coalesce(t.failed, 0),
		        coalesce(t.skipped, 0), coalesce(t.flaky, 0), coalesce(t.duration_ms, 0),
		        coalesce(t.summary, ''), coalesce(t.raw_object, ''), t.created_at,
		        coalesce(r.pipelinerun_name, '')
		 from test_results t
		 left join runs r on r.id = t.run_id
		 where t.release_id = $1 and t.family <> 'load'
		 order by t.created_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("results of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var results []TestResult
	for rows.Next() {
		var result TestResult
		if err := rows.Scan(&result.ID, &result.RunID, &result.ReleaseID, &result.Family,
			&result.Tool, &result.Status, &result.Total, &result.Passed, &result.Failed,
			&result.Skipped, &result.Flaky, &result.DurationMS, &result.Summary,
			&result.RawObject, &result.CreatedAt, &result.RunName); err != nil {
			return nil, fmt.Errorf("results of release %d: %w", releaseID, err)
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// CasePage bounds a read of test cases.
//
// An end-to-end suite commonly produces several thousand cases, and returning them all
// costs PostgreSQL as much as the browser. Pagination is server-side: doing it in the
// client would presume everything had been transferred, that is, that nothing had been
// saved.
type CasePage struct {
	// OnlyFailures restricts to failed or flaky cases.
	OnlyFailures bool
	// Query filters on the suite and the name together.
	//
	// The filter is server-side for the same reason as the pagination, and not only by
	// symmetry: filtering the page already transferred would answer "no case matches" for
	// a test that exists two pages further on. A filter that sees only a fraction of the
	// data does not filter, it misleads.
	Query string
	// A zero or negative Limit returns everything: pagination is the caller's choice, not
	// a constraint of the store.
	Limit  int
	Offset int
}

// ListTestCases returns a page of a report's cases, and the total before slicing.
//
// The total is counted separately rather than with `count(*) over ()`. A window
// returns nothing when the page is empty, which happens as soon as an offset exceeds
// the total — the interface would then lose the one figure that lets it go back.
func (db *DB) ListTestCases(ctx context.Context, resultID int64, page CasePage) ([]TestCase, int, error) {
	filter := `from test_cases where test_result_id = $1`
	args := []any{resultID}
	if page.OnlyFailures {
		filter += ` and status in ('failed', 'flaky')`
	}
	if needle := strings.TrimSpace(page.Query); needle != "" {
		// `ilike` on the concatenation: the user types a fragment without knowing whether it
		// belongs to the suite or to the case's name.
		args = append(args, "%"+needle+"%")
		filter += fmt.Sprintf(` and (suite || ' ' || name) ilike $%d`, len(args))
	}

	// The total covers the filtered set, not the whole report: it is what must say "12
	// cases" when a filter keeps twelve out of three thousand.
	var total int
	if err := db.pool.QueryRow(ctx, `select count(*) `+filter, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("total cases of report %d: %w", resultID, err)
	}

	// `id` as the last sort criterion, and it is not decorative: without a total order,
	// two cases of the same name can swap between two queries, which makes one appear
	// twice and the other disappear when turning a page.
	query := `select id, test_result_id, suite, name, status, coalesce(duration_ms, 0),
	                 coalesce(failure_message, ''), coalesce(failure_trace, '')
	          ` + filter + ` order by suite, name, id`
	if page.Limit > 0 {
		query += fmt.Sprintf(` limit $%d offset $%d`, len(args)+1, len(args)+2)
		args = append(args, page.Limit, max(page.Offset, 0))
	}

	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("cases of report %d: %w", resultID, err)
	}
	defer rows.Close()

	var cases []TestCase
	for rows.Next() {
		var testCase TestCase
		if err := rows.Scan(&testCase.ID, &testCase.TestResultID, &testCase.Suite,
			&testCase.Name, &testCase.Status, &testCase.DurationMS,
			&testCase.FailureMessage, &testCase.FailureTrace); err != nil {
			return nil, 0, fmt.Errorf("cases of report %d: %w", resultID, err)
		}
		cases = append(cases, testCase)
	}
	return cases, total, rows.Err()
}

// CaseDiff is a family's comparison with the previous release.
//
// It is the view that answers the question actually asked before shipping — "what
// changed?" — and not "how many tests pass". A stable counter can
type CaseDiff struct {
	Suite  string
	Name   string
	Status string
	// Was is the status in the release compared against. Empty when the case did not exist
	// there — that is, a test that was added.
	Was string
	// Change is `new-failure`, `fixed`, `added`, `removed` or `unchanged`.
	Change string
}

// Kinds of change between two releases.
const (
	ChangeNewFailure = "new-failure"
	ChangeFixed      = "fixed"
	ChangeAdded      = "added"
	ChangeRemoved    = "removed"
	ChangeUnchanged  = "unchanged"
)

// DiffFamily compares a test family between two releases.
//
// Compared in the database and not in Go: the two sets of cases may number thousands
// of entries, and fetching them to pair them in memory would move the whole history
// only to keep the differences.
//
// The `full outer join` is what makes the four kinds of change visible at once: an
// inner join would lose the added and removed tests, which are precisely the ones the
// eye does not catch.
func (db *DB) DiffFamily(ctx context.Context, releaseID, previousID int64, family string) ([]CaseDiff, error) {
	const query = `
		with current_cases as (
		    select distinct on (c.suite, c.name) c.suite, c.name, c.status
		    from test_cases c
		    join test_results r on r.id = c.test_result_id
		    where r.release_id = $1 and r.family = $3
		    -- The most recent report prevails: a release may be tested several
		    -- times, and the last run is what describes it.
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
		return nil, fmt.Errorf("comparing family %q: %w", family, err)
	}
	defer rows.Close()

	var diffs []CaseDiff
	for rows.Next() {
		var diff CaseDiff
		if err := rows.Scan(&diff.Suite, &diff.Name, &diff.Status, &diff.Was, &diff.Change); err != nil {
			return nil, fmt.Errorf("comparing family %q: %w", family, err)
		}
		diffs = append(diffs, diff)
	}
	return diffs, rows.Err()
}

// PreviousRelease returns a version's previous release, by creation date.
//
// Compared with the previous one and not with the last final release: a candidate is
// compared with the candidate before it, otherwise `rc2` would be set against a
// release shipped weeks earlier and everything would look like a change.
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

// ReprocessTarget is a run whose results can be replayed.
type ReprocessTarget struct {
	RunID     int64
	RunName   string
	ReleaseID *int64
	// Tools names what has already been projected for this run. It serves the log:
	// knowing what disappears before rebuilding it is the only way to observe that a
	// fixed parser now produces less, and not more.
	Tools []string
}

// ListReprocessTargets returns the runs whose raw artifacts are known.
//
// The selection starts from `raw_object` and not from the list of runs: a run whose
// source object is not recorded cannot be replayed, and offering it would give a
// replay that erases without rebuilding.
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

// DeleteTestResults erases a run's results.
//
// Erase then rebuild, rather than add: a replay that stacked would duplicate every
// case and skew every cross-release comparison, which is precisely what the replay
// exists to fix.
//
// The cases, findings and measures go with them, by cascade. That is intended: they
// make no sense detached from the report that produced them.
func (db *DB) DeleteTestResults(ctx context.Context, runID int64) (int64, error) {
	tally, err := db.pool.Exec(ctx, `delete from test_results where run_id = $1`, runID)
	if err != nil {
		return 0, fmt.Errorf("erasing the results of run %d: %w", runID, err)
	}
	return tally.RowsAffected(), nil
}
