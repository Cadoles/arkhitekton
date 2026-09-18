package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrVersionNotFoundByID reports a missing version.
var ErrVersionNotFoundByID = errors.New("version introuvable")

// VersionSummary is a version and its progress.
//
// Two completion measures, because they do not say the same thing: by issue count,
// readable but a large issue counts as much as a small one; weighted by `done_ratio`,
// finer but dependent on how carefully it is filled in.
type VersionSummary struct {
	ID int64
	// ProjectID attaches the version to its repository. Needed to know which version
	// scheme and which release cycle apply.
	ProjectID int64
	// ExternalID is the version's identifier in the tracker, empty when it comes from
	// none.
	ExternalID string
	Name       string
	Status     string
	DueDate    *time.Time
	SyncedAt   time.Time

	// Pending marks a provisional version, created by a release before synchronisation
	// knew the real one.
	Pending bool

	// Counted is the denominator: issues other than `rejected`. Rejected ones weigh
	// neither in the numerator nor in the denominator — counting them below would lower a
	// version's completion because an issue was dropped.
	Counted int
	Done    int
	// WeightedDone is the sum of the counted issues' `done_ratio`, in percentage points.
	// Divided by Counted it gives the second measure.
	WeightedDone int

	// OpenAnomalies is counted separately and does not enter completion: a version may be
	// functionally complete and carry open anomalies; they are two distinct signals and
	// must stay so.
	OpenAnomalies int

	Releases int
}

// Buckets is the mapping of tracker statuses, in order of progress.
//
// One place, and ordered: the screen's columns, the administration selector's list
// and the database constraint must say the same thing. Three separate enumerations
// would diverge at the first addition.
//
// `rejected` sits outside progress: it is neither before nor after, it is out of
// scope.
var Buckets = []string{BucketTodo, BucketDoing, BucketClientTest, BucketDone}

const (
	BucketTodo  = "todo"
	BucketDoing = "doing"
	// BucketClientTest is awaiting the client's approval: development is finished,
	// acceptance is not.
	//
	// It does not count as done. It enters the completion denominator, never the
	// numerator — confusing "finished developing" with "accepted" is exactly what a
	// completion rate must avoid.
	BucketClientTest = "client-test"
	BucketDone       = "done"
	// BucketRejected leaves the computation: neither numerator nor denominator. Counting
	// it below would lower a version's completion because an issue was dropped.
	BucketRejected = "rejected"
)

// OpenBuckets are the buckets of an issue still to be handled.
//
// It is this list that decides whether an anomaly is open, and not the tracker's
// `is_closed` flag. The two diverge on the real instance: one status is declared not
// closed while it maps to `done`, and another the reverse. Counting anomalies on
// `is_closed` therefore displayed five finished anomalies as open.
//
// Using two notions of "closed" in the same screen is the underlying fault; the
// mapping is what the user sets and sees, hence the only authority.
//
// `client-test` is absent from it: an anomaly awaiting client approval is no longer
// for the team to handle.
var OpenBuckets = []string{BucketTodo, BucketDoing}

// BucketIsOpen says whether a bucket names an issue still to be handled.
func BucketIsOpen(bucket string) bool {
	for _, open := range OpenBuckets {
		if open == bucket {
			return true
		}
	}
	return false
}

// ValidBucket says whether a bucket is declared.
func ValidBucket(bucket string) bool {
	if bucket == BucketRejected {
		return true
	}
	for _, known := range Buckets {
		if known == bucket {
			return true
		}
	}
	return false
}

// anomalyTrackers bounds what counts as an anomaly.
//
// The instance declares two names where a single one was assumed. Comparing for
// equality would miss half of them, and the prefix is the only reliable common
// ground.
const anomalyTrackerPrefix = "Anomalie"

// ListVersionSummaries returns a project's versions and their progress.
//
// Closed versions with no issue and no release are not hidden: they exist in the
// tracker, and hiding them would cast doubt on the screen's completeness.
//
// Bounded to the project since the dashboard drives several. It was not: the
// `/projects/{p}/versions` route passed its project and the query ignored it — a
// project with no version displayed its neighbour's twenty, with no error saying so.
// That is the worst failure mode of multi-project work, the one where a client's
// screen shows another's data.
func (db *DB) ListVersionSummaries(ctx context.Context, projectID int64) ([]VersionSummary, error) {
	rows, err := db.pool.Query(ctx, versionSummaryQuery+`
		   and v.project_id = $3
		 order by v.due_date desc nulls last, v.name desc`,
		anomalyTrackerPrefix, OpenBuckets, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing the versions: %w", err)
	}
	defer rows.Close()

	var summaries []VersionSummary
	for rows.Next() {
		summary, err := scanVersionSummary(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// GetVersionSummary returns a version by its local key.
func (db *DB) GetVersionSummary(ctx context.Context, id int64) (VersionSummary, error) {
	rows, err := db.pool.Query(ctx, versionSummaryQuery+` and v.id = $3`,
		anomalyTrackerPrefix, OpenBuckets, id)
	if err != nil {
		return VersionSummary{}, fmt.Errorf("version %d: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return VersionSummary{}, fmt.Errorf("version %d: %w", id, err)
		}
		return VersionSummary{}, ErrVersionNotFoundByID
	}
	return scanVersionSummary(rows)
}

// versionSummaryQuery aggregates issues and releases per version.
//
// The aggregates are computed in lateral sub-queries rather than by joins: joining
// issues and releases in one query would multiply the rows of one by the other, and
// each count would be wrong by a factor equal to the other table's row count.
const versionSummaryQuery = `
	select v.id, v.project_id, coalesce(v.external_id, ''), v.origin, v.name, v.status, v.due_date, v.synced_at,
	       counts.counted, counts.done, counts.weighted, counts.anomalies,
	       releases.total
	from versions v
	left join lateral (
	    select count(*) filter (where status_bucket <> 'rejected')            as counted,
	           count(*) filter (where status_bucket = 'done')                 as done,
	           coalesce(sum(done_ratio) filter (where status_bucket <> 'rejected'), 0) as weighted,
	           -- Open in the sense of the mapped bucket, never of the tracker's
	           -- is_closed flag: the two diverge, and that is what made five
	           -- finished anomalies count as open.
	           count(*) filter (
	               where tracker like $1 || '%' and status_bucket = any($2)
	           ) as anomalies
	    from issues where version_id = v.id
	) counts on true
	left join lateral (
	    select count(*) as total from releases where version_id = v.id
	) releases on true
	where true`

func scanVersionSummary(rows pgx.Rows) (VersionSummary, error) {
	var summary VersionSummary
	var origin string
	if err := rows.Scan(&summary.ID, &summary.ProjectID, &summary.ExternalID, &origin, &summary.Name, &summary.Status,
		&summary.DueDate, &summary.SyncedAt, &summary.Counted, &summary.Done,
		&summary.WeightedDone, &summary.OpenAnomalies, &summary.Releases); err != nil {
		return VersionSummary{}, fmt.Errorf("reading a version: %w", err)
	}
	// Provisional status is read from `origin`. It used to be inferred from the sign of a
	// numeric identifier — a negative integer that existed only to satisfy a not-null
	// constraint, and that one had to know how to read.
	summary.Pending = origin == VersionOriginPending
	return summary, nil
}

// VersionIssue is an issue within a version's scope.
type VersionIssue struct {
	// ExternalID is the issue's identifier in the tracker, as text.
	ExternalID string
	Tracker    string
	Subject    string
	StatusName string
	Bucket     string
	Priority   string
	Assignee   string
	DoneRatio  int
	IsClosed   bool
	// Anomaly marks anomalies, counted apart from the completion rate.
	Anomaly bool
}

// ListVersionIssues returns a version's scope.
//
// Every issue, rejected ones included: they leave the completion computation but stay
// part of the scope under discussion, and hiding them would send someone hunting for
// an issue they know exists.
func (db *DB) ListVersionIssues(ctx context.Context, versionID int64) ([]VersionIssue, error) {
	rows, err := db.pool.Query(ctx,
		`select external_id, tracker, subject, status_name, status_bucket,
		        coalesce(priority, ''), coalesce(assignee, ''), done_ratio, is_closed
		 from issues
		 where version_id = $1
		 order by tracker, external_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("issues of version %d: %w", versionID, err)
	}
	defer rows.Close()

	var issues []VersionIssue
	for rows.Next() {
		var issue VersionIssue
		if err := rows.Scan(&issue.ExternalID, &issue.Tracker, &issue.Subject,
			&issue.StatusName, &issue.Bucket, &issue.Priority, &issue.Assignee,
			&issue.DoneRatio, &issue.IsClosed); err != nil {
			return nil, fmt.Errorf("issues of version %d: %w", versionID, err)
		}
		issue.Anomaly = len(issue.Tracker) >= len(anomalyTrackerPrefix) &&
			issue.Tracker[:len(anomalyTrackerPrefix)] == anomalyTrackerPrefix
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// SyncState is a synchronisation's state.
//
// LastOK is distinct from LastRun so the interface can say "data fourteen minutes
// old, last synchronisation failed" rather than a mute figure.
type SyncState struct {
	Source    string
	Cursor    string
	LastRun   *time.Time
	LastOK    *time.Time
	LastError string
}

// GetSyncState returns a source's state for a project, or an empty state when it has
// never run.
//
// Per project. A single cursor advanced on the first project read, and the second
// resumed from an instant at which it had read nothing: its history was skipped,
// permanently and with no message.
func (db *DB) GetSyncState(ctx context.Context, source string, projectID int64) (SyncState, error) {
	state := SyncState{Source: source}
	var cursor, lastError *string

	err := db.pool.QueryRow(ctx,
		`select cursor, last_run, last_ok, last_error from sync_state
		 where source = $1 and project_id = $2`,
		source, projectID).Scan(&cursor, &state.LastRun, &state.LastOK, &lastError)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Never synchronised is not an error: it is the state at first start-up, and it leads
			// to a full read.
			return state, nil
		}
		return SyncState{}, fmt.Errorf("synchronisation state %q: %w", source, err)
	}
	if cursor != nil {
		state.Cursor = *cursor
	}
	if lastError != nil {
		state.LastError = *lastError
	}
	return state, nil
}

// RecordSyncSuccess advances a project's cursor and clears the previous error.
//
// The `where project_id is not null` predicate is not decorative: `sync_state`
// carries two partial unique indexes — one per project, one for what targets none —
// and PostgreSQL refuses an `on conflict` that does not restate the targeted index's
// predicate. Without it the write fails with 42P10.
//
// Observed in production, in the worker's log: every probe pass failed to record its
// state, and the screen could therefore never say "last attempt failed".
func (db *DB) RecordSyncSuccess(ctx context.Context, source string, projectID int64, cursor string) error {
	_, err := db.pool.Exec(ctx,
		`insert into sync_state (source, project_id, cursor, last_run, last_ok, last_error, updated_at)
		 values ($1, $2, nullif($3, ''), now(), now(), null, now())
		 on conflict (source, project_id) where project_id is not null do update set
		     cursor = excluded.cursor, last_run = excluded.last_run,
		     last_ok = excluded.last_ok, last_error = null, updated_at = now()`,
		source, projectID, cursor)
	if err != nil {
		return fmt.Errorf("recording synchronisation %q: %w", source, err)
	}
	return nil
}

// RecordSyncFailure records the failure without touching the cursor.
//
// The cursor is left in place deliberately: advancing it after a failure would skip
// the unread window, and issues modified during it would never be picked up. Better
// to read twice than to lose.
func (db *DB) RecordSyncFailure(ctx context.Context, source string, projectID int64, message string) error {
	_, err := db.pool.Exec(ctx,
		`insert into sync_state (source, project_id, last_run, last_error, updated_at)
		 values ($1, $2, now(), $3, now())
		 on conflict (source, project_id) where project_id is not null do update set
		     last_run = excluded.last_run, last_error = excluded.last_error,
		     updated_at = now()`,
		source, projectID, message)
	if err != nil {
		return fmt.Errorf("recording the synchronisation failure %q: %w", source, err)
	}
	return nil
}

// ResetSyncCursor clears a project's cursor to force a full read.
func (db *DB) ResetSyncCursor(ctx context.Context, source string, projectID int64) error {
	_, err := db.pool.Exec(ctx,
		`insert into sync_state (source, project_id, cursor, updated_at)
		 values ($1, $2, null, now())
		 on conflict (source, project_id) where project_id is not null do update set cursor = null, updated_at = now()`,
		source, projectID)
	if err != nil {
		return fmt.Errorf("resetting cursor %q: %w", source, err)
	}
	return nil
}

// VersionRelease is a release produced for a version.
type VersionRelease struct {
	Tag          string
	Kind         string
	State        string
	ImageVersion string
	CreatedAt    time.Time
}

// ListVersionReleases returns a version's releases, newest first.
func (db *DB) ListVersionReleases(ctx context.Context, versionID int64) ([]VersionRelease, error) {
	rows, err := db.pool.Query(ctx,
		`select tag, kind, state, image_version, created_at
		 from releases where version_id = $1
		 order by created_at desc`, versionID)
	if err != nil {
		return nil, fmt.Errorf("releases of version %d: %w", versionID, err)
	}
	defer rows.Close()

	var releases []VersionRelease
	for rows.Next() {
		var item VersionRelease
		if err := rows.Scan(&item.Tag, &item.Kind, &item.State,
			&item.ImageVersion, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("releases of version %d: %w", versionID, err)
		}
		releases = append(releases, item)
	}
	return releases, rows.Err()
}
