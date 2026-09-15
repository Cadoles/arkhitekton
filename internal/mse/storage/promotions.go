package storage

import (
	"context"
	"fmt"
	"time"
)

// Promotion est un tag de promotion poussé pour un environnement.
type Promotion struct {
	ReleaseID     int64
	EnvironmentID string
	ImageName     string
	Tag           string
	Digest        string
	JobID         *int64
	PushedAt      time.Time
}

// RecordPromotion enregistre une promotion demandée.
//
// « Demandée » et non « effectuée » : pousser le tag ne déploie rien. C'est ce qui
// permet de distinguer « promu mais pas déployé » de « jamais promu » (B7).
//
// Le projet est celui de la release : le lire en base plutôt que le recevoir
// empêche d'enregistrer une promotion d'une release vers l'environnement d'un
// autre projet.
func (s *Store) RecordPromotion(ctx context.Context, promotion Promotion) error {
	_, err := s.pool.Exec(ctx,
		`insert into image_promotions (
		     project_id, release_id, environment_id, image_name, tag, digest, pushed_at, job_id
		 ) values ((select project_id from releases where id = $1),
		           $1, $2, $3, $4, nullif($5, ''), now(), $6)
		 -- Repromouvoir un environnement est légitime : reprise après échec, ou
		 -- correction. La dernière demande fait foi.
		 on conflict (release_id, environment_id, image_name) do update set
		     tag = excluded.tag,
		     digest = coalesce(excluded.digest, image_promotions.digest),
		     pushed_at = excluded.pushed_at,
		     job_id = excluded.job_id`,
		promotion.ReleaseID, promotion.EnvironmentID, promotion.ImageName,
		promotion.Tag, promotion.Digest, promotion.JobID)
	if err != nil {
		return fmt.Errorf("enregistrement de la promotion de %q: %w", promotion.Tag, err)
	}
	return nil
}

// ListPromotions rend les promotions d'une release.
func (s *Store) ListPromotions(ctx context.Context, releaseID int64) ([]Promotion, error) {
	rows, err := s.pool.Query(ctx,
		`select environment_id, image_name, tag, coalesce(digest, ''), pushed_at, job_id
		 from image_promotions where release_id = $1 order by pushed_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("promotions de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var promotions []Promotion
	for rows.Next() {
		promotion := Promotion{ReleaseID: releaseID}
		if err := rows.Scan(&promotion.EnvironmentID, &promotion.ImageName, &promotion.Tag,
			&promotion.Digest, &promotion.PushedAt, &promotion.JobID); err != nil {
			return nil, fmt.Errorf("promotions de la release %d: %w", releaseID, err)
		}
		promotions = append(promotions, promotion)
	}
	return promotions, rows.Err()
}

// EnvironmentPromotion est la dernière promotion demandée pour un environnement.
type EnvironmentPromotion struct {
	EnvironmentID string
	ReleaseTag    string
	Tag           string
	PushedAt      time.Time
	JobID         *int64
}

// LatestPromotions rend, par environnement d'un projet, la dernière promotion
// demandée.
//
// La moitié manquante de l'observation : celle-ci dit ce qui tourne, celle-là ce
// qu'on a demandé (B7, B8).
func (s *Store) LatestPromotions(ctx context.Context, projectID int64) (map[string]EnvironmentPromotion, error) {
	rows, err := s.pool.Query(ctx,
		`select distinct on (p.environment_id)
		        p.environment_id, r.tag, p.tag, p.pushed_at, p.job_id
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 where p.project_id = $1
		 order by p.environment_id, p.pushed_at desc`, projectID)
	if err != nil {
		return nil, fmt.Errorf("dernières promotions: %w", err)
	}
	defer rows.Close()

	latest := make(map[string]EnvironmentPromotion)
	for rows.Next() {
		var promotion EnvironmentPromotion
		if err := rows.Scan(&promotion.EnvironmentID, &promotion.ReleaseTag,
			&promotion.Tag, &promotion.PushedAt, &promotion.JobID); err != nil {
			return nil, fmt.Errorf("dernières promotions: %w", err)
		}
		latest[promotion.EnvironmentID] = promotion
	}
	return latest, rows.Err()
}

// EnvironmentHistory est une promotion demandée pour un environnement.
type EnvironmentHistory struct {
	ReleaseTag   string
	ImageVersion string
	Tag          string
	PushedAt     time.Time
	JobID        *int64
	JobState     string
	RequestedBy  string
}

// ListEnvironmentHistory rend la suite des promotions d'un environnement d'un
// projet.
//
// La source est ce que le dashboard a demandé, et non l'historique des commits de
// `mse-kustom` : une promotion refusée par une ImagePolicy n'apparaît nulle part
// côté Git, alors qu'elle a bien été demandée.
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
		return nil, fmt.Errorf("historique de %q: %w", environmentID, err)
	}
	defer rows.Close()

	var history []EnvironmentHistory
	for rows.Next() {
		var entry EnvironmentHistory
		if err := rows.Scan(&entry.ReleaseTag, &entry.ImageVersion, &entry.Tag,
			&entry.PushedAt, &entry.JobID, &entry.JobState, &entry.RequestedBy); err != nil {
			return nil, fmt.Errorf("historique de %q: %w", environmentID, err)
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// DeployedByRelease rend, par tag de release d'un projet, les environnements de
// ce projet qui la font tourner.
//
// Par la même règle que [Store.DeployedRelease] : la version observée désigne la
// release du projet, une promotion enregistrée tranche entre plusieurs candidates,
// à défaut la plus récemment créée.
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
		return nil, fmt.Errorf("déploiements des releases: %w", err)
	}
	defer rows.Close()

	deployed := map[string][]string{}
	for rows.Next() {
		var environment, tag string
		if err := rows.Scan(&environment, &tag); err != nil {
			return nil, fmt.Errorf("déploiements des releases: %w", err)
		}
		deployed[tag] = append(deployed[tag], environment)
	}
	return deployed, rows.Err()
}
