package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

/*
 * Release note: which Redmine requests a release brings.
 *
 * The table carries only an attachment. The subject, the tracker and the status come from
 * `issues` at read time, so that a subject corrected in Redmine appears with nothing to
 * regenerate.
 */

// ErrNoteAlreadyLinked reports an issue already linked.
//
// Distinct from a write error: linking twice is a user gesture, not a fault, and the
// caller must be able to say so rather than fail.
var ErrNoteAlreadyLinked = errors.New("store: issue already linked to this release")

// NoteEntry is a line of a release note.
type NoteEntry struct {
	// ExternalID is the issue's identifier in the tracker, as text.
	ExternalID  string
	Source      string
	PullRequest string
	AddedBy     string
	// The following fields come from `issues`, absent while synchronisation has not seen the
	// issue.
	Known      bool
	Tracker    string
	Subject    string
	StatusName string
	Anomaly    bool
}

const (
	NoteSourceAuto   = "auto"
	NoteSourceManual = "manual"
)

// LinkNoteIssue links an issue to a release's note.
func (db *DB) LinkNoteIssue(
	ctx context.Context, releaseID int64, reference, source, pullRequest, actor string,
) error {
	tag, err := db.pool.Exec(ctx,
		`insert into release_notes (release_id, external_id, source, pull_request, added_by)
		 values ($1, $2, $3, $4, $5)
		 on conflict (release_id, external_id) do nothing`,
		releaseID, reference, source, pullRequest, actor)
	if err != nil {
		return fmt.Errorf("linking issue %s: %w", reference, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoteAlreadyLinked
	}
	return nil
}

// UnlinkNoteIssue removes an issue from a release's note.
func (db *DB) UnlinkNoteIssue(ctx context.Context, releaseID int64, reference string) error {
	if _, err := db.pool.Exec(ctx,
		`delete from release_notes where release_id = $1 and external_id = $2`,
		releaseID, reference); err != nil {
		return fmt.Errorf("unlinking issue %s: %w", reference, err)
	}
	return nil
}

// ListNoteEntries returns a release's note.
//
// A loose join on `issues`: an issue linked before synchronisation has seen it stays
// listed, without its subject. Hiding it would make a line someone added disappear, which
// would read as a loss.
func (db *DB) ListNoteEntries(ctx context.Context, releaseID int64) ([]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select n.external_id, n.source, n.pull_request, n.added_by,
		        i.external_id is not null,
		        coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from release_notes n
		   join releases r on r.id = n.release_id
		   -- Join bounded to the release's project. An identifier is unique only
		   -- within its project: joining on external_id alone would surface the
		   -- subject of another deployment's issue of the same number, and
		   -- duplicate the row.
		   left join issues i
		          on i.project_id = r.project_id and i.external_id = n.external_id
		  where n.release_id = $1
		  order by n.external_id`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("note of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.ExternalID, &entry.Source, &entry.PullRequest,
			&entry.AddedBy, &entry.Known, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("note of release %d: %w", releaseID, err)
		}
		entry.Anomaly = isAnomaly(entry.Tracker)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// NoteCandidates returns the issues eligible for an automatic note.
//
// The criterion is the status: an issue in review has its code written and reviewed, which
// is the condition for a pull request about it to have been merged. Matching with pull
// requests is done by the caller, which alone talks to the forge.
//
// The statuses kept are passed as a parameter rather than written here: they come from the
// deployment's tracker vocabulary, which it can rename without this code changing.
//
// The pool is bounded to the release's project, and the bound goes through the release
// rather than a project identifier parameter: the caller has the release at hand, and a
// parameter one can forget to pass would make another deployment's issue of the same
// number a candidate.
func (db *DB) NoteCandidates(
	ctx context.Context, releaseID int64, statuses []string,
) (map[string]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select i.external_id, coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from releases r
		   join issues i on i.project_id = r.project_id
		  where r.id = $1 and i.status_name = any($2)`, releaseID, statuses)
	if err != nil {
		return nil, fmt.Errorf("issues in review: %w", err)
	}
	defer rows.Close()

	candidates := map[string]NoteEntry{}
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.ExternalID, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("issues in review: %w", err)
		}
		entry.Known = true
		entry.Anomaly = isAnomaly(entry.Tracker)
		candidates[entry.ExternalID] = entry
	}
	return candidates, rows.Err()
}

// ListReleaseVersionIssues returns the issues of a release's version.
//
// It is the pool one draws from to complete a note by hand: the issue sought almost always
// belongs to the release's version, and finding it by number alone meant having it in
// front of you in the tracker.
//
// Rejected ones are set aside: they are not delivered, and offering them for linking would
// invite announcing to the client what was dropped.
func (db *DB) ListReleaseVersionIssues(ctx context.Context, releaseID int64) ([]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select i.external_id, coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from releases r
		   join issues i on i.version_id = r.version_id
		  where r.id = $1
		    and i.status_bucket <> $2
		  order by i.tracker, i.external_id`, releaseID, BucketRejected)
	if err != nil {
		return nil, fmt.Errorf("issues of the version of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.ExternalID, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("issues of the version of release %d: %w", releaseID, err)
		}
		entry.Known = true
		entry.Anomaly = isAnomaly(entry.Tracker)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// ReleaseIDByTag returns a release's key from its tag.
func (db *DB) ReleaseIDByTag(ctx context.Context, tag string) (int64, error) {
	var id int64
	if err := db.pool.QueryRow(ctx,
		`select id from releases where tag = $1`, tag).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrReleaseNotFound
		}
		return 0, fmt.Errorf("release %q: %w", tag, err)
	}
	return id, nil
}

// isAnomaly classifies a tracker.
//
// The prefix and not equality: the tracker carries several anomaly variants, all of them
// fixes. The same rule as the version mapping, hence the shared constant.
func isAnomaly(tracker string) bool {
	return len(tracker) >= len(anomalyTrackerPrefix) &&
		tracker[:len(anomalyTrackerPrefix)] == anomalyTrackerPrefix
}
