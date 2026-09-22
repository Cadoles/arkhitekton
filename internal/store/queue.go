package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// A job's states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Waiting reasons, constrained by the schema.
//
// A queue that does not explain its waiting systematically produces needless retries:
// the user sees a job standing still and launches a second.
const (
	// ReasonKindBusy: the kind's queue is full. The most common reason, and the only one
	// a setting can lift.
	ReasonKindBusy = "kind-busy"
	// ReasonCapacity: the global ceiling is reached, across every queue. It protects the
	// cluster and prevails over the per-kind ceilings.
	ReasonCapacity = "capacity"
)

// Limits carries the effective ceilings, per kind.
//
// Composed by the caller from the catalogue and the settings: `store` knows neither,
// and recomposing them here would tie it to the catalogue for the sake of an
// addition.
type Limits struct {
	// PerKind gives a kind's ceiling. A kind absent from it takes Default.
	PerKind map[string]int
	// Default applies to kinds the catalogue does not describe — local tasks, or a kind
	// retired since the job was queued.
	Default int
	// Global bounds the total across every queue. Without it, seven queues of five places
	// would allow thirty-five simultaneous runs, which no cluster of this project holds.
	Global int
}

// Of returns a kind's effective ceiling.
func (l Limits) Of(kind string) int {
	if limit, declared := l.PerKind[kind]; declared && limit > 0 {
		return limit
	}
	return l.Default
}

// arrays returns the ceilings in the shape `unnest` expects.
//
// Two parallel arrays rather than JSON: the join reads in SQL without extraction, and
// Postgres can index them.
func (l Limits) arrays() ([]string, []int32) {
	kinds := make([]string, 0, len(l.PerKind))
	limits := make([]int32, 0, len(l.PerKind))
	for kind, limit := range l.PerKind {
		kinds = append(kinds, kind)
		limits = append(limits, int32(limit))
	}
	return kinds, limits
}

// JobRequest describes a launch request.
type JobRequest struct {
	Kind        string
	Priority    int
	Params      map[string]string
	RequestedBy string
	ReleaseID   *int64
	Environment *string
	// ProjectID attaches the job to its repository. Nil for what targets none.
	//
	// The scheduler uses it to mount the project's deploy key rather than a shared one:
	// without it, it can only fall back to the shared key and undo the benefit of
	// separating them.
	ProjectID *int64
}

// Job is a job in the queue.
type Job struct {
	ID          int64
	Kind        string
	Priority    int
	State       string
	Params      map[string]string
	RequestedBy string
	RequestedAt time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Attempt     int
	QueueReason string
	Error       string
	RunName     string
	// ReleaseID attaches the job to a release. Set for promotions and exports, whose run
	// starts from the release and not from parameters.
	ReleaseID *int64
	// ProjectID attaches the job to its repository. Nil for jobs predating the migration
	// that targeted no release: inventing one for them would return a false history
	// rather than an incomplete one.
	ProjectID *int64
	// ConcurrentAtStart is how many runs were observed at the moment of the claim. Set by
	// ClaimNext alone: that is the only instant the information exists, and it is what
	// decides whether a load measurement is comparable with its baseline.
	ConcurrentAtStart int
}

// ErrJobNotFound reports a missing job.
var ErrJobNotFound = errors.New("job introuvable")

// Enqueue puts a job in the queue.
func (db *DB) Enqueue(ctx context.Context, request JobRequest) (int64, error) {
	params, err := json.Marshal(request.Params)
	if err != nil {
		return 0, fmt.Errorf("serialising the parameters: %w", err)
	}
	if request.Priority == 0 {
		request.Priority = 100
	}

	var id int64
	err = db.pool.QueryRow(ctx,
		// The project is derived from the release when the caller did not give one. Eight
		// sites enqueue jobs, and requiring each to remember the project guaranteed one would
		// forget: the run would then have left with the shared key, with no error. Here the
		// rule is written once.
		`insert into jobs (kind, priority, state, params, requested_by, release_id,
		                   environment_id, project_id)
		 values ($1, $2, 'queued', $3, $4, $5, $6,
		         coalesce($7, (select project_id from releases where id = $5)))
		 returning id`,
		request.Kind, request.Priority, params,
		request.RequestedBy, request.ReleaseID, request.Environment,
		request.ProjectID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("queueing %q: %w", request.Kind, err)
	}
	return id, nil
}

// ClaimNext claims the next eligible job and moves it to `running`.
//
// All the queue semantics are in this query, deliberately: splitting it between SQL
// and Go would open a window between the read and the write during which a second
// scheduler could pick the same job. `for update ... skip locked` closes it — belt and
// braces with leader election, because launching a production deployment twice is not
// recoverable.
//
// Two conditions:
//
//  1. the global ceiling protects the cluster, across every queue;
//  2. the kind's queue must have a free place.
//
// Each kind has its queue. A kind with no declared ceiling takes the default, which
// holds for local tasks — absent from the catalogue — as for a kind retired since the
// job was queued: the job advances instead of waiting for a setting that will never
// exist.
//
// The order stays the same for all, priority then age: it is the single queue that
// decides who is examined first, the ceilings only set aside. A job whose queue is
// full is skipped, not blocking — otherwise an integration run would hold every unit
// test behind it.
//
// Returns ErrJobNotFound when nothing is eligible — a common case, not an error.
func (db *DB) ClaimNext(
	ctx context.Context, limits Limits, runName func(kind string, id int64) string,
) (Job, error) {
	var job Job

	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return job, fmt.Errorf("opening a transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	const query = `
		with limits as (
		    select * from unnest($2::text[], $3::int[]) as l(kind, max_concurrent)
		),
		running as (
		    select kind from jobs where state = 'running'
		)
		select j.id, j.kind, j.priority, j.params, j.requested_by,
		       j.requested_at, j.attempt, j.release_id, j.project_id,
		       -- The start conditions are recorded here and not later: minutes
		       -- afterwards, nothing says what was running at the moment of the
		       -- measurement. The running CTE is computed before the claim, so
		       -- this count excludes the claimed job.
		       (select count(*) from running)
		from jobs j
		where j.state = 'queued'
		  and (select count(*) from running) < $1
		  and (select count(*) from running r where r.kind = j.kind)
		      < coalesce((select l.max_concurrent from limits l where l.kind = j.kind), $4)
		order by j.priority, j.requested_at
		for update of j skip locked
		limit 1`

	kinds, perKind := limits.arrays()

	var params []byte
	err = transaction.QueryRow(ctx, query, limits.Global, kinds, perKind, limits.Default).Scan(
		&job.ID, &job.Kind, &job.Priority, &params,
		&job.RequestedBy, &job.RequestedAt, &job.Attempt, &job.ReleaseID,
		&job.ProjectID, &job.ConcurrentAtStart)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return job, ErrJobNotFound
		}
		return job, fmt.Errorf("selecting the next job: %w", err)
	}

	if len(params) > 0 {
		if err := json.Unmarshal(params, &job.Params); err != nil {
			return job, fmt.Errorf("parameters of job %d: %w", job.ID, err)
		}
	}

	// The PipelineRun's name is decided before creation in the cluster, and written in
	// the same transaction as the transition. That is what makes an orphaned run
	// impossible: if the creation then fails, the job returns to `queued` and the name is
	// reassigned on the next attempt.
	job.RunName = runName(job.Kind, job.ID)

	// Observed exclusivity is no longer a held barrier but a queue of one place: the kind
	// tolerated no twin run. It is not enough to make a measurement comparable —
	// `concurrent_at_start` must be zero, so nothing else was running either — and it is
	// that second condition that now carries the weight.
	exclusive := limits.Of(job.Kind) == 1

	_, err = transaction.Exec(ctx,
		`update jobs
		 set state = 'running', started_at = now(), queue_reason = null,
		     -- Observed conditions, frozen in the same transaction as the claim:
		     -- that is the only instant they exist.
		     exclusive_at_start = $3,
		     concurrent_at_start = $4,
		     run_name = $2, updated_at = now()
		 where id = $1`, job.ID, job.RunName, exclusive, job.ConcurrentAtStart)
	if err != nil {
		return job, fmt.Errorf("claiming job %d: %w", job.ID, err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return job, fmt.Errorf("committing the claim of job %d: %w", job.ID, err)
	}

	job.State = JobRunning
	return job, nil
}

// MaxJobAttempts bounds a job's retries.
//
// A retry serves transient failures — a momentarily unavailable cluster, a saturated
// node. It does not repair a job that has become unsatisfiable: a parameter the
// catalogue requires and the job does not carry will never materialise, because
// parameters are frozen at enqueue time and the catalogue evolves.
//
// Without a ceiling, such a job is retried indefinitely. Observed: two hundred and
// thirty attempts in a few minutes, and the whole queue held behind it — the scheduler
// always picked the same, oldest job before considering the others.
const MaxJobAttempts = 5

// Requeue puts a job back in the queue after a creation failure in the cluster.
//
// Returns false when the ceiling is reached: the caller must then conclude the job
// failed rather than insist. Returning the information rather than concluding here
// keeps the decision where the reason is known — the scheduler knows why the retry
// failed, and it is what records it.
func (db *DB) Requeue(ctx context.Context, id int64, reason string) (bool, error) {
	var attempt int
	err := db.pool.QueryRow(ctx,
		`update jobs
		 set state = 'queued', started_at = null, run_name = null,
		     attempt = attempt + 1, error = $2, updated_at = now()
		 where id = $1 and state = 'running'
		 returning attempt`, id, reason).Scan(&attempt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The job was no longer `running`: cancelled meanwhile, or already retried by another
			// pass. Nothing to do, and not an error.
			return false, nil
		}
		return false, fmt.Errorf("requeueing job %d: %w", id, err)
	}
	return attempt < MaxJobAttempts, nil
}

// FinishJob concludes a job.
func (db *DB) FinishJob(ctx context.Context, id int64, state, reason string) error {
	_, err := db.pool.Exec(ctx,
		`update jobs
		 set state = $2, finished_at = now(), error = nullif($3, ''), updated_at = now()
		 where id = $1 and state not in ('succeeded', 'failed', 'cancelled')`,
		id, state, reason)
	if err != nil {
		return fmt.Errorf("concluding job %d: %w", id, err)
	}
	return nil
}

// CancelQueuedJob cancels a job still in the queue.
//
// Returns false if the job was no longer queued: the caller must then go through the
// cluster, which is a different operation and cannot be decided here.
func (db *DB) CancelQueuedJob(ctx context.Context, id int64, reason string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update jobs
		 set state = 'cancelled', finished_at = now(), cancel_reason = $2, updated_at = now()
		 where id = $1 and state = 'queued'`, id, reason)
	if err != nil {
		return false, fmt.Errorf("cancelling job %d: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// RunningJob describes a running job, for cancellation and reconciliation.
type RunningJob struct {
	ID      int64
	Kind    string
	RunName string
}

// GetJob returns a job.
func (db *DB) GetJob(ctx context.Context, id int64) (Job, error) {
	var job Job
	var params []byte
	var reason, failure, runName *string

	err := db.pool.QueryRow(ctx,
		`select id, kind, priority, state, params, requested_by,
		        requested_at, started_at, finished_at, attempt, queue_reason, error, run_name
		 from jobs where id = $1`, id).
		Scan(&job.ID, &job.Kind, &job.Priority, &job.State, &params,
			&job.RequestedBy, &job.RequestedAt, &job.StartedAt, &job.FinishedAt,
			&job.Attempt, &reason, &failure, &runName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return job, ErrJobNotFound
		}
		return job, fmt.Errorf("reading job %d: %w", id, err)
	}

	if len(params) > 0 {
		_ = json.Unmarshal(params, &job.Params)
	}
	job.QueueReason = value(reason)
	job.Error = value(failure)
	job.RunName = value(runName)
	return job, nil
}

// ListQueue returns the active jobs: running first, then waiting in the real order of
// service.
//
// The order displayed is the scheduler's, and no other. A queue displayed in an order
// different from the one it will be served in is worse than no queue: it makes people
// wait for the wrong job.
func (db *DB) ListQueue(ctx context.Context) ([]Job, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, priority, state, params, requested_by,
		        requested_at, started_at, attempt, queue_reason, run_name
		 from jobs
		 where state in ('queued', 'running')
		 order by case state when 'running' then 0 else 1 end,
		          priority, requested_at`)
	if err != nil {
		return nil, fmt.Errorf("reading the queue: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		var params []byte
		var reason, runName *string

		if err := rows.Scan(&job.ID, &job.Kind, &job.Priority, &job.State,
			&params, &job.RequestedBy, &job.RequestedAt, &job.StartedAt, &job.Attempt,
			&reason, &runName); err != nil {
			return nil, fmt.Errorf("reading the queue: %w", err)
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params, &job.Params)
		}
		job.QueueReason = value(reason)
		job.RunName = value(runName)
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ExplainQueue records on each waiting job the reason it is not advancing.
//
// Computed in the database and not on display: the reason depends on the queue's
// global state at a given instant, and two clients recomputing it each on their side
// would show different explanations of the same block.
func (db *DB) ExplainQueue(ctx context.Context, limits Limits) error {
	const query = `
		with limits as (
		    select * from unnest($2::text[], $3::int[]) as l(kind, max_concurrent)
		),
		running as (
		    select kind from jobs where state = 'running'
		),
		capacity as (
		    select count(*) as total from running
		)
		update jobs j
		set queue_reason = case
		        -- The branch order is that of the dominant cause: the global
		        -- ceiling prevails, because no queue setting will lift it.
		        -- Saying "queue full" to a job held by the ceiling would send
		        -- someone looking for room in the wrong place.
		        when capacity.total >= $1 then 'capacity'
		        when (select count(*) from running r where r.kind = j.kind)
		             >= coalesce((select l.max_concurrent from limits l where l.kind = j.kind), $4)
		             then 'kind-busy'
		        else null
		    end,
		    updated_at = now()
		from capacity
		where j.state = 'queued'`

	kinds, perKind := limits.arrays()
	if _, err := db.pool.Exec(ctx, query, limits.Global, kinds, perKind, limits.Default); err != nil {
		return fmt.Errorf("explaining the queue: %w", err)
	}
	return nil
}

// QueueLimits returns the ceilings set by hand, per kind.
//
// Only the modified settings appear: copying the catalogue into the database would
// freeze its value, and changing a default in the ConfigMap would have no effect with
// nothing saying so.
func (db *DB) QueueLimits(ctx context.Context) (map[string]int, error) {
	rows, err := db.pool.Query(ctx, `select kind, max_concurrent from queue_limits`)
	if err != nil {
		return nil, fmt.Errorf("reading the queue ceilings: %w", err)
	}
	defer rows.Close()

	limits := map[string]int{}
	for rows.Next() {
		var kind string
		var limit int
		if err := rows.Scan(&kind, &limit); err != nil {
			return nil, fmt.Errorf("reading the queue ceilings: %w", err)
		}
		limits[kind] = limit
	}
	return limits, rows.Err()
}

// SetQueueLimit sets a queue's ceiling.
func (db *DB) SetQueueLimit(ctx context.Context, kind string, max int, by string) error {
	if max < 1 {
		return fmt.Errorf("ceiling of %q: %d, expected >= 1", kind, max)
	}
	_, err := db.pool.Exec(ctx,
		`insert into queue_limits (kind, max_concurrent, updated_by)
		 values ($1, $2, $3)
		 on conflict (kind) do update
		     set max_concurrent = excluded.max_concurrent,
		         updated_by = excluded.updated_by,
		         updated_at = now()`, kind, max, by)
	if err != nil {
		return fmt.Errorf("setting the ceiling of %q: %w", kind, err)
	}
	return nil
}

// ClearQueueLimit returns a queue to its catalogue ceiling.
func (db *DB) ClearQueueLimit(ctx context.Context, kind string) error {
	if _, err := db.pool.Exec(ctx, `delete from queue_limits where kind = $1`, kind); err != nil {
		return fmt.Errorf("clearing the ceiling of %q: %w", kind, err)
	}
	return nil
}

// QueueOccupancy counts the active jobs per kind and per state.
//
// Counted in the database and not on display: the queues screen must say how many
// places are taken now, and a count made over the paginated job list would be wrong as
// soon as it is truncated.
func (db *DB) QueueOccupancy(ctx context.Context) (map[string]QueueCount, error) {
	rows, err := db.pool.Query(ctx,
		`select kind,
		        count(*) filter (where state = 'running') as running,
		        count(*) filter (where state = 'queued')  as queued
		 from jobs
		 where state in ('queued', 'running')
		 group by kind`)
	if err != nil {
		return nil, fmt.Errorf("queue occupancy: %w", err)
	}
	defer rows.Close()

	counts := map[string]QueueCount{}
	for rows.Next() {
		var kind string
		var count QueueCount
		if err := rows.Scan(&kind, &count.Running, &count.Queued); err != nil {
			return nil, fmt.Errorf("queue occupancy: %w", err)
		}
		counts[kind] = count
	}
	return counts, rows.Err()
}

// QueueCount is a queue's occupancy.
type QueueCount struct {
	Running int
	Queued  int
}

// RunningJobs lists the running jobs, for reconciliation at start-up.
func (db *DB) RunningJobs(ctx context.Context) ([]RunningJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, coalesce(run_name, '') from jobs where state = 'running' order by id`)
	if err != nil {
		return nil, fmt.Errorf("reading the running jobs: %w", err)
	}
	defer rows.Close()

	var jobs []RunningJob
	for rows.Next() {
		var job RunningJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.RunName); err != nil {
			return nil, fmt.Errorf("reading the running jobs: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

// RequestCancel records a cancellation request on a running job.
//
// The api does not talk to Kubernetes — its service account does not even mount a
// token. Cancelling a running run is therefore recorded as an intent, which the
// scheduler carries out on its next turn. The detour costs two seconds and avoids
// giving the api cluster access for an operation it rarely performs.
func (db *DB) RequestCancel(ctx context.Context, id int64, reason string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update jobs set cancel_reason = $2, updated_at = now()
		 where id = $1 and state = 'running' and cancel_reason is null`, id, reason)
	if err != nil {
		return false, fmt.Errorf("cancellation request for job %d: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// PendingCancellations lists the runs whose stop was requested.
func (db *DB) PendingCancellations(ctx context.Context) ([]RunningJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, run_name from jobs
		 where state = 'running' and cancel_reason is not null and run_name is not null
		 order by id`)
	if err != nil {
		return nil, fmt.Errorf("reading the requested cancellations: %w", err)
	}
	defer rows.Close()

	var jobs []RunningJob
	for rows.Next() {
		var job RunningJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.RunName); err != nil {
			return nil, fmt.Errorf("reading the requested cancellations: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// InstanceID returns this database's identity.
//
// It goes into PipelineRun names. Without it, job identifiers — which restart at 1 on
// a fresh database — produced names already taken by runs left in the cluster: creation
// hit a conflict, read as "already done", and no run left at all. The interface then
// showed the failure of a run hours old.
func (db *DB) InstanceID(ctx context.Context) (string, error) {
	var id string
	if err := db.pool.QueryRow(ctx, `select id from instance`).Scan(&id); err != nil {
		return "", fmt.Errorf("database identity: %w", err)
	}
	return id, nil
}
