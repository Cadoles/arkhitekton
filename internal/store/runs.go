package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunRecord is what a PipelineRun's projection writes to the database.
//
// The type is defined here and not in internal/tekton so that `store` does not depend
// on client-go: the persistence layer has no reason to know Kubernetes.
type RunRecord struct {
	Name        string
	Namespace   string
	Pipeline    string
	Status      string
	Reason      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Results     map[string]string
	JobID       int64
	Observed    bool
	// DeclaredTasks lists the pipeline's tasks as it declares them. It serves to show
	// what has not started: Tekton creates a TaskRun only when a task starts.
	DeclaredTasks []DeclaredTask
}

// DeclaredTask is a task as the pipeline declares it.
//
// Restated here rather than imported from `tekton`: this package knows nothing of
// Tekton, and the observer's adapter does the translation. It is the same rule that
// keeps `tekton` ignorant of PostgreSQL, and it is paid once — six copied fields —
// rather than in a cross-dependency between two packages that need not know each
// other.
type DeclaredTask struct {
	Name string `json:"name"`
	// DisplayName carries the `tekton.dev/pipelineTask` label, that is, the name of the
	// pipeline's task and not a human label. The name is an accident of history, and it
	// cost a defect: the same step displayed twice, once run under this name and once
	// "pending" under the declaration's label. This is what brings the two together.
	DisplayName string `json:"display_name,omitempty"`
	Finally     bool   `json:"finally,omitempty"`
}

// TaskRunRecord is a TaskRun's projection.
type TaskRunRecord struct {
	RunName     string
	Name        string
	DisplayName string
	Status      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	PodName     string
}

// terminalStatuses must stay aligned with tekton.Status.Terminal(). The duplication
// is accepted: removing it would force `store` to import the Kubernetes package, for
// a list of five strings.
const terminalStatuses = `('succeeded', 'failed', 'cancelled', 'timedout')`

// UpsertRun records or updates a run, and returns its identifier.
//
// Two protections live in the query, not in the calling code:
//
//  1. A terminal state never becomes transient again. Informer events do not arrive
//     in order — a periodic resync replays every object — and a finished run returning
//     to `running` would freeze its exclusion class with nothing to explain it.
//  2. Dates do not regress. A known `completed_at` is not cleared by an older event
//     that does not carry it yet.
//
// Putting them in SQL rather than in Go makes them true even under concurrent writes
// by two workers, which leader election makes rare but not impossible during a
// handover.
func (db *DB) UpsertRun(ctx context.Context, record RunRecord) (int64, error) {
	results, err := marshalResults(record.Results)
	if err != nil {
		return 0, err
	}

	var jobID any
	if record.JobID != 0 {
		jobID = record.JobID
	}

	const query = `
		insert into runs (
			pipeline_name, pipelinerun_name, namespace, status, reason,
			started_at, completed_at, results, observed, job_id, declared_tasks
		)
		-- job_id goes through a sub-query rather than being inserted as is. The
		-- column references the jobs table, and a PipelineRun carrying an unknown
		-- job identifier — a restored database, a label copied by hand — would
		-- fail the insert on the constraint. The observation would be lost for a
		-- referential integrity reason, which is the wrong trade: better a run
		-- displayed with no link to its job.
		--
		-- observed follows the same logic: with no valid link, the run stays
		-- read-only. That is the safe side — the dashboard does not claim to drive
		-- what it cannot attach.
		select $1, $2, $3, $4, $5, $6, $7, $8,
		       $9 or job.id is null,
		       job.id, $11
		from (select 1) as always
		left join (select id from jobs where id = $10) as job on true
		on conflict (pipelinerun_name) do update set
			status = case
				when runs.status in ` + terminalStatuses + ` then runs.status
				else excluded.status
			end,
			reason = case
				when runs.status in ` + terminalStatuses + ` then runs.reason
				else excluded.reason
			end,
			started_at   = coalesce(runs.started_at, excluded.started_at),
			completed_at = coalesce(runs.completed_at, excluded.completed_at),
			results      = coalesce(excluded.results, runs.results),
			job_id       = coalesce(runs.job_id, excluded.job_id),
			-- The declaration is not lost: Tekton removes it from a finished run's
			-- status, and a late redelivery would otherwise empty the list of a
			-- run qu'on regarde encore.
			declared_tasks = case
				when jsonb_array_length(excluded.declared_tasks) > 0 then excluded.declared_tasks
				else runs.declared_tasks
			end,
			updated_at   = now()
		returning id`

	declared, err := json.Marshal(record.DeclaredTasks)
	if err != nil {
		return 0, fmt.Errorf("declared tasks of run %q: %w", record.Name, err)
	}
	if record.DeclaredTasks == nil {
		declared = []byte("[]")
	}

	var id int64
	err = db.pool.QueryRow(ctx, query,
		record.Pipeline, record.Name, record.Namespace, record.Status, nullString(record.Reason),
		record.StartedAt, record.CompletedAt, results, record.Observed, jobID, declared,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("recording run %q: %w", record.Name, err)
	}
	return id, nil
}

// UpsertTaskRun records or updates a task, attached to its run.
//
// The attachment is by PipelineRun name: a TaskRun may be observed before its
// PipelineRun, the informer guaranteeing no order between the two resources. The task
// is then ignored and the next event — or the resync — will pick it up.
func (db *DB) UpsertTaskRun(ctx context.Context, record TaskRunRecord) error {
	const query = `
		insert into task_runs (
			run_id, name, display_name, status, started_at, completed_at, pod_name
		)
		select id, $2, $3, $4, $5, $6, $7 from runs where pipelinerun_name = $1
		on conflict (run_id, name) do update set
			status = case
				when task_runs.status in ` + terminalStatuses + ` then task_runs.status
				else excluded.status
			end,
			display_name = coalesce(excluded.display_name, task_runs.display_name),
			started_at   = coalesce(task_runs.started_at, excluded.started_at),
			completed_at = coalesce(task_runs.completed_at, excluded.completed_at),
			-- The pod's name must never be cleared: it is the only handle for
			-- reading the logs, and the pod disappears before the TaskRun.
			pod_name     = coalesce(task_runs.pod_name, excluded.pod_name),
			updated_at   = now()`

	_, err := db.pool.Exec(ctx, query,
		record.RunName, record.Name, nullString(record.DisplayName), record.Status,
		record.StartedAt, record.CompletedAt, nullString(record.PodName),
	)
	if err != nil {
		return fmt.Errorf("recording task %q: %w", record.Name, err)
	}
	return nil
}

// UnfinishedRun describes a run the database believes is in flight.
type UnfinishedRun struct {
	ID     int64
	Name   string
	Status string
}

// UnfinishedRuns lists a namespace's unfinished runs.
//
// The entry point of reconciliation at start-up: this list is what gets compared with
// what the cluster actually holds.
func (db *DB) UnfinishedRuns(ctx context.Context, namespace string) ([]UnfinishedRun, error) {
	rows, err := db.pool.Query(ctx,
		`select id, pipelinerun_name, status from runs
		 where namespace = $1 and status not in `+terminalStatuses+`
		 order by id`, namespace)
	if err != nil {
		return nil, fmt.Errorf("reading the unfinished runs: %w", err)
	}
	defer rows.Close()

	var runs []UnfinishedRun
	for rows.Next() {
		var run UnfinishedRun
		if err := rows.Scan(&run.ID, &run.Name, &run.Status); err != nil {
			return nil, fmt.Errorf("reading the unfinished runs: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// MarkRunVanished concludes a run whose PipelineRun disappeared from the cluster.
//
// Without that conclusion, a restart leaves runs stuck in `running` that freeze their
// exclusion class — the most likely failure mode of the whole arrangement. The reason
// is explicit: "vanished" is not "failed", and the distinction avoids sending someone
// hunting for a log that does not exist.
func (db *DB) MarkRunVanished(ctx context.Context, id int64, reason string) error {
	_, err := db.pool.Exec(ctx,
		`update runs
		 set status = 'failed', reason = $2,
		     completed_at = coalesce(completed_at, now()), updated_at = now()
		 where id = $1 and status not in `+terminalStatuses, id, reason)
	if err != nil {
		return fmt.Errorf("concluding run %d: %w", id, err)
	}
	return nil
}

// RunSummary is a run's list view.
type RunSummary struct {
	ID          int64
	Name        string
	Pipeline    string
	Status      string
	Reason      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Observed    bool
	// Namespace is the PipelineRun's. It serves to compose the link to the Tekton
	// dashboard, which files its objects by namespace.
	Namespace string
	// ReleaseTag and RequestedBy say where the run came from. Empty on an observed run —
	// triggered outside the dashboard, it has neither job nor requester — and that is
	// information in itself: nobody here launched it.
	//
	// Set by GetRun only: the list has no use for them, and the join would make every row
	// pay.
	ReleaseTag  string
	RequestedBy string
}

// ListRuns returns the most recent runs first.
func (db *DB) ListRuns(ctx context.Context, limit int) ([]RunSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := db.pool.Query(ctx,
		`select id, pipelinerun_name, pipeline_name, status, coalesce(reason, ''),
		        started_at, completed_at, observed, namespace
		 from runs
		 -- Runs with no start time — created but not yet reconciled by Tekton —
		 -- must appear at the head and not at the end of the list.
		 order by coalesce(started_at, created_at) desc
		 limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing the runs: %w", err)
	}
	defer rows.Close()

	var runs []RunSummary
	for rows.Next() {
		var run RunSummary
		if err := rows.Scan(&run.ID, &run.Name, &run.Pipeline, &run.Status, &run.Reason,
			&run.StartedAt, &run.CompletedAt, &run.Observed, &run.Namespace); err != nil {
			return nil, fmt.Errorf("listing the runs: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func marshalResults(results map[string]string) (any, error) {
	if len(results) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return nil, fmt.Errorf("serialising the results: %w", err)
	}
	return encoded, nil
}

// nullString avoids writing empty strings where absence is the right information: an
// empty `reason` and an unknown `reason` would otherwise be indistinguishable.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// SealTaskLog records a task's log object and marks it sealed.
//
// Called after the object is written, never before: the reverse order would announce
// a readable log nobody could open.
func (db *DB) SealTaskLog(ctx context.Context, runName, taskName, object string) error {
	_, err := db.pool.Exec(ctx,
		`update task_runs t
		 set log_object = $3, log_sealed = true, updated_at = now()
		 from runs r
		 where r.id = t.run_id and r.pipelinerun_name = $1 and t.name = $2`,
		runName, taskName, object)
	if err != nil {
		return fmt.Errorf("sealing the log of %q: %w", taskName, err)
	}
	return nil
}

// TaskLogLocation describes where to read a task's log.
type TaskLogLocation struct {
	RunName  string
	TaskName string
	PodName  string
	Object   string
	Sealed   bool
}

// TaskLog returns the location of a task's log.
func (db *DB) TaskLog(ctx context.Context, runName, taskName string) (TaskLogLocation, error) {
	location := TaskLogLocation{RunName: runName, TaskName: taskName}

	var podName, object *string
	err := db.pool.QueryRow(ctx,
		`select t.pod_name, t.log_object, t.log_sealed
		 from task_runs t join runs r on r.id = t.run_id
		 where r.pipelinerun_name = $1 and t.name = $2`, runName, taskName).
		Scan(&podName, &object, &location.Sealed)
	if err != nil {
		return location, fmt.Errorf("location of the log of %q: %w", taskName, err)
	}

	if podName != nil {
		location.PodName = *podName
	}
	if object != nil {
		location.Object = *object
	}
	return location, nil
}

// RunsUpdatedSince returns the runs modified since a given instant, and the instant
// of the most recent modification.
//
// It feeds the state stream: returning the whole list on every turn would retransmit
// hundreds of frozen runs for a single change.
func (db *DB) RunsUpdatedSince(ctx context.Context, since time.Time) ([]RunSummary, time.Time, error) {
	rows, err := db.pool.Query(ctx,
		`select id, pipelinerun_name, pipeline_name, status, coalesce(reason, ''),
		        started_at, completed_at, observed, namespace, updated_at
		 from runs
		 where updated_at > $1
		 order by updated_at
		 limit 200`, since)
	if err != nil {
		return nil, since, fmt.Errorf("modified runs: %w", err)
	}
	defer rows.Close()

	latest := since
	var runs []RunSummary
	for rows.Next() {
		var run RunSummary
		var updatedAt time.Time
		if err := rows.Scan(&run.ID, &run.Name, &run.Pipeline, &run.Status, &run.Reason,
			&run.StartedAt, &run.CompletedAt, &run.Observed, &run.Namespace,
			&updatedAt); err != nil {
			return nil, since, fmt.Errorf("modified runs: %w", err)
		}
		if updatedAt.After(latest) {
			latest = updatedAt
		}
		runs = append(runs, run)
	}
	return runs, latest, rows.Err()
}

// TaskSummary is a task's view within a run's detail.
type TaskSummary struct {
	Name        string
	DisplayName string
	Status      string
	StartedAt   *time.Time
	CompletedAt *time.Time
	PodName     string
	// LogSealed says the log is final. The interface uses it to decide whether to follow
	// the stream or settle for a read.
	LogSealed bool
	HasLog    bool
}

// RunDetail is a run and its tasks.
type RunDetail struct {
	Run   RunSummary
	Tasks []TaskSummary
	// Declared lists the pipeline's tasks, in graph order. Empty on a run projected before
	// that list was recorded: the caller then falls back to the started tasks alone.
	Declared []DeclaredTask
}

// ErrRunNotFound signale un run absent.
var ErrRunNotFound = errors.New("run introuvable")

// GetRun returns a run and its tasks, in execution order.
func (db *DB) GetRun(ctx context.Context, name string) (RunDetail, error) {
	var detail RunDetail

	var declared []byte
	// The joins are outer by design: an observed run has no job, and a synchronisation job
	// has no release. An inner join would make them vanish from the screen rather than
	// display them without provenance.
	err := db.pool.QueryRow(ctx,
		`select r.id, r.pipelinerun_name, r.pipeline_name, r.status, coalesce(r.reason, ''),
		        r.started_at, r.completed_at, r.observed, r.namespace, r.declared_tasks,
		        coalesce(j.requested_by, ''), coalesce(rel.tag, '')
		 from runs r
		 left join jobs j on j.id = r.job_id
		 left join releases rel on rel.id = j.release_id
		 where r.pipelinerun_name = $1`, name).
		Scan(&detail.Run.ID, &detail.Run.Name, &detail.Run.Pipeline, &detail.Run.Status,
			&detail.Run.Reason, &detail.Run.StartedAt, &detail.Run.CompletedAt,
			&detail.Run.Observed, &detail.Run.Namespace, &declared,
			&detail.Run.RequestedBy, &detail.Run.ReleaseTag)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return detail, ErrRunNotFound
		}
		return detail, fmt.Errorf("reading run %q: %w", name, err)
	}

	// An unreadable declaration does not prevent returning the run: the screen falls back
	// to the started tasks, which is the previous behaviour.
	if len(declared) > 0 {
		_ = json.Unmarshal(declared, &detail.Declared)
	}

	rows, err := db.pool.Query(ctx,
		`select name, coalesce(display_name, name), status, started_at, completed_at,
		        coalesce(pod_name, ''), log_sealed, log_object is not null
		 from task_runs
		 where run_id = $1
		 -- Tasks with no start time — not yet scheduled — go to the end of the
		 -- list: they have nothing to show, unlike runs where the most recent is
		 -- what one looks for.
		 order by started_at nulls last, name`, detail.Run.ID)
	if err != nil {
		return detail, fmt.Errorf("reading the tasks of %q: %w", name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var task TaskSummary
		if err := rows.Scan(&task.Name, &task.DisplayName, &task.Status, &task.StartedAt,
			&task.CompletedAt, &task.PodName, &task.LogSealed, &task.HasLog); err != nil {
			return detail, fmt.Errorf("reading the tasks of %q: %w", name, err)
		}
		detail.Tasks = append(detail.Tasks, task)
	}
	return detail, rows.Err()
}
