package store

import (
	"context"
	"testing"
)

func loadFixture(t *testing.T, db *DB) (int64, int64) {
	t.Helper()
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	jobID, err := db.Enqueue(ctx, JobRequest{
		Kind: "load-test", RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("job: %v", err)
	}

	var runID int64
	err = db.pool.QueryRow(ctx,
		`insert into runs (job_id, pipeline_name, pipelinerun_name, namespace, status, started_at)
		 values ($1, 'load-test', $2, 'mse-tekton', 'succeeded', now()) returning id`,
		jobID, "load-test-1").Scan(&runID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return releaseID, runID
}

func yes() *bool { ok := true; return &ok }
func no() *bool  { ko := false; return &ko }

func TestSaveLoadResultKeepsTheMeasuredConditions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	resultID, err := db.SaveLoadResult(ctx,
		LoadResult{
			Group: "load", Tool: "k6", Producer: "k6@1.0.0",
			RunID: runID, ReleaseID: &releaseID, Scenario: "panier",
			Exclusive: true, ConcurrentJobs: 0, DurationMS: 60000, Summary: "60 s",
		},
		[]BenchMeasure{
			{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
				Value: 310.2, LowerBetter: true, ThresholdOK: yes()},
			{Metric: "http_reqs:count", Label: "Requests", Unit: "req", Value: 24000, Volume: true},
		})
	if err != nil {
		t.Fatalf("enregistrement: %v", err)
	}

	measures, err := db.ListResultMeasures(ctx, resultID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(measures) != 2 {
		t.Fatalf("%d mesure(s)", len(measures))
	}
	// The conditions accompany every measure: without them, a measure is only a number.
	if !measures[0].Exclusive || measures[0].ConcurrentAt != 0 {
		t.Errorf("conditions = %+v", measures[0])
	}
	if measures[0].ThresholdOK == nil || !*measures[0].ThresholdOK {
		t.Errorf("seuil = %v", measures[0].ThresholdOK)
	}
	if !measures[1].Volume {
		t.Error("the volume flag was lost")
	}
}

// The campaign's frozen status follows the thresholds declared in the load tool and not
// the baseline: a frozen column depending on a moving reference would become false at the
// first re-pinning.
func TestLoadStatusFollowsTheDeclaredThresholds(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	resultID, err := db.SaveLoadResult(ctx,
		LoadResult{Group: "load", Tool: "k6", Producer: "k6@1.0.0", RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{
			{Metric: "a:p(95)", Label: "A", Unit: "ms", Value: 1, LowerBetter: true, ThresholdOK: yes()},
			{Metric: "b:rate", Label: "B", Unit: "%", Value: 2, LowerBetter: true, ThresholdOK: no()},
		})
	if err != nil {
		t.Fatalf("enregistrement: %v", err)
	}

	var status string
	if err := db.pool.QueryRow(ctx, `select status from test_results where id = $1`, resultID).Scan(&status); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if status != "fail" {
		t.Errorf("status = %q, expected fail", status)
	}
}

// A campaign with no declared threshold pronounces no verdict on itself.
func TestLoadStatusWithoutDeclaredThresholds(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	resultID, _ := db.SaveLoadResult(ctx,
		LoadResult{Group: "load", Tool: "k6", Producer: "k6@1.0.0", RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{{Metric: "a:p(95)", Label: "A", Unit: "ms", Value: 1, LowerBetter: true}})

	var status string
	db.pool.QueryRow(ctx, `select status from test_results where id = $1`, resultID).Scan(&status)
	if status != "na" {
		t.Errorf("status = %q, expected na", status)
	}
}

func TestBaselinesArePinnedNotComputed(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	first, _ := db.SaveLoadResult(ctx,
		LoadResult{Group: "load", Tool: "k6", Producer: "k6@1.0.0", RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
			Value: 300, LowerBetter: true}})
	second, _ := db.SaveLoadResult(ctx,
		LoadResult{Group: "load", Tool: "k6", Producer: "k6@1.0.0", RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
		[]BenchMeasure{{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
			Value: 450, LowerBetter: true}})

	firstMeasures, _ := db.ListResultMeasures(ctx, first)
	secondMeasures, _ := db.ListResultMeasures(ctx, second)

	if err := db.PinBaseline(ctx, firstMeasures[0].ID, "vcarroy"); err != nil {
		t.Fatalf("pinning: %v", err)
	}

	baselines, err := db.Baselines(ctx, "panier")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The reference is the one that was designated, and a more recent campaign does not move
	// it by itself. That is what makes drift visible.
	if baselines["http_req_duration:p(95)"].Value != 300 {
		t.Errorf("baseline = %v, expected 300", baselines["http_req_duration:p(95)"].Value)
	}

	// Re-pinning is allowed: a baseline ages.
	if err := db.PinBaseline(ctx, secondMeasures[0].ID, "vcarroy"); err != nil {
		t.Fatalf("re-pinning: %v", err)
	}
	baselines, _ = db.Baselines(ctx, "panier")
	if baselines["http_req_duration:p(95)"].Value != 450 {
		t.Errorf("baseline = %v, expected 450", baselines["http_req_duration:p(95)"].Value)
	}
	if baselines["http_req_duration:p(95)"].PinnedBy != "vcarroy" {
		t.Error("the gesture must be recorded")
	}
}

func TestScenarioHistoryIsMostRecentFirst(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID, runID := loadFixture(t, db)

	for _, value := range []float64{300, 320, 290} {
		if _, err := db.SaveLoadResult(ctx,
			LoadResult{Group: "load", Tool: "k6", Producer: "k6@1.0.0", RunID: runID, ReleaseID: &releaseID, Scenario: "panier", Exclusive: true},
			[]BenchMeasure{{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
				Value: value, LowerBetter: true}}); err != nil {
			t.Fatalf("enregistrement: %v", err)
		}
	}

	history, err := db.ListScenarioMeasures(ctx, "panier", "http_req_duration:p(95)", 6)
	if err != nil {
		t.Fatalf("historique: %v", err)
	}
	if len(history) != 3 || history[0].Value != 290 {
		t.Errorf("historique = %+v", history)
	}
}

// Recorded at claim time: minutes later, nothing says what was running at the moment of
// the measurement.
func TestClaimRecordsTheStartConditions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "load-test", RequestedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	// A queue of one place: that is what makes the measurement exclusive, the previous model's
	// barrier having gone.
	limits := Limits{PerKind: map[string]int{"load-test": 1}, Default: 5, Global: 4}
	job, err := db.ClaimNext(ctx, limits, func(kind string, id int64) string { return "run-1" })
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	exclusive, concurrent, err := db.StartConditions(ctx, job.ID)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if !exclusive || concurrent != 0 {
		t.Errorf("conditions = exclusive %v, concurrents %d", exclusive, concurrent)
	}
}

// An ordinary job is not exclusive whatever happens: the class decides that, not the luck
// of having found the cluster empty.
func TestAnOrdinaryJobIsNeverMarkedExclusive(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "static-tests", RequestedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	job, _ := db.ClaimNext(ctx, caps(4, nil), func(kind string, id int64) string { return "run-2" })
	exclusive, _, err := db.StartConditions(ctx, job.ID)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if exclusive {
		t.Error("a job outside the barrier must never be marked exclusive")
	}
}

// Not recorded: treated as non-exclusive. Erring this way sets aside a valid measure;
// erring the other way would let a skewed measure into the comparison.
func TestUnrecordedConditionsDefaultToNonExclusive(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id, err := db.Enqueue(ctx, JobRequest{Kind: "x", RequestedBy: "v"})
	if err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	exclusive, concurrent, err := db.StartConditions(ctx, id)
	if err != nil {
		t.Fatalf("conditions: %v", err)
	}
	if exclusive || concurrent != 0 {
		t.Errorf("conditions = %v / %d", exclusive, concurrent)
	}
}
