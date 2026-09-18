package storage

import (
	"context"
	"fmt"
	"time"
)

// Promotion is a promotion tag pushed for an environment.
type Promotion struct {
	ReleaseID     int64
	EnvironmentID string
	ImageName     string
	Tag           string
	Digest        string
	JobID         *int64
	PushedAt      time.Time
}

// RecordPromotion records a requested promotion.
//
// "Requested" and not "performed": pushing the tag deploys nothing. That is what allows telling
// "promoted but not deployed" from "never promoted".
//
// The project is the release's: reading it from the database rather than receiving it stops a
// promotion of a release to another project's environment being recorded.
func (s *Store) RecordPromotion(ctx context.Context, promotion Promotion) error {
	_, err := s.pool.Exec(ctx,
		`insert into image_promotions (
		     project_id, release_id, environment_id, image_name, tag, digest, pushed_at, job_id
		 ) values ((select project_id from releases where id = $1),
		           $1, $2, $3, $4, nullif($5, ''), now(), $6)
		 -- Promoting an environment again is legitimate: a retry after a failure, or a
		 -- correction. The last request is authoritative.
		 on conflict (release_id, environment_id, image_name) do update set
		     tag = excluded.tag,
		     digest = coalesce(excluded.digest, image_promotions.digest),
		     pushed_at = excluded.pushed_at,
		     job_id = excluded.job_id`,
		promotion.ReleaseID, promotion.EnvironmentID, promotion.ImageName,
		promotion.Tag, promotion.Digest, promotion.JobID)
	if err != nil {
		return fmt.Errorf("recording the promotion of %q: %w", promotion.Tag, err)
	}
	return nil
}

// ListPromotions returns a release's promotions.
func (s *Store) ListPromotions(ctx context.Context, releaseID int64) ([]Promotion, error) {
	rows, err := s.pool.Query(ctx,
		`select environment_id, image_name, tag, coalesce(digest, ''), pushed_at, job_id
		 from image_promotions where release_id = $1 order by pushed_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("promotions of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var promotions []Promotion
	for rows.Next() {
		promotion := Promotion{ReleaseID: releaseID}
		if err := rows.Scan(&promotion.EnvironmentID, &promotion.ImageName, &promotion.Tag,
			&promotion.Digest, &promotion.PushedAt, &promotion.JobID); err != nil {
			return nil, fmt.Errorf("promotions of release %d: %w", releaseID, err)
		}
		promotions = append(promotions, promotion)
	}
	return promotions, rows.Err()
}

// EnvironmentPromotion is the last promotion requested for an environment.
type EnvironmentPromotion struct {
	EnvironmentID string
	ReleaseTag    string
	Tag           string
	PushedAt      time.Time
	JobID         *int64
}

// LatestPromotions returns, per environment of a project, the last promotion requested.
//
// The missing half of the observation: the observation says what is running, this says what was
// requested.
func (s *Store) LatestPromotions(ctx context.Context, projectID int64) (map[string]EnvironmentPromotion, error) {
	rows, err := s.pool.Query(ctx,
		`select distinct on (p.environment_id)
		        p.environment_id, r.tag, p.tag, p.pushed_at, p.job_id
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 where p.project_id = $1
		 order by p.environment_id, p.pushed_at desc`, projectID)
	if err != nil {
		return nil, fmt.Errorf("latest promotions: %w", err)
	}
	defer rows.Close()

	latest := make(map[string]EnvironmentPromotion)
	for rows.Next() {
		var promotion EnvironmentPromotion
		if err := rows.Scan(&promotion.EnvironmentID, &promotion.ReleaseTag,
			&promotion.Tag, &promotion.PushedAt, &promotion.JobID); err != nil {
			return nil, fmt.Errorf("latest promotions: %w", err)
		}
		latest[promotion.EnvironmentID] = promotion
	}
	return latest, rows.Err()
}

// EnvironmentHistory is a promotion requested for an environment.
type EnvironmentHistory struct {
	ReleaseTag   string
	ImageVersion string
	Tag          string
	PushedAt     time.Time
	JobID        *int64
	JobState     string
	RequestedBy  string
}

// ListEnvironmentHistory returns the sequence of promotions of a project's environment.
//
// The source is what the dashboard requested, and not the history of `mse-kustom`'s commits: a
// promotion refused by an ImagePolicy appears nowhere on the Git side, although it was indeed
// requested.
func (s *Store) ListEnvironmentHistory(
	ctx context.Context, projectID int64, environmentID string, limit int,
) ([]EnvironmentHistory, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.pool.Query(ctx,
		`select r.tag, r.image_version, p.tag, p.pushed_at, p.job_id,
		        coalesce(j.state, ''), coalesce(j.requested_by, '')
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 left join jobs j on j.id = p.job_id
		 where p.project_id = $1 and p.environment_id = $2
		 order by p.pushed_at desc
		 limit $3`, projectID, environmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("history of %q: %w", environmentID, err)
	}
	defer rows.Close()

	var history []EnvironmentHistory
	for rows.Next() {
		var entry EnvironmentHistory
		if err := rows.Scan(&entry.ReleaseTag, &entry.ImageVersion, &entry.Tag,
			&entry.PushedAt, &entry.JobID, &entry.JobState, &entry.RequestedBy); err != nil {
			return nil, fmt.Errorf("history of %q: %w", environmentID, err)
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// DeployedByRelease returns, per release tag of a project, the environments of that project
// running it.
//
// By the same rule as [Store.DeployedRelease]: the version observed names the project's release,
// a recorded promotion settles between several candidates, failing that the most recently
// created one.
func (s *Store) DeployedByRelease(ctx context.Context, projectID int64) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx,
		`select distinct on (e.id) e.id, r.tag
		   from environments e
		   join environment_observations o
		     on o.project_id = e.project_id and o.environment_id = e.id
		    and o.portal_version is not null
		   join releases r
		     on r.image_version = o.portal_version and r.project_id = e.project_id
		   left join image_promotions p
		     on p.release_id = r.id and p.project_id = e.project_id and p.environment_id = e.id
		  where e.project_id = $1 and e.visible
		  order by e.id, p.pushed_at desc nulls last, r.created_at desc`, projectID)
	if err != nil {
		return nil, fmt.Errorf("deployments of the releases: %w", err)
	}
	defer rows.Close()

	deployed := map[string][]string{}
	for rows.Next() {
		var environment, tag string
		if err := rows.Scan(&environment, &tag); err != nil {
			return nil, fmt.Errorf("deployments of the releases: %w", err)
		}
		deployed[tag] = append(deployed[tag], environment)
	}
	return deployed, rows.Err()
}
