package store

import (
	"context"
	"fmt"
)

// Environment décrit un environnement de déploiement.
type Environment struct {
	ID    string
	Label string
	// PromotionSuffix est la métadonnée SemVer attendue par Flux dans le tag.
	// Une erreur ici ne produit aucun échec visible : le tag est poussé, Flux
	// l'ignore, et la promotion paraît réussie. D'où la vérification au démarrage
	// contre les ImagePolicy réelles.
	PromotionSuffix   string
	AcceptsPrerelease bool
	Rank              int
	Namespace         string
	FluxKustomization string
	// Observable vaut false pour la production, dans un cluster indépendant : ni
	// PortalData, ni Deployment, ni statut Flux n'y sont lisibles.
	Observable bool
	Visible    bool
}

// ListEnvironments rend les environnements dans leur ordre de promotion.
func (db *DB) ListEnvironments(ctx context.Context) ([]Environment, error) {
	rows, err := db.pool.Query(ctx,
		`select id, label, promotion_suffix, accepts_prerelease, rank, namespace,
		        coalesce(flux_kustomization, ''), observable, visible
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
		if err := rows.Scan(&environment.ID, &environment.Label, &environment.PromotionSuffix,
			&environment.AcceptsPrerelease, &environment.Rank, &environment.Namespace,
			&environment.FluxKustomization, &environment.Observable, &environment.Visible); err != nil {
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
		     id, label, promotion_suffix, accepts_prerelease, rank, namespace,
		     flux_kustomization, observable, visible
		 ) values ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8, $9)
		 on conflict (id) do update set
		     label = excluded.label,
		     promotion_suffix = excluded.promotion_suffix,
		     accepts_prerelease = excluded.accepts_prerelease,
		     rank = excluded.rank,
		     namespace = excluded.namespace,
		     flux_kustomization = excluded.flux_kustomization,
		     observable = excluded.observable,
		     visible = excluded.visible,
		     updated_at = now()`,
		environment.ID, environment.Label, environment.PromotionSuffix,
		environment.AcceptsPrerelease, environment.Rank, environment.Namespace,
		environment.FluxKustomization, environment.Observable, environment.Visible)
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
	FluxState      string
	// Resolution dit d'où vient l'information : `portaldata` atteste qu'une
	// version tourne et que ses migrations ont abouti, `git` seulement que Flux a
	// retenu ce tag, `unobservable` qu'on ne peut rien en dire.
	Resolution string
}

// SaveObservation enregistre l'état constaté d'un environnement.
func (db *DB) SaveObservation(ctx context.Context, observation Observation) error {
	_, err := db.pool.Exec(ctx,
		`insert into environment_observations (
		     environment_id, observed_at, portal_version, migration_state, flux_state, resolution
		 ) values ($1, now(), nullif($2, ''), nullif($3, ''), nullif($4, ''), $5)
		 on conflict (environment_id) do update set
		     observed_at = now(),
		     portal_version = excluded.portal_version,
		     migration_state = excluded.migration_state,
		     flux_state = excluded.flux_state,
		     resolution = excluded.resolution,
		     updated_at = now()`,
		observation.EnvironmentID, observation.PortalVersion, observation.MigrationState,
		observation.FluxState, observation.Resolution)
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
		`select e.id, e.label, e.promotion_suffix, e.accepts_prerelease, e.rank, e.namespace,
		        coalesce(e.flux_kustomization, ''), e.observable, e.visible,
		        o.portal_version, o.migration_state, o.flux_state, o.resolution,
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
		var portalVersion, migrationState, fluxState, resolution, observedAt *string

		if err := rows.Scan(&view.Environment.ID, &view.Environment.Label,
			&view.Environment.PromotionSuffix, &view.Environment.AcceptsPrerelease,
			&view.Environment.Rank, &view.Environment.Namespace,
			&view.Environment.FluxKustomization, &view.Environment.Observable,
			&view.Environment.Visible,
			&portalVersion, &migrationState, &fluxState, &resolution, &observedAt); err != nil {
			return nil, fmt.Errorf("vue des environnements: %w", err)
		}

		if resolution != nil {
			view.Observation = &Observation{
				EnvironmentID:  view.Environment.ID,
				PortalVersion:  value(portalVersion),
				MigrationState: value(migrationState),
				FluxState:      value(fluxState),
				Resolution:     *resolution,
			}
			view.ObservedAt = observedAt
		}

		views = append(views, view)
	}
	return views, rows.Err()
}
