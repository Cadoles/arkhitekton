package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// migrated returns a database at the current schema. Every test in this file works on
// runs, with no dependency on a reference dataset.
func migratedBare(t *testing.T) *DB {
	t.Helper()

	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func runningRecord(name string) RunRecord {
	started := time.Now().Add(-2 * time.Minute)
	return RunRecord{
		Name:      name,
		Namespace: "mse-tekton-pipelines",
		Pipeline:  "php-static-tests",
		Status:    "running",
		Reason:    "Running",
		StartedAt: &started,
		Observed:  true,
	}
}

func TestUpsertRunIsIdempotent(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	first, err := db.UpsertRun(ctx, runningRecord("run-idempotent"))
	if err != nil {
		t.Fatalf("premier enregistrement: %v", err)
	}
	second, err := db.UpsertRun(ctx, runningRecord("run-idempotent"))
	if err != nil {
		t.Fatalf("second enregistrement: %v", err)
	}
	if first != second {
		t.Errorf("different identifiers: %d then %d", first, second)
	}
}

// The projection's central invariant. An informer resync replays every object in
// arbitrary order: without this guard a finished run would return to `running` and
// freeze its exclusion class, with nothing to explain why the queue stopped advancing.
func TestTerminalStatusIsNeverOverwritten(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-terminal")); err != nil {
		t.Fatalf("enregistrement initial: %v", err)
	}

	completed := time.Now()
	final := runningRecord("run-terminal")
	final.Status = "failed"
	final.Reason = "Failed"
	final.CompletedAt = &completed
	if _, err := db.UpsertRun(ctx, final); err != nil {
		t.Fatalf("transition to failed: %v", err)
	}

	// The late event: same object, earlier state.
	if _, err := db.UpsertRun(ctx, runningRecord("run-terminal")); err != nil {
		t.Fatalf("late event: %v", err)
	}

	var status, reason string
	var completedAt *time.Time
	err := db.Pool().QueryRow(ctx,
		`select status, coalesce(reason, ''), completed_at from runs where pipelinerun_name = 'run-terminal'`).
		Scan(&status, &reason, &completedAt)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if status != "failed" {
		t.Errorf("status = %q, expected failed: a terminal state was overwritten", status)
	}
	if reason != "Failed" {
		t.Errorf("reason = %q, expected Failed", reason)
	}
	if completedAt == nil {
		t.Error("completed_at cleared by an older event")
	}
}

func TestTaskRunAttachesToItsRun(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-with-task")); err != nil {
		t.Fatalf("run: %v", err)
	}

	record := TaskRunRecord{
		RunName:     "run-with-task",
		Name:        "run-with-task-unit-test",
		DisplayName: "unit-test",
		Status:      "running",
		PodName:     "run-with-task-unit-test-pod",
	}
	if err := db.UpsertTaskRun(ctx, record); err != nil {
		t.Fatalf("task: %v", err)
	}

	var count int
	if err := db.Pool().QueryRow(ctx,
		`select count(*) from task_runs t join runs r on r.id = t.run_id
		 where r.pipelinerun_name = 'run-with-task'`).Scan(&count); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if count != 1 {
		t.Errorf("%d task(s) attached, expected 1", count)
	}
}

// A TaskRun may arrive before its PipelineRun: the informer guarantees no order between
// the two resources. The write must then do nothing, without error — the next event will
// pick it up.
func TestTaskRunWithoutItsRunIsIgnoredWithoutError(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	err := db.UpsertTaskRun(ctx, TaskRunRecord{
		RunName: "run-jamais-vu",
		Name:    "run-jamais-vu-task",
		Status:  "running",
	})
	if err != nil {
		t.Fatalf("an orphaned task must not produce an error: %v", err)
	}

	var count int
	if err := db.Pool().QueryRow(ctx, `select count(*) from task_runs`).Scan(&count); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if count != 0 {
		t.Errorf("%d task(s) written with no parent run", count)
	}
}

// The pod disappears before the TaskRun, and a late event no longer carries it. Clearing
// it would make the logs unreadable while they may still be capturable — or already
// sealed under a key derived from the pod.
func TestPodNameIsNeverErased(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-pod")); err != nil {
		t.Fatalf("run: %v", err)
	}

	withPod := TaskRunRecord{
		RunName: "run-pod", Name: "run-pod-task", Status: "running",
		PodName: "run-pod-task-pod",
	}
	if err := db.UpsertTaskRun(ctx, withPod); err != nil {
		t.Fatalf("task with a pod: %v", err)
	}

	withoutPod := TaskRunRecord{RunName: "run-pod", Name: "run-pod-task", Status: "running"}
	if err := db.UpsertTaskRun(ctx, withoutPod); err != nil {
		t.Fatalf("task with no pod: %v", err)
	}

	var podName *string
	if err := db.Pool().QueryRow(ctx,
		`select pod_name from task_runs where name = 'run-pod-task'`).Scan(&podName); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if podName == nil || *podName != "run-pod-task-pod" {
		t.Errorf("pod_name = %v, expected to be kept", podName)
	}
}

func TestUnfinishedRunsExcludesTerminalOnes(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	completed := time.Now()
	for name, status := range map[string]string{
		"run-en-cours":   "running",
		"run-en-attente": "pending",
		"run-reussi":     "succeeded",
		"run-annule":     "cancelled",
	} {
		record := runningRecord(name)
		record.Status = status
		if status == "succeeded" || status == "cancelled" {
			record.CompletedAt = &completed
		}
		if _, err := db.UpsertRun(ctx, record); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	unfinished, err := db.UnfinishedRuns(ctx, "mse-tekton-pipelines")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(unfinished) != 2 {
		t.Fatalf("%d unfinished run(s), expected 2: %+v", len(unfinished), unfinished)
	}
}

// Concluding a vanished run, the heart of reconciliation at start-up.
func TestMarkRunVanishedConcludesTheRun(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id, err := db.UpsertRun(ctx, runningRecord("run-disparu"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	const reason = "PipelineRunDisparu"
	if err := db.MarkRunVanished(ctx, id, reason); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	var status, got string
	var completedAt *time.Time
	if err := db.Pool().QueryRow(ctx,
		`select status, coalesce(reason, ''), completed_at from runs where id = $1`, id).
		Scan(&status, &got, &completedAt); err != nil {
		t.Fatalf("read back: %v", err)
	}

	if status != "failed" {
		t.Errorf("status = %q, expected failed", status)
	}
	// The reason must say "vanished" and not "failed": the distinction avoids sending
	// someone hunting for a log that never existed.
	if got != reason {
		t.Errorf("reason = %q, expected %q", got, reason)
	}
	if completedAt == nil {
		t.Error("completed_at not set")
	}
}

// A run already concluded must not be rewritten by reconciliation: its real reason is
// worth more than "vanished".
func TestMarkRunVanishedLeavesTerminalRunsAlone(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	completed := time.Now()
	record := runningRecord("run-deja-conclu")
	record.Status = "succeeded"
	record.Reason = "Completed"
	record.CompletedAt = &completed
	id, err := db.UpsertRun(ctx, record)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if err := db.MarkRunVanished(ctx, id, "PipelineRunDisparu"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	var status, reason string
	if err := db.Pool().QueryRow(ctx,
		`select status, coalesce(reason, '') from runs where id = $1`, id).Scan(&status, &reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "succeeded" || reason != "Completed" {
		t.Errorf("terminal run rewritten: status = %q, reason = %q", status, reason)
	}
}

// A run Tekton has not yet reconciled has no start time. It must appear at the top of
// the list and not at the bottom, otherwise the run just launched is the hardest to
// find.
func TestListRunsPlacesUnstartedRunsFirst(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	old := runningRecord("run-ancien")
	ancient := time.Now().Add(-24 * time.Hour)
	old.StartedAt = &ancient
	if _, err := db.UpsertRun(ctx, old); err != nil {
		t.Fatalf("run ancien: %v", err)
	}

	fresh := runningRecord("brand-new-run")
	fresh.Status = "pending"
	fresh.StartedAt = nil
	if _, err := db.UpsertRun(ctx, fresh); err != nil {
		t.Fatalf("run neuf: %v", err)
	}

	runs, err := db.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("%d run(s), expected 2", len(runs))
	}
	if runs[0].Name != "brand-new-run" {
		t.Errorf("first run = %q, expected the brand-new run", runs[0].Name)
	}
}

func TestUpsertRunStoresResultsAndJobID(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	record := runningRecord("run-resultats")
	record.Status = "succeeded"
	completed := time.Now()
	record.CompletedAt = &completed
	record.Results = map[string]string{"tag": "1.8.0-rc1"}
	record.JobID = insertJob(t, db)
	record.Observed = false

	if _, err := db.UpsertRun(ctx, record); err != nil {
		t.Fatalf("run: %v", err)
	}

	var results map[string]string
	var jobID *int64
	var observed bool
	if err := db.Pool().QueryRow(ctx,
		`select results, job_id, observed from runs where pipelinerun_name = 'run-resultats'`).
		Scan(&results, &jobID, &observed); err != nil {
		t.Fatalf("read back: %v", err)
	}

	if results["tag"] != "1.8.0-rc1" {
		t.Errorf("results = %v", results)
	}
	if jobID == nil || *jobID != record.JobID {
		t.Errorf("job_id = %v, expected %d", jobID, record.JobID)
	}
	if observed {
		t.Error("a run carrying a job_id must not be marked observed")
	}
}

// insertJob creates a minimal job. The runs.job_id column references it: a test that
// invented an identifier would exercise the constraint, not the projection.
func insertJob(t *testing.T, db *DB) int64 {
	t.Helper()

	var id int64
	err := db.Pool().QueryRow(context.Background(),
		`insert into jobs (kind, queue_class, state, params, requested_by)
		 values ('php-static-tests', 'parallel', 'running', '{}'::jsonb, 'test')
		 returning id`).Scan(&id)
	if err != nil {
		t.Fatalf("creating the job: %v", err)
	}
	return id
}

// A job label pointing at a non-existent job must not cost the observation: the
// referential constraint would fail the insert, and the run would appear nowhere. It is
// recorded without a link, hence read-only.
func TestUnknownJobIDDoesNotLoseTheRun(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	record := runningRecord("run-job-fantome")
	record.JobID = 999999
	record.Observed = false

	if _, err := db.UpsertRun(ctx, record); err != nil {
		t.Fatalf("an unknown job must not fail the recording: %v", err)
	}

	var jobID *int64
	var observed bool
	if err := db.Pool().QueryRow(ctx,
		`select job_id, observed from runs where pipelinerun_name = 'run-job-fantome'`).
		Scan(&jobID, &observed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if jobID != nil {
		t.Errorf("job_id = %v, expected null", *jobID)
	}
	if !observed {
		t.Error("a run with no valid job link must be read-only")
	}
}

func TestGetRunReturnsItsTasksInExecutionOrder(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-detail")); err != nil {
		t.Fatalf("run: %v", err)
	}

	early := time.Now().Add(-3 * time.Minute)
	late := time.Now().Add(-time.Minute)
	tasks := []TaskRunRecord{
		// Inserted in reverse execution order, and named so that alphabetical order does not
		// save the test.
		{RunName: "run-detail", Name: "run-detail-zebre", DisplayName: "zebre", Status: "running", StartedAt: &late},
		{RunName: "run-detail", Name: "run-detail-alpha", DisplayName: "alpha", Status: "succeeded", StartedAt: &early},
		{RunName: "run-detail", Name: "run-detail-attente", DisplayName: "attente", Status: "pending"},
	}
	for _, task := range tasks {
		if err := db.UpsertTaskRun(ctx, task); err != nil {
			t.Fatalf("task %s: %v", task.Name, err)
		}
	}

	detail, err := db.GetRun(ctx, "run-detail")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Run.Name != "run-detail" {
		t.Errorf("run = %q", detail.Run.Name)
	}
	if len(detail.Tasks) != 3 {
		t.Fatalf("%d task(s), expected 3", len(detail.Tasks))
	}

	order := []string{detail.Tasks[0].DisplayName, detail.Tasks[1].DisplayName, detail.Tasks[2].DisplayName}
	expected := []string{"alpha", "zebre", "attente"}
	for i := range expected {
		if order[i] != expected[i] {
			t.Errorf("order = %v, expected %v (the unstarted task must end the list)", order, expected)
			break
		}
	}
}

// A run purged by clean-up cron jobs is a normal case: the error must be
// distinguishable so the interface answers 404 rather than 500.
func TestGetRunDistinguishesAbsence(t *testing.T) {
	db := migrated(t)

	_, err := db.GetRun(context.Background(), "run-that-does-not-exist")
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("error = %v, expected ErrRunNotFound", err)
	}
}

func TestRunsUpdatedSinceReturnsOnlyChanges(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertRun(ctx, runningRecord("run-avant")); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, watermark, err := db.RunsUpdatedSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Nothing changed: the stream must retransmit nothing, otherwise every turn would
	// resend hundreds of frozen runs.
	unchanged, _, err := db.RunsUpdatedSince(ctx, watermark)
	if err != nil {
		t.Fatalf("empty read: %v", err)
	}
	if len(unchanged) != 0 {
		t.Errorf("%d run(s) retransmitted with no change", len(unchanged))
	}

	if _, err := db.UpsertRun(ctx, runningRecord("run-apres")); err != nil {
		t.Fatalf("second run: %v", err)
	}

	changed, _, err := db.RunsUpdatedSince(ctx, watermark)
	if err != nil {
		t.Fatalf("read after the change: %v", err)
	}
	if len(changed) != 1 || changed[0].Name != "run-apres" {
		t.Errorf("changements = %+v", changed)
	}
}

// testProjectID is the project created by [migrated]. A constant and not a variable: the
// schema is recreated for each test, and the sequence restarts at one.
const testProjectID int64 = 1

// testProjectSlug is the name that project carries in URLs.
const testProjectSlug = "test"

// migrated returns a database at the current schema, with a host project.
//
// Without one, no version and no release can exist. Created here rather than in every
// test: it is a prerequisite of the schema, not a subject under test. Tests that count
// projects — bootstrap, repository administration — take [migratedBare], which creates
// none.
//
// The identifier is predictable because `resetSchema` recreates the schema: the sequence
// restarts at one.
func migrated(t *testing.T) *DB {
	t.Helper()

	db := migratedBare(t)
	if _, err := db.Pool().Exec(context.Background(),
		// With a tracker: most of these tests are about matching releases with a project
		// management tool's versions, which only means something for a tracked project. The
		// untracked case has its own test.
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context,
		                       tracker_project, created_by)
		 values ($1, 'vcarroy/test', 'Test', 'https://forge.example', 'Dockerfile', '.',
		         'projet-suivi', 'test')`,
		testProjectSlug); err != nil {
		t.Fatalf("test project: %v", err)
	}
	return db
}
