package store

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
)

// One run's facts, in their three shapes, land in one transaction and mark the run as read; a
// second reading of the same run writes nothing.
func TestARunsFactsAreStoredOnce(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	runID, err := db.UpsertRun(ctx, runningRecord("run-facts"))
	if err != nil {
		t.Fatal(err)
	}
	code := int32(1)
	if err := db.UpsertTaskRun(ctx, TaskRunRecord{RunName: "run-facts", Name: "run-facts-phpstan",
		DisplayName: "run-phpstan", Status: "failed", Steps: []StepRecord{{Name: "exec", Status: "failed", ExitCode: &code}}}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := db.RunSnapshot(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Interpreted || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Name != "run-phpstan" ||
		*snapshot.Tasks[0].Steps[0].ExitCode != 1 {
		t.Errorf("snapshot = %+v", snapshot)
	}

	ok := true
	run := InterpretedRun{
		RunID: runID, Producer: facts.Producer{Plugin: "php", Version: "1.0.0"}, Capability: "php/static-tests",
		Exclusive: true,
		Sets: []facts.Set{
			{Shape: facts.ShapeCases, Group: "static", Tool: "phpstan",
				Cases: []facts.Case{{Suite: "src/A.php", Name: "rule", Status: facts.CaseFailed}}},
			{Shape: facts.ShapeFindings, Group: "security", Tool: "security-audit", Status: facts.StatusFail,
				Findings: &facts.Findings{Total: 1, BySeverity: map[facts.Severity]int{facts.SeverityCritical: 1},
					Items: []facts.Finding{{Fingerprint: "f1", Severity: facts.SeverityCritical, Title: "x"}}}},
			{Shape: facts.ShapeMeasures, Group: "load", Tool: "k6", Status: facts.StatusPass,
				Measures: &facts.Measures{Scenario: "checkout",
					Items: []facts.Measure{{Metric: "http_reqs:rate", Value: 4, ThresholdOK: &ok}}}},
		},
	}
	if err := db.SaveFacts(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFacts(ctx, run); !errors.Is(err, ErrAlreadyInterpreted) {
		t.Errorf("second reading: %v", err)
	}

	var rows, cases, findings, measures int
	var producer, shapes string
	if err := db.Pool().QueryRow(ctx,
		`select count(*), min(producer), string_agg(shape || ':' || status, ',' order by id)
		   from test_results where run_id = $1`, runID).Scan(&rows, &producer, &shapes); err != nil {
		t.Fatal(err)
	}
	_ = db.Pool().QueryRow(ctx, `select count(*) from test_cases c join test_results r on r.id = c.test_result_id
		where r.run_id = $1`, runID).Scan(&cases)
	_ = db.Pool().QueryRow(ctx, `select count(*) from security_findings f join test_results r on r.id = f.test_result_id
		where r.run_id = $1`, runID).Scan(&findings)
	_ = db.Pool().QueryRow(ctx, `select count(*) from bench_measures m join test_results r on r.id = m.test_result_id
		where r.run_id = $1`, runID).Scan(&measures)

	if rows != 3 || producer != "php@1.0.0" || shapes != "cases:fail,findings:fail,measures:pass" {
		t.Errorf("results = %d, %q, %q", rows, producer, shapes)
	}
	if cases != 1 || findings != 1 || measures != 1 {
		t.Errorf("cases %d, findings %d, measures %d", cases, findings, measures)
	}

	snapshot, _ = db.RunSnapshot(ctx, runID)
	if !snapshot.Interpreted {
		t.Error("run not marked as read")
	}
}
