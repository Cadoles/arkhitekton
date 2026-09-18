package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
)

// Release is a recorded release.
type Release struct {
	ID int64
	// ProjectID is the repository carrying this release. Two projects tracked together
	// each publish their own `1.0.0`, and this is what tells them apart: a tag's
	// uniqueness is relative to its project.
	ProjectID int64
	VersionID int64
	// Tag is the release's identity: `1.8.0-rc2`, `1.8.0`.
	Tag   string
	Kind  string
	State string
	// ImageVersion is the version the image carries, distinct from the tag for every
	// final release produced by re-tag: what goes to production is bit for bit what was
	// approved, and the version baked into it stays the candidate's. Comparing the
	// running version with the tag would fail systematically.
	ImageVersion    string
	SourceReleaseID *int64
	// GitRef is the reference named at creation — `main`, a tag. It says
	GitRef string
	// GitCommit is the resolution of GitRef at that instant. It is what makes the release
	// reproducible: a branch moves.
	GitCommit  string
	CreatedBy  string
	CreatedAt  time.Time
	ReleasedAt *time.Time
	// ManualActions is the free-text account of the delivery's manual steps, reproduced
	// as is in the delivery note. The author and the date accompany it because that
	// document is contractual.
	ManualActions          string
	ManualActionsUpdatedBy string
	ManualActionsUpdatedAt *time.Time
}

// ErrReleaseNotFound reports a missing release.
var ErrReleaseNotFound = errors.New("release introuvable")

// ErrVersionNotFound reports a missing tracker version.
//
// Distinct from a write error: the versions table may be empty, and the caller must
// be able to say why the creation is refused rather than display a constraint
// violation.
var ErrVersionNotFound = errors.New("version introuvable")

// FindVersionByName finds a tracker version by its name.
//
// This is the join point of the invariant: a release's tag is the version's name,
// possibly followed by a pre-release suffix.
func (db *DB) FindVersionByName(ctx context.Context, projectID int64, name string) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`select id from versions where project_id = $1 and name = $2`, projectID, name).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrVersionNotFound
		}
		return 0, fmt.Errorf("looking up version %q: %w", name, err)
	}
	return id, nil
}

// CreateRelease records a release.
func (db *DB) CreateRelease(ctx context.Context, release Release) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into releases (
		     project_id, version_id, tag, kind, state, git_ref, git_commit, image_version,
		     source_release_id, created_by
		 ) values ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8, $9, $10)
		 returning id`,
		release.ProjectID, release.VersionID, release.Tag, release.Kind, release.State,
		release.GitRef, release.GitCommit, release.ImageVersion,
		release.SourceReleaseID, release.CreatedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("creating release %q: %w", release.Tag, err)
	}
	return id, nil
}

// GetRelease returns a release by its tag, within a project.
//
// The project is not decorative: the tag is unique only within it, and reading by tag
// alone would return another project's release as soon as a second one exists.
func (db *DB) GetRelease(ctx context.Context, projectID int64, tag string) (Release, error) {
	var release Release
	err := db.pool.QueryRow(ctx,
		`select id, project_id, version_id, tag, kind, state, coalesce(git_ref, ''),
		        coalesce(git_commit, ''), image_version,
		        source_release_id, created_by, created_at, released_at,
		        coalesce(manual_actions_md, ''), coalesce(manual_actions_updated_by, ''),
		        manual_actions_updated_at
		 from releases where project_id = $1 and tag = $2`, projectID, tag).
		Scan(&release.ID, &release.ProjectID, &release.VersionID, &release.Tag, &release.Kind, &release.State,
			&release.GitRef, &release.GitCommit, &release.ImageVersion, &release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt,
			&release.ManualActions, &release.ManualActionsUpdatedBy, &release.ManualActionsUpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return release, ErrReleaseNotFound
		}
		return release, fmt.Errorf("reading release %q: %w", tag, err)
	}
	return release, nil
}

// ListReleases returns a project's releases, newest first.
func (db *DB) ListReleases(ctx context.Context, projectID int64, limit int) ([]Release, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := db.pool.Query(ctx,
		`select id, project_id, version_id, tag, kind, state, coalesce(git_ref, ''),
		        coalesce(git_commit, ''), image_version,
		        source_release_id, created_by, created_at, released_at
		 from releases where project_id = $1 order by created_at desc limit $2`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing the releases: %w", err)
	}
	defer rows.Close()

	var releases []Release
	for rows.Next() {
		var release Release
		if err := rows.Scan(&release.ID, &release.ProjectID, &release.VersionID, &release.Tag, &release.Kind,
			&release.State, &release.GitRef, &release.GitCommit, &release.ImageVersion,
			&release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt); err != nil {
			return nil, fmt.Errorf("listing the releases: %w", err)
		}
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

// SetReleaseState advances a release.
//
// Which transition is allowed is decided by internal/release, outside the database: a
// transition table duplicated in SQL and in Go would eventually diverge, and it is
// the Go version that carries the error messages.
func (db *DB) SetReleaseState(ctx context.Context, id int64, state string) error {
	_, err := db.pool.Exec(ctx,
		`update releases set state = $2, updated_at = now() where id = $1`, id, state)
	if err != nil {
		return fmt.Errorf("state of release %d: %w", id, err)
	}
	return nil
}

// EnsureVersion attaches a release to its version, creating one if needed.
//
// The search happens in three steps, and the order carries the meaning:
//
//  1. the exact name — the case of a provisional version already created for this
//     same number;
//  2. the normalised name, preferring the version that is not closed — the nominal
//     case since synchronisation: a release tagged `3.2.0-rc1` has base `3.2.0`,
//     while the tracker version is called `v3.2.0`. Searching the exact name alone
//     created a phantom provisional version, adopted five minutes later by
//     synchronisation; meanwhile the screen showed the release attached to a version
//     "unknown to the tracker" that existed perfectly well. The preference rule is
//     the one adoption uses, so that immediate and deferred attachment name the same
//     version;
//  3. creating a provisional version of origin `pending` — the fallback when the
//     tracker does not know the number yet, recognisable and adopted later.
func (db *DB) EnsureVersion(ctx context.Context, projectID int64, name string) (int64, error) {
	id, err := db.FindVersionByName(ctx, projectID, name)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrVersionNotFound) {
		return 0, err
	}

	// Among the real versions carrying this number, the one that is not closed wins; on
	// a tie the attachment is undecidable and it falls back to the provisional one —
	// choosing at random would attach the release to the wrong version, whereas the
	// provisional one stays visible and flagged.
	candidates, err := db.realVersionsNormalized(ctx, projectID, release.NormalizeVersionName(name))
	if err != nil {
		return 0, err
	}
	if active := activeOnly(candidates); len(active) == 1 {
		return active[0].id, nil
	}
	if len(candidates) == 1 {
		return candidates[0].id, nil
	}

	// `normalized_name` is filled in from creation, and that is not cosmetic: it is the
	// only column on which synchronisation can recognise this provisional version in
	// order to adopt it. Null, it would stay orphaned and the real version would display
	// with no release.
	err = db.pool.QueryRow(ctx,
		// The origin depends on the project, and that is the whole point. A tracked project
		// receives a provisional version, awaiting adoption by synchronisation. A project
		// with no tracker receives a definitive one: nothing will come to confirm it, and
		// marking it "pending" would make the anomaly its permanent state.
		//
		// The status follows: `open` for a version that stands on its own,
		// `en-attente-de-synchronisation` for one that still hopes — a stored value,
		// left as it is so existing rows keep one vocabulary. Neither tool nor external
		// identifier: this version exists only in the dashboard.
		`insert into versions (project_id, name, normalized_name, origin, status, synced_at)
		 select $1, $2, $3,
		        case when tracked then 'pending' else 'own' end,
		        case when tracked then 'en-attente-de-synchronisation' else 'open' end,
		        now()
		 from (select coalesce(trim(tracker_project), '') <> '' as tracked
		       from projects where id = $1) as p
		 returning id`, projectID, name, release.NormalizeVersionName(name)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("creating provisional version %q: %w", name, err)
	}
	return id, nil
}

// SetManualActions records the account of a delivery's manual steps.
//
// The author and the timestamp are set here and not supplied by the caller: this text
// ends up in a contractual document, and its provenance must not depend on what a
// client chooses to declare.
func (db *DB) SetManualActions(ctx context.Context, tag, markdown, author string) error {
	tally, err := db.pool.Exec(ctx,
		`update releases
		 set manual_actions_md = nullif($2, ''),
		     manual_actions_updated_by = $3,
		     manual_actions_updated_at = now(),
		     updated_at = now()
		 where tag = $1`, tag, markdown, author)
	if err != nil {
		return fmt.Errorf("manual actions of %q: %w", tag, err)
	}
	if tally.RowsAffected() == 0 {
		return ErrReleaseNotFound
	}
	return nil
}

// ReleaseJob is a job attached to a release.
type ReleaseJob struct {
	ID            int64
	Kind          string
	State         string
	EnvironmentID string
	RunName       string
	RequestedBy   string
	RequestedAt   time.Time
	FinishedAt    *time.Time
}

// ListReleaseJobs returns what was triggered for a release.
//
// It is the path between a promotion and its evidence: the promotion says what was
// asked, the job says what happened, and its run carries the logs.
func (db *DB) ListReleaseJobs(ctx context.Context, releaseID int64) ([]ReleaseJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, state, coalesce(environment_id, ''), coalesce(run_name, ''),
		        requested_by, requested_at, finished_at
		 from jobs where release_id = $1 order by requested_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("jobs of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var jobs []ReleaseJob
	for rows.Next() {
		var job ReleaseJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.State, &job.EnvironmentID,
			&job.RunName, &job.RequestedBy, &job.RequestedAt, &job.FinishedAt); err != nil {
			return nil, fmt.Errorf("jobs of release %d: %w", releaseID, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// MarkReleased moves a release to `released` and dates its delivery.
//
// The write is conditioned on the starting state, like [DB.AdvanceReleaseState]: two
// simultaneous approvals, or an approval arriving after an abandonment, must not
// tread on each other. Returns false when the release moved between the read and the
// write.
//
// `released_at` is set here and nowhere else: it is the date of the human decision,
// not of a deployment. A release approved but never promoted still carries its
// approval date, and that is what separates "approved, not yet delivered" from
// "never approved" in the delivery note.
func (db *DB) MarkReleased(ctx context.Context, id int64, from string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update releases set state = $3, released_at = now(), updated_at = now()
		 where id = $1 and state = $2`, id, from, string(release.StateReleased))
	if err != nil {
		return false, fmt.Errorf("approving release %d: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// JobReleaseContext describes the release a job advances.
type JobReleaseContext struct {
	ReleaseID int64
	// JobID is the job itself.
	//
	// Returned because facts need it to deduplicate: a release is rebuilt, and "this
	// release's build" is not enough to identify which. The job is.
	JobID int64
	// Kind is the job's kind — `build`, `promote` — and not the release's.
	Kind  string
	Tag   string
	State string
	// Environment names the environment a promotion targets. Empty for the other kinds,
	// which target none.
	Environment string
	// GitRef and GitCommit serve to chain verifications onto what was built. The commit
	// prevails: the branch may have moved since the build.
	GitRef    string
	GitCommit string
	// ProjectID is the release's repository.
	//
	// Returned because chained verifications clone too: without it they left with no
	// project and mounted the deployment's secret, which undid the separation of keys for
	// half of a release's runs.
	ProjectID int64
}

// ErrJobWithoutRelease reports a job carrying no release.
//
// A common case and not an exceptional one: launching tests by hand from the queue
// names none.
var ErrJobWithoutRelease = errors.New("job with no release attached")

// JobRelease returns the release a job advances, and its current state.
//
// Read as the job ends: the state may have changed meanwhile — a manual retry, a
// cancellation — and deciding a transition on the state known at enqueue time would
// produce a silent overwrite.
func (db *DB) JobRelease(ctx context.Context, jobID int64) (JobReleaseContext, error) {
	var entry JobReleaseContext
	err := db.pool.QueryRow(ctx,
		`select j.id, j.kind, coalesce(j.environment_id, ''), r.id, r.tag, r.state,
		        coalesce(r.git_ref, ''), coalesce(r.git_commit, ''), r.project_id
		 from jobs j join releases r on r.id = j.release_id
		 where j.id = $1`, jobID).
		Scan(&entry.JobID, &entry.Kind, &entry.Environment,
			&entry.ReleaseID, &entry.Tag, &entry.State,
			&entry.GitRef, &entry.GitCommit, &entry.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return JobReleaseContext{}, ErrJobWithoutRelease
		}
		return JobReleaseContext{}, fmt.Errorf("release of job %d: %w", jobID, err)
	}
	return entry, nil
}

// AdvanceReleaseState writes a state if the release is still in the one that was
// read, and returns false otherwise.
//
// The condition on the starting state is what distinguishes this write from
// [DB.SetReleaseState]: between the read and the write another gesture may have moved
// the release, and overwriting its state would erase a human decision with the
// conclusion of a pipeline that was no longer the last word.
func (db *DB) AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`update releases set state = $3, updated_at = now()
		 where id = $1 and state = $2`, id, from, to)
	if err != nil {
		return false, fmt.Errorf("advancing release %d: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// VerificationTally counts a release's verifications by outcome.
type VerificationTally struct {
	Pending   int
	Failed    int
	Succeeded int
}

// Done says every verification in the series has returned its verdict.
//
// An empty series is not concluded. The count covering only the last build, there is
// an instant — between writing `verifying` and queueing the tests — when the series is
// empty. A late verdict from a previous series, redelivered by the informer at that
// moment, would see "everything concluded" and move the release to `ready` without a
// single test of the current series having run.
func (t VerificationTally) Done() bool {
	return t.Pending == 0 && t.Failed+t.Succeeded > 0
}

// CountVerifications counts a release's verification jobs.
//
// It serves to conclude `verifying`: the release advances only when every family has
// returned its verdict. Counting in the database rather than keeping state in memory
// is what makes the conclusion correct after a worker restart — the runs continue
// while it is not running.
//
// The kinds counted are passed by the caller: it decides what constitutes a
// verification, and that list lives next to the one that launches them.
func (db *DB) CountVerifications(
	ctx context.Context, releaseID int64, kinds []string, buildKind string,
) (VerificationTally, error) {
	var tally VerificationTally
	// The count covers only the last series. A release retried after a failure drags the
	// verifications of its previous attempts along: counting them would fail a release
	// whose second series passes entirely, with nothing saying so. The boundary is the
	// last build — a series' verifications are queued after it, and identifiers are
	// monotonic.
	err := db.pool.QueryRow(ctx,
		// The last attempt of each family, not all of them.
		//
		// A family retried after a failure left its previous attempts in the count: three
		// integration failures followed by a success counted three failures, and the release
		// stayed failed over verdicts the last attempt had replaced. The replay button could
		// therefore catch nothing up.
		`with derniers as (
		     select distinct on (kind) kind, state
		       from jobs
		      where release_id = $1
		        and kind = any($2)
		        and id > coalesce(
		            (select max(id) from jobs where release_id = $1 and kind = $3), 0)
		      order by kind, id desc
		 )
		 select
		     count(*) filter (where state in ('queued', 'running')),
		     count(*) filter (where state = 'failed'),
		     count(*) filter (where state = 'succeeded')
		 from derniers`,
		releaseID, kinds, buildKind).
		Scan(&tally.Pending, &tally.Failed, &tally.Succeeded)
	if err != nil {
		return VerificationTally{}, fmt.Errorf("verifications of release %d: %w", releaseID, err)
	}
	return tally, nil
}

// BuildTally counts a release's builds.
type BuildTally struct {
	Pending   int
	Failed    int
	Succeeded int
}

// Done says every image was built, and none failed.
//
// An empty series is not concluded, for the same reason as the verifications: between
// writing `building` and queueing the builds there is an instant when the count is
// zero, and a late redelivery would see "everything succeeded".
func (t BuildTally) Done() bool {
	return t.Pending == 0 && t.Failed == 0 && t.Succeeded > 0
}

// CountBuilds counts a release's builds, one per image.
//
// A release now builds several images — the pipeline handles one per run — and it is
// built only when all have succeeded. Counting in the database rather than keeping
// state in memory is what makes the conclusion correct after a worker restart.
//
// The last attempt of each image, not all of them: retrying a build would otherwise
// leave its previous attempts in the count, and a release whose retry passes entirely
// would stay failed over a verdict the last attempt replaced.
//
// Grouping is on the `image-name` parameter, the only thing saying which image a job
// builds. Jobs predating that parameter carry it as null and therefore group
// together, which is the previous behaviour.
func (db *DB) CountBuilds(ctx context.Context, releaseID int64, buildKind string) (BuildTally, error) {
	var tally BuildTally
	err := db.pool.QueryRow(ctx,
		`with derniers as (
		     select distinct on (params->>'image-name') params->>'image-name' as image, state
		       from jobs
		      where release_id = $1 and kind = $2
		      order by params->>'image-name', id desc
		 )
		 select
		     count(*) filter (where state in ('queued', 'running')),
		     count(*) filter (where state = 'failed'),
		     count(*) filter (where state = 'succeeded')
		 from derniers`,
		releaseID, buildKind).
		Scan(&tally.Pending, &tally.Failed, &tally.Succeeded)
	if err != nil {
		return BuildTally{}, fmt.Errorf("builds of release %d: %w", releaseID, err)
	}
	return tally, nil
}

// ReleaseBadges carries what a screen shows beside a release's name, without having
// to unfold it.
type ReleaseBadges struct {
	// BuildFailed says the last build failed.
	//
	// The last and not "a" build: a release retried after an infrastructure failure would
	// otherwise keep a failure badge nothing justifies.
	BuildFailed bool
}

// ReleaseBadgesByTag returns, for each release, what its badges are composed of.
//
// One read for every release rather than one per release: the list shows fifty, and
// fifty round trips for two booleans and an environment name would be paid every time
// the screen opens.
func (db *DB) ReleaseBadgesByTag(ctx context.Context) (map[string]ReleaseBadges, error) {
	badges := map[string]ReleaseBadges{}

	// The state of each release's last build.
	builds, err := db.pool.Query(ctx,
		`select distinct on (r.tag) r.tag, j.state
		   from releases r
		   join jobs j on j.release_id = r.id and j.kind = 'build'
		  order by r.tag, j.id desc`)
	if err != nil {
		return nil, fmt.Errorf("builds of the releases: %w", err)
	}
	defer builds.Close()

	for builds.Next() {
		var tag, state string
		if err := builds.Scan(&tag, &state); err != nil {
			return nil, fmt.Errorf("builds of the releases: %w", err)
		}
		entry := badges[tag]
		entry.BuildFailed = state == JobFailed
		badges[tag] = entry
	}
	if err := builds.Err(); err != nil {
		return nil, fmt.Errorf("builds of the releases: %w", err)
	}

	// The environments where a release runs are not read here: they belong to the MSE
	// plugin, which supplies them to the screen.
	return badges, nil
}
