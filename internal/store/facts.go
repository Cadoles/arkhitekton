package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
)

// ErrAlreadyInterpreted reports a run whose facts are already recorded.
var ErrAlreadyInterpreted = errors.New("run already interpreted")

// InterpretedRun is what reading one run produced.
type InterpretedRun struct {
	RunID     int64
	ReleaseID *int64
	Producer  facts.Producer
	// Capability is what the run instantiated.
	Capability string
	// Exclusive and ConcurrentJobs are the queue's state when the run started.
	Exclusive      bool
	ConcurrentJobs int
	Sets           []facts.Set
}

// SaveFacts records a run's facts and marks the run as read, in one transaction.
//
// The mark comes first, and is what makes this idempotent: two readings of one run, triggered by
// the PipelineRun and by its last TaskRun at the same moment, queue on the run's row, and the
// second finds it marked.
func (db *DB) SaveFacts(ctx context.Context, run InterpretedRun) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("facts of run %d: %w", run.RunID, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	marked, err := transaction.Exec(ctx,
		`update runs set interpreted_at = now(), interpreted_by = $2
		  where id = $1 and interpreted_at is null`, run.RunID, run.Producer.String())
	if err != nil {
		return fmt.Errorf("facts of run %d: %w", run.RunID, err)
	}
	if marked.RowsAffected() == 0 {
		return ErrAlreadyInterpreted
	}

	for _, set := range run.Sets {
		if err := saveSet(ctx, transaction, run, set); err != nil {
			return err
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("facts of run %d: %w", run.RunID, err)
	}
	return nil
}

func saveSet(ctx context.Context, transaction pgx.Tx, run InterpretedRun, set facts.Set) error {
	result := TestResult{
		RunID: run.RunID, ReleaseID: run.ReleaseID,
		// The legacy column keeps the group, read by the screens until they read the group.
		Family: set.Group, Group: set.Group, Tool: set.Tool, Shape: string(set.Shape),
		Status: string(set.Verdict()), DurationMS: set.DurationMS,
		Summary: set.Summary, RawObject: set.RawObject,
		Producer: run.Producer.String(), Capability: run.Capability,
		Exclusive: run.Exclusive, ConcurrentJobs: run.ConcurrentJobs,
	}

	switch set.Shape {
	case facts.ShapeFindings:
		if set.Findings != nil {
			result.Total = set.Findings.Total
			result.Failed = set.Findings.BySeverity[facts.SeverityCritical] + set.Findings.BySeverity[facts.SeverityHigh]
		}
		id, err := insertTestResult(ctx, transaction, result, nil)
		if err != nil || set.Findings == nil {
			return err
		}
		return insertSecurityAudit(ctx, transaction, auditOf(id, set.Findings), findingsOf(set.Findings))

	case facts.ShapeMeasures:
		if set.Measures == nil {
			_, err := insertTestResult(ctx, transaction, result, nil)
			return err
		}
		measures := make([]BenchMeasure, 0, len(set.Measures.Items))
		for _, measure := range set.Measures.Items {
			measures = append(measures, BenchMeasure{
				Metric: measure.Metric, Label: measure.Label, Unit: measure.Unit, Value: measure.Value,
				LowerBetter: measure.LowerBetter, Volume: measure.Volume, ThresholdOK: measure.ThresholdOK,
			})
		}
		_, err := insertLoadResult(ctx, transaction, LoadResult{
			RunID: run.RunID, ReleaseID: run.ReleaseID, Scenario: set.Measures.Scenario,
			Group: set.Group, Tool: set.Tool, Producer: result.Producer, Capability: run.Capability,
			Exclusive: run.Exclusive, ConcurrentJobs: run.ConcurrentJobs,
			DurationMS: set.DurationMS, Summary: set.Summary, RawObject: set.RawObject,
		}, measures, result.Status)
		return err

	default:
		counts := set.Counts()
		result.Total, result.Passed, result.Failed = counts.Total, counts.Passed, counts.Failed
		result.Skipped, result.Flaky = counts.Skipped, counts.Flaky
		cases := make([]TestCase, 0, len(set.Cases))
		for _, c := range set.Cases {
			cases = append(cases, TestCase{
				Suite: c.Suite, Name: c.Name, Status: string(c.Status), DurationMS: c.DurationMS,
				FailureMessage: c.Message, FailureTrace: c.Trace,
			})
		}
		_, err := insertTestResult(ctx, transaction, result, cases)
		return err
	}
}

func auditOf(resultID int64, findings *facts.Findings) SecurityAudit {
	return SecurityAudit{
		TestResultID: resultID, RunID: findings.RunID, GeneratedAt: findings.GeneratedAt,
		Total: findings.Total, Suppressed: findings.Suppressed,
		Critical: findings.BySeverity[facts.SeverityCritical], High: findings.BySeverity[facts.SeverityHigh],
		Medium: findings.BySeverity[facts.SeverityMedium], Low: findings.BySeverity[facts.SeverityLow],
		DeltaNew: findings.DeltaNew, DeltaExisting: findings.DeltaExisting, DeltaResolved: findings.DeltaResolved,
		FailedScanners: findings.FailedScanners, ScannerStatus: findings.ScannerStatus,
		Policy: findings.Policy, PreviousRunID: findings.PreviousRunID,
	}
}

func findingsOf(findings *facts.Findings) []SecurityFinding {
	stored := make([]SecurityFinding, 0, len(findings.Items))
	for _, f := range findings.Items {
		stored = append(stored, SecurityFinding{
			Fingerprint: f.Fingerprint, RunID: f.RunID, TargetKind: f.TargetKind, TargetName: f.TargetName,
			Tool: f.Tool, Category: f.Category, RuleID: f.RuleID, Severity: string(f.Severity), CVSS: f.CVSS,
			Title: f.Title, Description: f.Description, Location: f.Location,
			PackageName: f.PackageName, PackageInstalled: f.PackageInstalled, PackageFixed: f.PackageFixed,
			Fixable: f.Fixable, Status: f.Status, FirstSeen: f.FirstSeen, Suppressed: f.Suppressed,
			SuppressionReason: f.SuppressionReason, SuppressionExpires: f.SuppressionExpires,
		})
	}
	return stored
}

// RunSnapshot is what reading a run needs: its state, its tasks' outcomes, its job.
type RunSnapshot struct {
	ID          int64
	Name        string
	Pipeline    string
	Status      string
	Reason      string
	JobID       *int64
	ReleaseID   *int64
	StartedAt   *time.Time
	CompletedAt *time.Time
	Interpreted bool
	Tasks       []TaskSnapshot
}

// TaskSnapshot is one task's outcome. Name is the pipeline's task name, stable across runs.
type TaskSnapshot struct {
	Name   string
	Status string
	Reason string
	Steps  []StepRecord
}

// Terminal says whether the run and every task of it are over: reading earlier would take a
// task still running for one that never started.
func (s RunSnapshot) Terminal() bool {
	if !terminal(s.Status) {
		return false
	}
	for _, task := range s.Tasks {
		if !terminal(task.Status) {
			return false
		}
	}
	return true
}

func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timedout":
		return true
	}
	return false
}

// RunSnapshot reads a run for its interpretation.
func (db *DB) RunSnapshot(ctx context.Context, runID int64) (RunSnapshot, error) {
	var snapshot RunSnapshot
	err := db.pool.QueryRow(ctx,
		`select r.id, r.pipelinerun_name, r.pipeline_name, r.status, coalesce(r.reason, ''),
		        r.job_id, j.release_id, r.started_at, r.completed_at, r.interpreted_at is not null
		   from runs r left join jobs j on j.id = r.job_id
		  where r.id = $1`, runID).
		Scan(&snapshot.ID, &snapshot.Name, &snapshot.Pipeline, &snapshot.Status, &snapshot.Reason,
			&snapshot.JobID, &snapshot.ReleaseID, &snapshot.StartedAt, &snapshot.CompletedAt,
			&snapshot.Interpreted)
	if err != nil {
		return snapshot, fmt.Errorf("run %d: %w", runID, err)
	}

	rows, err := db.pool.Query(ctx,
		`select coalesce(nullif(display_name, ''), name), status, coalesce(reason, ''), steps
		   from task_runs where run_id = $1 order by id`, runID)
	if err != nil {
		return snapshot, fmt.Errorf("tasks of run %d: %w", runID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var task TaskSnapshot
		var steps []byte
		if err := rows.Scan(&task.Name, &task.Status, &task.Reason, &steps); err != nil {
			return snapshot, fmt.Errorf("tasks of run %d: %w", runID, err)
		}
		if err := json.Unmarshal(steps, &task.Steps); err != nil {
			return snapshot, fmt.Errorf("steps of task %q: %w", task.Name, err)
		}
		snapshot.Tasks = append(snapshot.Tasks, task)
	}
	return snapshot, rows.Err()
}

// RunIDByName returns a run's identifier from its PipelineRun's name.
func (db *DB) RunIDByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx, `select id from runs where pipelinerun_name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrRunNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("run %q: %w", name, err)
	}
	return id, nil
}
