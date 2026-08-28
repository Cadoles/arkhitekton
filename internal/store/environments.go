package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Environment décrit un environnement de déploiement.
type Environment struct {
	ID        string
	Label     string
	Rank      int
	Namespace string
	// DeployerSettings porte ce dont l'outil de déploiement a besoin, dans son
	// vocabulaire. Le magasin le **transporte sans le lire** : c'est ce qui
	// permet d'ajouter un déployeur sans ajouter de colonne (migration 0033).
	DeployerSettings map[string]string
	// Observable vaut false pour la production, dans un cluster indépendant : ni
	// attestation, ni Deployment, ni statut du déployeur n'y sont lisibles.
	Observable bool
	Visible    bool
}

// ListEnvironments rend les environnements dans leur ordre de promotion.
func (db *DB) ListEnvironments(ctx context.Context) ([]Environment, error) {
	rows, err := db.pool.Query(ctx,
		`select id, label, rank, namespace, deployer_settings, observable, visible
		 from environments
		 -- Le rang est l'ordre de promotion, seul ordre qui ait un sens ici :
		 -- unstable, qualif, préprod, prod.
		 order by rank`)
	if err != nil {
		return nil, fmt.Errorf("liste des environnements: %w", err)
	}
	defer rows.Close()

	var environments []Environment
	for rows.Next() {
		var environment Environment
		if err := rows.Scan(&environment.ID, &environment.Label, &environment.Rank,
			&environment.Namespace, &environment.DeployerSettings,
			&environment.Observable, &environment.Visible); err != nil {
			return nil, fmt.Errorf("liste des environnements: %w", err)
		}
		environments = append(environments, environment)
	}
	return environments, rows.Err()
}

// UpsertEnvironment enregistre ou met à jour un environnement.
func (db *DB) UpsertEnvironment(ctx context.Context, environment Environment) error {
	_, err := db.pool.Exec(ctx,
		`insert into environments (
		     id, label, rank, namespace, deployer_settings, observable, visible
		 ) values ($1, $2, $3, $4, coalesce($5, '{}'::jsonb), $6, $7)
		 on conflict (id) do update set
		     label = excluded.label,
		     rank = excluded.rank,
		     namespace = excluded.namespace,
		     deployer_settings = excluded.deployer_settings,
		     observable = excluded.observable,
		     visible = excluded.visible,
		     updated_at = now()`,
		environment.ID, environment.Label, environment.Rank, environment.Namespace,
		environment.DeployerSettings, environment.Observable, environment.Visible)
	if err != nil {
		return fmt.Errorf("enregistrement de l'environnement %q: %w", environment.ID, err)
	}
	return nil
}

// Observation est ce que l'environnement dit réellement de lui-même.
type Observation struct {
	EnvironmentID string
	// PortalVersion est la version de l'image ayant réalisé le dernier bootstrap
	// réussi, à rapprocher de `releases.image_version` et jamais du tag.
	PortalVersion  string
	MigrationState string
	// DeclaredState est l'état que l'outil de déploiement rapporte. Une
	// **intention** : il dit ce qu'il a appliqué, pas ce qui tourne.
	DeclaredState string
	// Resolution dit d'où vient l'information : `portaldata` atteste qu'une
	// version tourne et que ses migrations ont abouti, `git` seulement que Flux a
	// retenu ce tag, `unobservable` qu'on ne peut rien en dire.
	Resolution string
}

// SaveObservation enregistre l'état constaté d'un environnement.
func (db *DB) SaveObservation(ctx context.Context, observation Observation) error {
	_, err := db.pool.Exec(ctx,
		`insert into environment_observations (
		     environment_id, observed_at, portal_version, migration_state, declared_state, resolution
		 ) values ($1, now(), nullif($2, ''), nullif($3, ''), nullif($4, ''), $5)
		 on conflict (environment_id) do update set
		     observed_at = now(),
		     portal_version = excluded.portal_version,
		     migration_state = excluded.migration_state,
		     declared_state = excluded.declared_state,
		     resolution = excluded.resolution,
		     updated_at = now()`,
		observation.EnvironmentID, observation.PortalVersion, observation.MigrationState,
		observation.DeclaredState, observation.Resolution)
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

// ListEnvironmentViews rend les environnements visibles et ce qu'on sait d'eux.
func (db *DB) ListEnvironmentViews(ctx context.Context) ([]EnvironmentView, error) {
	rows, err := db.pool.Query(ctx,
		`select e.id, e.label, e.rank, e.namespace, e.deployer_settings,
		        e.observable, e.visible,
		        o.portal_version, o.migration_state, o.declared_state, o.resolution,
		        to_char(o.observed_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
		 from environments e
		 left join environment_observations o on o.environment_id = e.id
		 where e.visible
		 order by e.rank`)
	if err != nil {
		return nil, fmt.Errorf("vue des environnements: %w", err)
	}
	defer rows.Close()

	var views []EnvironmentView
	for rows.Next() {
		var view EnvironmentView
		var portalVersion, migrationState, declaredState, resolution, observedAt *string

		if err := rows.Scan(&view.Environment.ID, &view.Environment.Label,
			&view.Environment.Rank, &view.Environment.Namespace,
			&view.Environment.DeployerSettings, &view.Environment.Observable,
			&view.Environment.Visible,
			&portalVersion, &migrationState, &declaredState, &resolution, &observedAt); err != nil {
			return nil, fmt.Errorf("vue des environnements: %w", err)
		}

		if resolution != nil {
			view.Observation = &Observation{
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
// « Fait tourner » et non « a reçu » : la distinction est tout l'objet de ce
// type. Une promotion pousse un tag ; ce qui tourne est ce que `PortalData`
// rapporte. Les deux peuvent différer — pendant une réconciliation, ou
// indéfiniment si Flux a ignoré le tag.
type DeployedRelease struct {
	Tag string
	// ImageVersion est la version que l'image porte, et c'est **elle** qui a été
	// rapprochée de l'observation. Comparer au tag échouerait sur toute release
	// finale produite par re-tag (B17).
	ImageVersion string
	Kind         string
	State        string
	GitRef       string
	CreatedBy    string
	CreatedAt    time.Time
	// PromotedAt date la promotion vers cet environnement. Nil quand la version
	// observée correspond à une release que le dashboard n'a jamais promue ici —
	// un déploiement fait avant lui, ou par un autre chemin.
	PromotedAt  *time.Time
	PromotedTag string
	// Ambiguous vaut true quand plusieurs releases portent la version observée et
	// qu'aucune promotion ne tranche. Le re-tag rend ce cas normal : la RC et la
	// finale partagent la même image. Le taire ferait afficher l'une pour l'autre.
	Ambiguous bool
}

// ErrNoDeployedRelease signale une version observée qui ne correspond à aucune
// release connue.
//
// Cas parfaitement normal : l'application est déployée depuis des années, et le
// dashboard n'a connaissance que des releases créées par lui. L'interface doit le
// dire plutôt que présenter un écran vide.
var ErrNoDeployedRelease = errors.New("aucune release connue ne correspond à la version observée")

// DeployedRelease rapproche la version observée sur un environnement de la
// release qui la porte.
//
// La jointure se fait sur `image_version` et jamais sur le tag : ce que
// `PortalData` rapporte est la version bakée dans l'image, restée celle de la RC
// pour toute release finale produite par re-tag (B8, B17).
//
// L'ambiguïté est résolue par la promotion, non par une heuristique : quand une
// RC et sa finale partagent la même image, c'est celle qui a été promue ici qui
// est déployée. Deviner par la date de création choisirait la RC, c'est-à-dire
// systématiquement la mauvaise dès qu'une finale existe.
func (db *DB) DeployedRelease(ctx context.Context, environmentID string) (DeployedRelease, error) {
	var release DeployedRelease

	const query = `
		with observed as (
		    select portal_version from environment_observations
		    where environment_id = $1 and portal_version is not null
		),
		candidates as (
		    -- Les colonnes calculées portent un alias : sans lui, le select
		    -- extérieur ne sait pas les nommer.
		    select r.tag, r.image_version, r.kind, r.state,
		           coalesce(r.git_ref, '') as git_ref, r.created_by, r.created_at,
		           p.pushed_at, coalesce(p.tag, '') as promoted_tag,
		           count(*) over () as total
		    from releases r
		    join observed o on o.portal_version = r.image_version
		    -- Jointure externe : une release peut porter la version observée sans
		    -- avoir été promue ici par le dashboard.
		    left join image_promotions p
		         on p.release_id = r.id and p.environment_id = $1
		)
		select tag, image_version, kind, state, git_ref, created_by, created_at,
		       pushed_at, promoted_tag, total
		from candidates
		-- La promotion tranche : une release promue ici passe devant une release
		-- qui partage seulement son image. À défaut, la plus récemment créée.
		order by pushed_at desc nulls last, created_at desc
		limit 1`

	var total int
	err := db.pool.QueryRow(ctx, query, environmentID).Scan(
		&release.Tag, &release.ImageVersion, &release.Kind, &release.State,
		&release.GitRef, &release.CreatedBy, &release.CreatedAt,
		&release.PromotedAt, &release.PromotedTag, &total)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return release, ErrNoDeployedRelease
		}
		return release, fmt.Errorf("release déployée sur %q: %w", environmentID, err)
	}

	// Plusieurs candidates et aucune promotion pour les séparer : le dire.
	release.Ambiguous = total > 1 && release.PromotedAt == nil
	return release, nil
}
