package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Environment describes a project's deployment environment.
type Environment struct {
	// ProjectID is the project deploying to this environment. Two projects may each have a
	// `qualif`.
	ProjectID int64
	ID        string
	Label     string
	Rank      int
	Namespace string
	// DeployerSettings carries Flux's settings, in its vocabulary.
	DeployerSettings map[string]string
	// Observable is false for production, in an independent cluster: neither attestation, nor
	// Deployment, nor deployer status is readable there.
	Observable bool
	Visible    bool
}

const environmentColumns = `project_id, id, label, rank, namespace, deployer_settings, observable, visible`

func scanEnvironments(rows pgx.Rows) ([]Environment, error) {
	defer rows.Close()
	var environments []Environment
	for rows.Next() {
		var environment Environment
		if err := rows.Scan(&environment.ProjectID, &environment.ID, &environment.Label,
			&environment.Rank, &environment.Namespace, &environment.DeployerSettings,
			&environment.Observable, &environment.Visible); err != nil {
			return nil, fmt.Errorf("listing the environments: %w", err)
		}
		environments = append(environments, environment)
	}
	return environments, rows.Err()
}

// ListEnvironments returns a project's environments in their promotion order.
func (s *Store) ListEnvironments(ctx context.Context, projectID int64) ([]Environment, error) {
	rows, err := s.pool.Query(ctx,
		`select `+environmentColumns+`
		 from environments
		 where project_id = $1
		 -- The rank is the promotion order: unstable, qualif, preprod, prod.
		 order by rank`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing the environments: %w", err)
	}
	return scanEnvironments(rows)
}

// ListAllEnvironments returns every project's environments. Serves the observation, which reads
// each of them.
func (s *Store) ListAllEnvironments(ctx context.Context) ([]Environment, error) {
	rows, err := s.pool.Query(ctx,
		`select `+environmentColumns+` from environments order by project_id, rank`)
	if err != nil {
		return nil, fmt.Errorf("listing the environments: %w", err)
	}
	return scanEnvironments(rows)
}

// UpsertEnvironment records or updates an environment.
func (s *Store) UpsertEnvironment(ctx context.Context, environment Environment) error {
	_, err := s.pool.Exec(ctx,
		`insert into environments (
		     project_id, id, label, rank, namespace, deployer_settings, observable, visible
		 ) values ($1, $2, $3, $4, $5, coalesce($6, '{}'::jsonb), $7, $8)
		 on conflict (project_id, id) do update set
		     label = excluded.label,
		     rank = excluded.rank,
		     namespace = excluded.namespace,
		     deployer_settings = excluded.deployer_settings,
		     observable = excluded.observable,
		     visible = excluded.visible,
		     updated_at = now()`,
		environment.ProjectID, environment.ID, environment.Label, environment.Rank,
		environment.Namespace, environment.DeployerSettings, environment.Observable,
		environment.Visible)
	if err != nil {
		return fmt.Errorf("recording environment %q of project %d: %w",
			environment.ID, environment.ProjectID, err)
	}
	return nil
}

// Observation is what the environment really says about itself.
type Observation struct {
	ProjectID     int64
	EnvironmentID string
	// PortalVersion is the version of the image that performed the last successful bootstrap, to be
	// matched against `releases.image_version` and never against the tag.
	PortalVersion  string
	MigrationState string
	// DeclaredState is the state Flux reports. An **intention**: it says what it applied, not what
	// is running.
	DeclaredState string
	// Resolution says where the information comes from: `portaldata` attests that a version is
	// running and that its migrations completed, `git` only that Flux kept this tag, `unobservable`
	// that nothing can be said about it.
	Resolution string
}

// SaveObservation records an environment's observed state.
func (s *Store) SaveObservation(ctx context.Context, observation Observation) error {
	_, err := s.pool.Exec(ctx,
		`insert into environment_observations (
		     project_id, environment_id, observed_at, portal_version, migration_state,
		     declared_state, resolution
		 ) values ($1, $2, now(), nullif($3, ''), nullif($4, ''), nullif($5, ''), $6)
		 on conflict (project_id, environment_id) do update set
		     observed_at = now(),
		     portal_version = excluded.portal_version,
		     migration_state = excluded.migration_state,
		     declared_state = excluded.declared_state,
		     resolution = excluded.resolution,
		     updated_at = now()`,
		observation.ProjectID, observation.EnvironmentID, observation.PortalVersion,
		observation.MigrationState, observation.DeclaredState, observation.Resolution)
	if err != nil {
		return fmt.Errorf("observation of %q: %w", observation.EnvironmentID, err)
	}
	return nil
}

// EnvironmentView associates an environment with its last observation.
type EnvironmentView struct {
	Environment Environment
	Observation *Observation
	ObservedAt  *string
}

// ListEnvironmentViews returns a project's visible environments and what is known about them.
func (s *Store) ListEnvironmentViews(ctx context.Context, projectID int64) ([]EnvironmentView, error) {
	rows, err := s.pool.Query(ctx,
		`select e.project_id, e.id, e.label, e.rank, e.namespace, e.deployer_settings,
		        e.observable, e.visible,
		        o.portal_version, o.migration_state, o.declared_state, o.resolution,
		        to_char(o.observed_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
		 from environments e
		 left join environment_observations o
		        on o.project_id = e.project_id and o.environment_id = e.id
		 where e.project_id = $1 and e.visible
		 order by e.rank`, projectID)
	if err != nil {
		return nil, fmt.Errorf("view of the environments: %w", err)
	}
	defer rows.Close()

	var views []EnvironmentView
	for rows.Next() {
		var view EnvironmentView
		var portalVersion, migrationState, declaredState, resolution, observedAt *string

		if err := rows.Scan(&view.Environment.ProjectID, &view.Environment.ID,
			&view.Environment.Label, &view.Environment.Rank, &view.Environment.Namespace,
			&view.Environment.DeployerSettings, &view.Environment.Observable,
			&view.Environment.Visible,
			&portalVersion, &migrationState, &declaredState, &resolution, &observedAt); err != nil {
			return nil, fmt.Errorf("view of the environments: %w", err)
		}

		if resolution != nil {
			view.Observation = &Observation{
				ProjectID:      view.Environment.ProjectID,
				EnvironmentID:  view.Environment.ID,
				PortalVersion:  value(portalVersion),
				MigrationState: value(migrationState),
				DeclaredState:  value(declaredState),
				Resolution:     *resolution,
			}
			view.ObservedAt = observedAt
		}

		views = append(views, view)
	}
	return views, rows.Err()
}

// DeployedRelease is the release an environment is running.
//
// "Is running" and not "received": a promotion pushes a tag; what is running is what
// `PortalData` reports. The two may differ — during a reconciliation, or indefinitely if Flux
// ignored the tag.
type DeployedRelease struct {
	Tag string
	// ImageVersion is the version the image carries, and it is **that** which was matched against
	// the observation.
	ImageVersion string
	Kind         string
	State        string
	GitRef       string
	CreatedBy    string
	CreatedAt    time.Time
	// PromotedAt dates the promotion to this environment. Nil when the version observed corresponds
	// to a release the dashboard never promoted here.
	PromotedAt  *time.Time
	PromotedTag string
	// Ambiguous is true when several releases carry the version observed and no promotion settles
	// it. The re-tag makes this case normal.
	Ambiguous bool
}

// ErrNoDeployedRelease reports a version observed that corresponds to no known release. A normal
// case: the dashboard knows only the releases it created.
var ErrNoDeployedRelease = errors.New("no known release matches the version observed")

// DeployedRelease matches the version observed on a project's environment with the release of
// that project carrying it.
//
// The join is done on `image_version` and never on the tag, and **within the project**: a
// release of another project carrying the same image version is not running here. Ambiguity is
// resolved by the promotion.
func (s *Store) DeployedRelease(ctx context.Context, projectID int64, environmentID string) (DeployedRelease, error) {
	var release DeployedRelease

	const query = `
		with observed as (
		    select portal_version from environment_observations
		    where project_id = $1 and environment_id = $2 and portal_version is not null
		),
		candidates as (
		    select r.tag, r.image_version, r.kind, r.state,
		           coalesce(r.git_ref, '') as git_ref, r.created_by, r.created_at,
		           p.pushed_at, coalesce(p.tag, '') as promoted_tag,
		           count(*) over () as total
		    from releases r
		    join observed o on o.portal_version = r.image_version
		    -- Outer join: a release may carry the version observed without having been
		    -- promoted here by the dashboard.
		    left join image_promotions p
		         on p.release_id = r.id and p.project_id = $1 and p.environment_id = $2
		    where r.project_id = $1
		)
		select tag, image_version, kind, state, git_ref, created_by, created_at,
		       pushed_at, promoted_tag, total
		from candidates
		-- The promotion settles it; failing that, the most recently created one.
		order by pushed_at desc nulls last, created_at desc
		limit 1`

	var total int
	err := s.pool.QueryRow(ctx, query, projectID, environmentID).Scan(
		&release.Tag, &release.ImageVersion, &release.Kind, &release.State,
		&release.GitRef, &release.CreatedBy, &release.CreatedAt,
		&release.PromotedAt, &release.PromotedTag, &total)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return release, ErrNoDeployedRelease
		}
		return release, fmt.Errorf("release deployed on %q: %w", environmentID, err)
	}

	release.Ambiguous = total > 1 && release.PromotedAt == nil
	return release, nil
}
