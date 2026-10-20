package store

import (
	"context"
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"github.com/jackc/pgx/v5"
)

/*
 * Projecting the Redmine versions and requests.
 *
 * The synchronisation is a periodic incremental query, the definitive path and not a fallback: no
 * webhook plugin can be deployed on the instance.
 */

// TrackedVersion is a version as synchronisation writes it. A version's origins, as
// the schema constraint enumerates them: it comes from the tracker, was set while
// waiting for the tracker to know it, or belongs to a project that has none.
const (
	VersionOriginTracker = "tracker"
	VersionOriginPending = "pending"
	VersionOriginOwn     = "own"
)

type TrackedVersion struct {
	// Tool names the tracking tool this version comes from. Empty when it comes from
	// none.
	Tool string
	// ExternalID is its identifier in that tool, as text: one tracker numbers, another
	// writes `MSE-1234`.
	ExternalID string
	// ProjectID is the repository tracking this version. The external identifier is
	// unique only within it: two projects may track two instances, or two projects of
	// the same instance.
	ProjectID int64
	Name      string
	// Normalized is the comparable form. It is not unique: `3.1.2` and `v3.1.2` may
	// coexist.
	Normalized  string
	Description string
	Status      string
	DueDate     *time.Time
	UpdatedOn   *time.Time
}

// TrackedIssue is an issue as synchronisation writes it.
type TrackedIssue struct {
	// ProjectID bounds the attachment to a version: two tracked projects may carry the
	// same tracker identifier.
	ProjectID int64
	// Tool names the tracking tool, ExternalID the identifier it uses.
	Tool       string
	ExternalID string
	// VersionExternalID is the version's identifier in the tool, not the local key:
	// synchronisation reads the tracker and knows only its identifiers.
	VersionExternalID *string
	Tracker           string
	Subject           string
	StatusName        string
	StatusBucket      string
	Priority          string
	Assignee          string
	DoneRatio         int
	IsClosed          bool
	UpdatedOn         *time.Time
}

// UpsertRedmineVersion records a version and returns its local key.
//
// Simple, deliberately: the identity is the pair (tool, external_id). Matching with
// provisional versions is a separate operation ([DB.AdoptPendingVersions]), run
// after every version has been written.
//
// Mixing them would depend on iteration order. `3.1.2` and `v3.1.2` reduce to the
// same number; whichever came first would adopt the provisional one, and a release
// would end up attached to the closed version rather than the open one — an
// invisible arbitrary choice.
func (db *DB) UpsertRedmineVersion(ctx context.Context, version TrackedVersion) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into versions
		     (project_id, tool, external_id, name, normalized_name, description, status, due_date,
		      external_updated_on, origin, synced_at)
		 values ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, 'tracker', now())
		 -- The conflict is on the triple: the same identifier may be tracked by
		 -- two projects, or by two tools, and a wider uniqueness would make them
		 -- overwrite each other.
		 on conflict (project_id, tool, external_id) where external_id is not null do update set
		     name = excluded.name,
		     normalized_name = excluded.normalized_name,
		     description = excluded.description,
		     status = excluded.status,
		     due_date = excluded.due_date,
		     external_updated_on = excluded.external_updated_on,
		     origin = 'tracker',
		     synced_at = now(),
		     updated_at = now()
		 returning id`,
		version.ProjectID, version.Tool, version.ExternalID, version.Name, version.Normalized,
		version.Description, version.Status, version.DueDate, version.UpdatedOn).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("recording version %s/%s: %w",
			version.Tool, version.ExternalID, err)
	}
	return id, nil
}

// Adoption reports the matching of a provisional version.
type Adoption struct {
	// PendingName is the name the provisional version carries, taken from the release's
	// tag.
	PendingName string
	// AdoptedInto is the name of the tracker version that absorbed it. Empty when no
	// adoption happened.
	AdoptedInto string
	// Releases is how many releases are attached to the real version.
	Releases int64
	// Ambiguous marks a provisional version several real versions claim.
	Ambiguous bool
	// Candidates lists those versions, so the administration screen can name them.
	Candidates []string
}

// AdoptPendingVersions matches provisional versions with real ones.
//
// EnsureVersion creates, when a release is created, a row of origin `pending` when
// the version is not yet known — the release has to be attached to something.
// Without matching, the real version would display with no release and the
// provisional one with no scope.
//
// Adoption requires a single candidate. When several versions carry the same number,
// the one that is not closed is preferred: a project's closed versions may come from
// an older numbering scheme, and a release aiming at that number aims at the active
// version, never at its historical namesake.
//
// If the ambiguity remains — several active, or several closed and none active —
// nothing is adopted. Attaching to the wrong version is worse than attaching to
// none, and the case is returned to the caller for display.
//
// To be called after [DB.UpsertRedmineVersion] over every version: that is what
// makes the result independent of read order.
func (db *DB) AdoptPendingVersions(ctx context.Context) ([]Adoption, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("adoption: %w", err)
	}
	defer tx.Rollback(ctx)

	// `for update` on the provisional rows: two concurrent synchronisations must not
	// adopt the same one twice.
	rows, err := tx.Query(ctx,
		// `origin = 'pending'` and not a negative identifier: the two coincided while a
		// version without a tracker identifier could only be pending. A project with no
		// tracker now produces definitive ones, and trying to adopt them would match them
		// against a tool that does not exist.
		`select id, project_id, name, normalized_name from versions
		 where origin = 'pending' order by id for update`)
	if err != nil {
		return nil, fmt.Errorf("reading the provisional versions: %w", err)
	}
	type pending struct {
		id        int64
		projectID int64
		name      string
		// normalized is the comparable form. Matching happens within the project: two
		// applications tracked together may carry the same number, and adopting across
		// would attach a release to the other's version — silently, since both exist.
		normalized *string
	}
	var pendings []pending
	for rows.Next() {
		var entry pending
		if err := rows.Scan(&entry.id, &entry.projectID, &entry.name, &entry.normalized); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading the provisional versions: %w", err)
		}
		pendings = append(pendings, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the provisional versions: %w", err)
	}

	var report []Adoption
	for _, entry := range pendings {
		if entry.normalized == nil || *entry.normalized == "" {
			continue
		}

		candidates, err := realVersionsNamed(ctx, tx, entry.projectID, *entry.normalized)
		if err != nil {
			return nil, err
		}

		// Several versions may carry the same number, one closed and one open: a project's
		// closed versions may come from an older numbering scheme whose numbers the current
		// convention reused. A release aiming at that number aims at the version still
		// active, never at its historical namesake.
		//
		// The preference is on "not closed" and not on "open", because a version is normally
		// closed after its delivery: pinning the rule to `open` would break a matching that
		// worked, the day the version aimed at is closed.
		retained := candidates
		if len(candidates) > 1 {
			if active := activeOnly(candidates); len(active) == 1 {
				retained = active
			}
		}

		switch len(retained) {
		case 0:
			// No real version carries this number. The provisional one stays, and
			// PendingVersions will report it: that is the symptom of a typo in a tag, or of a
			// version missing from the tracker.
		case 1:
			moved, err := mergeVersion(ctx, tx, entry.id, retained[0].id)
			if err != nil {
				return nil, err
			}
			report = append(report, Adoption{
				PendingName: entry.name, AdoptedInto: retained[0].name, Releases: moved,
			})
		default:
			// Still undecidable: several active versions, or several closed and none active.
			// Choosing at random would attach releases to the wrong version.
			names := make([]string, 0, len(retained))
			for _, candidate := range retained {
				names = append(names, candidate.name)
			}
			report = append(report, Adoption{
				PendingName: entry.name, Ambiguous: true, Candidates: names,
			})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("adoption: %w", err)
	}
	return report, nil
}

type versionRef struct {
	id     int64
	name   string
	status string
}

// activeOnly keeps versions that are not closed.
//
// `locked` is among them: a locked version accepts no new issue but is not finished
// for all that.
func activeOnly(candidates []versionRef) []versionRef {
	active := make([]versionRef, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.status != "closed" {
			active = append(active, candidate)
		}
	}
	return active
}

// realVersionsNormalized is [realVersionsNamed] outside a transaction, for reads
// with no provisional row to lock — the immediate attachment of a release, notably.
func (db *DB) realVersionsNormalized(ctx context.Context, projectID int64, normalized string) ([]versionRef, error) {
	rows, err := db.pool.Query(ctx,
		`select id, name, status from versions
		 where project_id = $1 and origin = 'tracker' and normalized_name = $2
		 order by id`, projectID, normalized)
	if err != nil {
		return nil, fmt.Errorf("real versions named %q: %w", normalized, err)
	}
	defer rows.Close()
	return scanVersionRefs(rows, normalized)
}

func realVersionsNamed(
	ctx context.Context, tx pgx.Tx, projectID int64, normalized string,
) ([]versionRef, error) {
	rows, err := tx.Query(ctx,
		`select id, name, status from versions
		 where project_id = $1 and origin = 'tracker' and normalized_name = $2
		 order by id`, projectID, normalized)
	if err != nil {
		return nil, fmt.Errorf("real versions named %q: %w", normalized, err)
	}
	defer rows.Close()
	return scanVersionRefs(rows, normalized)
}

func scanVersionRefs(rows pgx.Rows, normalized string) ([]versionRef, error) {
	var refs []versionRef
	for rows.Next() {
		var ref versionRef
		if err := rows.Scan(&ref.id, &ref.name, &ref.status); err != nil {
			return nil, fmt.Errorf("real versions named %q: %w", normalized, err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// mergeVersion moves a provisional version's releases onto the real version, then
// deletes the provisional one.
//
// The order matters: deleting first would null `releases.version_id` through the
func mergeVersion(ctx context.Context, tx pgx.Tx, pendingID, realID int64) (int64, error) {
	tag, err := tx.Exec(ctx,
		`update releases set version_id = $2 where version_id = $1`, pendingID, realID)
	if err != nil {
		return 0, fmt.Errorf("moving the releases of version %d: %w", pendingID, err)
	}

	if _, err := tx.Exec(ctx, `delete from versions where id = $1`, pendingID); err != nil {
		return 0, fmt.Errorf("deleting provisional version %d: %w", pendingID, err)
	}
	return tag.RowsAffected(), nil
}

// UpsertRedmineIssue records an issue.
//
// The attachment is made by a sub-query on `external_id`: synchronisation knows only
// the tracker's identifiers, and resolving the local key in Go would cost a round
// trip per issue.
//
// An unknown version leaves `version_id` null rather than failing the issue. That
// happens normally: an issue may target another project's version, or one created
// between reading the versions and reading the issues.
//
// The attachment is bounded to the project. Two tracked projects may carry the same
// tracker identifier — two instances, or two projects of one instance — and a
// sub-query without that bound would attach the issue to the other's version.
func (db *DB) UpsertRedmineIssue(ctx context.Context, issue TrackedIssue) error {
	_, err := db.pool.Exec(ctx,
		`insert into issues
		     (project_id, tool, external_id, version_id, tracker, subject,
		      status_name, status_bucket, priority, assignee, done_ratio, is_closed,
		      external_updated_on, synced_at)
		 values ($12, $13, $1,
		         (select id from versions
		          where external_id = $2 and project_id = $12),
		         $3, $4, $5, $6, nullif($7, ''), nullif($8, ''), $9, $10, $11, now())
		 -- The conflict is on the triple: two projects, or two tools, may number
		 -- alike without naming the same issue.
		 on conflict (project_id, tool, external_id) do update set
		     version_id = excluded.version_id,
		     tracker = excluded.tracker,
		     subject = excluded.subject,
		     status_name = excluded.status_name,
		     status_bucket = excluded.status_bucket,
		     priority = excluded.priority,
		     assignee = excluded.assignee,
		     done_ratio = excluded.done_ratio,
		     is_closed = excluded.is_closed,
		     external_updated_on = excluded.external_updated_on,
		     synced_at = now(),
		     updated_at = now()`,
		issue.ExternalID, issue.VersionExternalID, issue.Tracker, issue.Subject,
		issue.StatusName, issue.StatusBucket, issue.Priority, issue.Assignee,
		issue.DoneRatio, issue.IsClosed, issue.UpdatedOn, issue.ProjectID, issue.Tool)
	if err != nil {
		return fmt.Errorf("recording issue %s/%s: %w",
			issue.Tool, issue.ExternalID, err)
	}
	return nil
}

// StatusMapping returns a project's status-to-bucket mapping.
//
// Loaded wholesale rather than queried per issue: thirty-odd statuses fit in memory,
// and one query per issue would multiply round trips by the number of issues
// synchronised.
//
// Layered: an instance default — the rows with no project, written by the bootstrap —
// and a project's departures, which win.
//
// Both, and not one or the other. A strictly per-project mapping would have every
// project born with no rule at all, and its completion rate would count every issue
// as not started. A strictly common mapping would let a status declared for one
// deployment move a figure displayed to another — a status name does not mean the
// same thing everywhere.
//
// `distinct on` keeps the project's row when it exists: the ordering puts non-null
// rows first, and PostgreSQL keeps the first of each name.
func (db *DB) StatusMapping(ctx context.Context, projectID int64) (map[string]string, error) {
	rows, err := db.pool.Query(ctx,
		`select distinct on (status_name) status_name, bucket
		 from issue_status_mapping
		 where project_id = $1 or project_id is null
		 order by status_name, project_id nulls last`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("status mapping: %w", err)
	}
	defer rows.Close()

	mapping := map[string]string{}
	for rows.Next() {
		var name, bucket string
		if err := rows.Scan(&name, &bucket); err != nil {
			return nil, fmt.Errorf("status mapping: %w", err)
		}
		mapping[name] = bucket
	}
	return mapping, rows.Err()
}

// UnmappedStatus is a status met with no declared mapping.
type UnmappedStatus struct {
	StatusName string
	Issues     int
}

// UnmappedStatuses returns the statuses that fell into the default bucket.
//
// An unknown status is not ignored silently: it lands in `todo` and appears here,
// hence in the administration screen. Tracker statuses belong to the deployment's
// workflow and change without warning; an unmapped addition would drift the
// completion rate with nothing saying so.
func (db *DB) UnmappedStatuses(ctx context.Context, projectID int64) ([]UnmappedStatus, error) {
	// A status is mapped if it is mapped for this project or by default. Looking only at
	// the project's rows would report as unmapped the statuses the bootstrap covers, and
	// the screen would cry out for no reason — an administration screen that cries out
	// for no reason stops being read.
	rows, err := db.pool.Query(ctx,
		`select i.status_name, count(*)
		 from issues i
		 left join issue_status_mapping m
		        on m.status_name = i.status_name
		       and (m.project_id = i.project_id or m.project_id is null)
		 where m.status_name is null and i.project_id = $1
		 group by i.status_name
		 order by count(*) desc, i.status_name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("unmapped statuses: %w", err)
	}
	defer rows.Close()

	var unmapped []UnmappedStatus
	for rows.Next() {
		var entry UnmappedStatus
		if err := rows.Scan(&entry.StatusName, &entry.Issues); err != nil {
			return nil, fmt.Errorf("unmapped statuses: %w", err)
		}
		unmapped = append(unmapped, entry)
	}
	return unmapped, rows.Err()
}

// SetStatusBucket declares or corrects a status mapping within a project.
//
// It always writes a project row, never the instance default: a decision taken on a
// project's screen must not move a figure displayed to another deployment.
func (db *DB) SetStatusBucket(ctx context.Context, projectID int64, statusName, bucket string) error {
	_, err := db.pool.Exec(ctx,
		// The predicate accompanies the conflict target: per-project uniqueness is a partial
		// index, and PostgreSQL only infers it on that condition. Without it the write fails
		// with "no unique or exclusion constraint", a message that does not name the cause.
		`insert into issue_status_mapping (project_id, status_name, bucket)
		 values ($1, $2, $3)
		 on conflict (project_id, status_name) where project_id is not null
		 do update set bucket = excluded.bucket, updated_at = now()`,
		projectID, statusName, bucket)
	if err != nil {
		return fmt.Errorf("mapping of status %q: %w", statusName, err)
	}
	return nil
}

// ReprojectIssues reapplies the mapping to issues already recorded.
//
// Necessary after correcting the mapping: without it the new rule would hold only
// for issues modified afterwards in the tracker, and the screen would show a mixture
// of both rules without saying so. Returns how many issues moved.
func (db *DB) ReprojectIssues(ctx context.Context, projectID int64) (int64, error) {
	// The self-join allows the outer join on the table being updated: PostgreSQL does not
	// accept a `left join` directly on an `update` target. The `coalesce` carries the
	// default bucket rule, the same as on insert — splitting it between two places would
	// let them diverge.
	//
	// Bounded to the project: reapplying one project's mapping must not touch another's
	// issues, whose vocabulary is not the same.
	//
	// The layering is [DB.StatusMapping]'s, repeated in a sub-select: the rule applied
	// here must be exactly the one the screen displays, otherwise reapplying would move
	// issues into buckets nobody declared.
	tag, err := db.pool.Exec(ctx,
		`update issues i
		 set status_bucket = coalesce(m.bucket, 'todo'), updated_at = now()
		 from issues source
		 left join (
		     select distinct on (status_name) status_name, bucket
		     from issue_status_mapping
		     where project_id = $1 or project_id is null
		     order by status_name, project_id nulls last
		 ) m on m.status_name = source.status_name
		 where source.id = i.id
		   and i.project_id = $1
		   and i.status_bucket <> coalesce(m.bucket, 'todo')`, projectID)
	if err != nil {
		return 0, fmt.Errorf("reapplying the mapping: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AmbiguousVersion reports several versions reducing to the same number.
type AmbiguousVersion struct {
	Normalized string
	Names      []string
}

// AmbiguousVersions returns the numbers carried by several active versions.
//
// The filter on active ones is what makes this screen useful. A reused number — one
// closed, one open — is settled in favour of the active version. Reporting them would
// mean two permanent warnings for a situation demanding no decision, and an
// administration screen that cries out for no reason stops being read.
//
// Only the genuinely undecidable cases remain: two active versions carrying the same
// number.
func (db *DB) AmbiguousVersions(ctx context.Context, projectID int64) ([]AmbiguousVersion, error) {
	// Bounded to the project: two projects may legitimately each carry a `3.1.0`, and
	// grouping them would invent an ambiguity that does not exist.
	rows, err := db.pool.Query(ctx,
		`select normalized_name, array_agg(name order by name)
		 from versions
		 where normalized_name is not null and origin = 'tracker' and status <> 'closed'
		   and project_id = $1
		 group by normalized_name
		 having count(*) > 1
		 order by normalized_name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("ambiguous versions: %w", err)
	}
	defer rows.Close()

	var ambiguous []AmbiguousVersion
	for rows.Next() {
		var entry AmbiguousVersion
		if err := rows.Scan(&entry.Normalized, &entry.Names); err != nil {
			return nil, fmt.Errorf("ambiguous versions: %w", err)
		}
		ambiguous = append(ambiguous, entry)
	}
	return ambiguous, rows.Err()
}

// PendingVersions returns the provisional versions never adopted.
//
// A provisional version surviving a successful synchronisation names a release
// attached to a version the tracker does not know — a typo in the tag, a deleted
// version, or a naming ambiguity. Reporting it keeps a screen from showing "awaiting
// synchronisation" indefinitely with nobody knowing why.
func (db *DB) PendingVersions(ctx context.Context, projectID int64) ([]string, error) {
	rows, err := db.pool.Query(ctx,
		`select name from versions
		 where origin = 'pending' and project_id = $1 order by name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("provisional versions: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("provisional versions: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// RenormalizeVersions realigns the normalised column with the current rule.
//
// `versions.normalized_name` is a derived column, written at synchronisation time.
// Changing the rule therefore makes every stored row obsolete, and matching would
// then compare a form computed with the new rule against one stored with the old: a
// release would stop finding its version, with no message at all.
//
// Synchronisation would eventually rewrite them, but only on its next pass, and only
// for versions it still sees. Doing it at start-up closes that window and also
// catches the provisional rows, which no synchronisation recomputes.
//
// Returns how many rows were corrected, so start-up can say it: zero is the common
// case, and a non-zero number deserves a log line because it signals a change of
// convention.
func (db *DB) RenormalizeVersions(ctx context.Context) (int, error) {
	rows, err := db.pool.Query(ctx, `select id, name, normalized_name from versions`)
	if err != nil {
		return 0, fmt.Errorf("reading the versions to renormalise: %w", err)
	}

	type correction struct {
		id         int64
		normalized string
	}
	var corrections []correction

	for rows.Next() {
		var id int64
		var name, normalized string
		if err := rows.Scan(&id, &name, &normalized); err != nil {
			rows.Close()
			return 0, fmt.Errorf("reading a version to renormalise: %w", err)
		}
		if wanted := release.NormalizeVersionName(name); wanted != normalized {
			corrections = append(corrections, correction{id: id, normalized: wanted})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("reading the versions to renormalise: %w", err)
	}

	// Corrections are applied after the read completes, not during it: writing on the
	// connection carrying another query's cursor is not allowed by pgx, and discovering
	// it at runtime would be a start-up failure.
	for _, fix := range corrections {
		if _, err := db.pool.Exec(ctx,
			`update versions set normalized_name = $2 where id = $1`,
			fix.id, fix.normalized); err != nil {
			return 0, fmt.Errorf("renormalising version %d: %w", fix.id, err)
		}
	}
	return len(corrections), nil
}
