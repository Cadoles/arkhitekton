package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Environment décrit un environnement de déploiement d'un projet.
type Environment struct {
	// ProjectID est le projet qui déploie vers cet environnement. Deux projets
	// peuvent chacun avoir un `qualif` (doc 16 §16.18).
	ProjectID int64
	ID        string
	Label     string
	Rank      int
	Namespace string
	// DeployerSettings porte les réglages de Flux, dans son vocabulaire.
	DeployerSettings map[string]string
	// Observable vaut false pour la production, dans un cluster indépendant : ni
	// attestation, ni Deployment, ni statut du déployeur n'y sont lisibles.
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
			return nil, fmt.Errorf("liste des environnements: %w", err)
		}
		environments = append(environments, environment)
	}
	return environments, rows.Err()
}

// ListEnvironments rend les environnements d'un projet dans leur ordre de
// promotion.
func (s *Store) ListEnvironments(ctx context.Context, projectID int64) ([]Environment, error) {
	rows, err := s.pool.Query(ctx,
		`select `+environmentColumns+`
		 from environments
		 where project_id = $1
		 -- Le rang est l'ordre de promotion : unstable, qualif, préprod, prod.
		 order by rank`, projectID)
	if err != nil {
		return nil, fmt.Errorf("liste des environnements: %w", err)
	}
	return scanEnvironments(rows)
}

// ListAllEnvironments rend les environnements de tous les projets. Sert à
// l'observation, qui relève chacun d'eux.
func (s *Store) ListAllEnvironments(ctx context.Context) ([]Environment, error) {
	rows, err := s.pool.Query(ctx,
		`select `+environmentColumns+` from environments order by project_id, rank`)
	if err != nil {
		return nil, fmt.Errorf("liste des environnements: %w", err)
	}
	return scanEnvironments(rows)
}

// UpsertEnvironment enregistre ou met à jour un environnement.
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
		return fmt.Errorf("enregistrement de l'environnement %q du projet %d: %w",
			environment.ID, environment.ProjectID, err)
	}
	return nil
}

// Observation est ce que l'environnement dit réellement de lui-même.
type Observation struct {
	ProjectID     int64
	EnvironmentID string
	// PortalVersion est la version de l'image ayant réalisé le dernier bootstrap
	// réussi, à rapprocher de `releases.image_version` et jamais du tag.
	PortalVersion  string
	MigrationState string
	// DeclaredState est l'état que Flux rapporte. Une **intention** : il dit ce
	// qu'il a appliqué, pas ce qui tourne.
	DeclaredState string
	// Resolution dit d'où vient l'information : `portaldata` atteste qu'une
	// version tourne et que ses migrations ont abouti, `git` seulement que Flux a
	// retenu ce tag, `unobservable` qu'on ne peut rien en dire.
	Resolution string
}

// SaveObservation enregistre l'état constaté d'un environnement.
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
		return fmt.Errorf("observation de %q: %w", observation.EnvironmentID, err)
	}
	return nil
}

// EnvironmentView associe un environnement à sa dernière observation.
type EnvironmentView struct {
	Environment Environment
	Observation *Observation
	ObservedAt  *string
}

// ListEnvironmentViews rend les environnements visibles d'un projet et ce qu'on
// sait d'eux.
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
		return nil, fmt.Errorf("vue des environnements: %w", err)
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
			return nil, fmt.Errorf("vue des environnements: %w", err)
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

// DeployedRelease est la release qu'un environnement fait tourner.
//
// « Fait tourner » et non « a reçu » : une promotion pousse un tag ; ce qui tourne
// est ce que `PortalData` rapporte. Les deux peuvent différer — pendant une
// réconciliation, ou indéfiniment si Flux a ignoré le tag.
type DeployedRelease struct {
	Tag string
	// ImageVersion est la version que l'image porte, et c'est **elle** qui a été
	// rapprochée de l'observation (B17).
	ImageVersion string
	Kind         string
	State        string
	GitRef       string
	CreatedBy    string
	CreatedAt    time.Time
	// PromotedAt date la promotion vers cet environnement. Nil quand la version
	// observée correspond à une release que le dashboard n'a jamais promue ici.
	PromotedAt  *time.Time
	PromotedTag string
	// Ambiguous vaut true quand plusieurs releases portent la version observée et
	// qu'aucune promotion ne tranche. Le re-tag rend ce cas normal.
	Ambiguous bool
}

// ErrNoDeployedRelease signale une version observée qui ne correspond à aucune
// release connue. Cas normal : le dashboard ne connaît que les releases créées
// par lui.
var ErrNoDeployedRelease = errors.New("aucune release connue ne correspond à la version observée")

// DeployedRelease rapproche la version observée sur un environnement d'un projet
// de la release de ce projet qui la porte.
//
// La jointure se fait sur `image_version` et jamais sur le tag (B8, B17), et
// **dans le projet** : une release d'un autre projet portant la même version
// d'image ne tourne pas ici. L'ambiguïté est résolue par la promotion.
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
		    -- Jointure externe : une release peut porter la version observée sans
		    -- avoir été promue ici par le dashboard.
		    left join image_promotions p
		         on p.release_id = r.id and p.project_id = $1 and p.environment_id = $2
		    where r.project_id = $1
		)
		select tag, image_version, kind, state, git_ref, created_by, created_at,
		       pushed_at, promoted_tag, total
		from candidates
		-- La promotion tranche ; à défaut, la plus récemment créée.
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
		return release, fmt.Errorf("release déployée sur %q: %w", environmentID, err)
	}

	release.Ambiguous = total > 1 && release.PromotedAt == nil
	return release, nil
}
