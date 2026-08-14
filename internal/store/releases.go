package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Release est une release enregistrée.
type Release struct {
	ID        int64
	VersionID int64
	// Tag est l'identité de la release : `1.8.0-rc2`, `1.8.0`.
	Tag   string
	Kind  string
	State string
	// ImageVersion est la version portée par l'image, distincte du tag pour
	// toute release finale produite par re-tag : ce qui part en production est
	// bit pour bit ce qui a été validé, et son `version.txt` reste celui de la
	// RC (B17). Comparer PortalData au tag échouerait systématiquement.
	ImageVersion    string
	SourceReleaseID *int64
	GitRef          string
	CreatedBy       string
	CreatedAt       time.Time
	ReleasedAt      *time.Time
	// ManualActions est le récit libre des gestes manuels de la livraison, repris
	// tel quel dans le bon de livraison. L'auteur et la date l'accompagnent parce
	// que ce document est contractuel.
	ManualActions          string
	ManualActionsUpdatedBy string
	ManualActionsUpdatedAt *time.Time
}

// ErrReleaseNotFound signale une release absente.
var ErrReleaseNotFound = errors.New("release introuvable")

// ErrVersionNotFound signale une version Redmine absente.
//
// Distincte d'une erreur d'écriture : sans le lot 1, la table des versions est
// vide, et l'appelant doit pouvoir dire *pourquoi* la création est refusée plutôt
// que d'afficher une violation de contrainte.
var ErrVersionNotFound = errors.New("version introuvable")

// FindVersionByName retrouve une version Redmine par son nom.
//
// C'est le point de jointure de l'invariant : le tag d'une release est le nom de
// la version, éventuellement suivi d'un suffixe de pré-release (B17).
func (db *DB) FindVersionByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx, `select id from versions where name = $1`, name).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrVersionNotFound
		}
		return 0, fmt.Errorf("recherche de la version %q: %w", name, err)
	}
	return id, nil
}

// CreateRelease enregistre une release.
func (db *DB) CreateRelease(ctx context.Context, release Release) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into releases (
		     version_id, tag, kind, state, git_ref, image_version, source_release_id, created_by
		 ) values ($1, $2, $3, $4, $5, $6, $7, $8)
		 returning id`,
		release.VersionID, release.Tag, release.Kind, release.State,
		release.GitRef, release.ImageVersion, release.SourceReleaseID, release.CreatedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("création de la release %q: %w", release.Tag, err)
	}
	return id, nil
}

// GetRelease rend une release par son tag.
func (db *DB) GetRelease(ctx context.Context, tag string) (Release, error) {
	var release Release
	err := db.pool.QueryRow(ctx,
		`select id, version_id, tag, kind, state, coalesce(git_ref, ''), image_version,
		        source_release_id, created_by, created_at, released_at,
		        coalesce(manual_actions_md, ''), coalesce(manual_actions_updated_by, ''),
		        manual_actions_updated_at
		 from releases where tag = $1`, tag).
		Scan(&release.ID, &release.VersionID, &release.Tag, &release.Kind, &release.State,
			&release.GitRef, &release.ImageVersion, &release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt,
			&release.ManualActions, &release.ManualActionsUpdatedBy, &release.ManualActionsUpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return release, ErrReleaseNotFound
		}
		return release, fmt.Errorf("lecture de la release %q: %w", tag, err)
	}
	return release, nil
}

// ListReleases rend les releases les plus récentes d'abord.
func (db *DB) ListReleases(ctx context.Context, limit int) ([]Release, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := db.pool.Query(ctx,
		`select id, version_id, tag, kind, state, coalesce(git_ref, ''), image_version,
		        source_release_id, created_by, created_at, released_at
		 from releases order by created_at desc limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("liste des releases: %w", err)
	}
	defer rows.Close()

	var releases []Release
	for rows.Next() {
		var release Release
		if err := rows.Scan(&release.ID, &release.VersionID, &release.Tag, &release.Kind,
			&release.State, &release.GitRef, &release.ImageVersion, &release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt); err != nil {
			return nil, fmt.Errorf("liste des releases: %w", err)
		}
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

// SetReleaseState fait avancer une release.
//
// La transition autorisée est décidée par `internal/release`, hors de la base :
// une table de transitions dupliquée en SQL et en Go finirait par diverger, et
// c'est la version Go qui porte les messages d'erreur.
func (db *DB) SetReleaseState(ctx context.Context, id int64, state string) error {
	_, err := db.pool.Exec(ctx,
		`update releases set state = $2, updated_at = now() where id = $1`, id, state)
	if err != nil {
		return fmt.Errorf("état de la release %d: %w", id, err)
	}
	return nil
}

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
// « Demandée » et non « effectuée » : pousser le tag ne déploie rien. Flux
// constate ensuite, et c'est l'observation des environnements qui dira si la
// version est réellement livrée. Enregistrer ici ce qui a été **demandé** est ce
// qui permet plus tard de distinguer « promu mais pas déployé » de « jamais
// promu » (B7).
func (db *DB) RecordPromotion(ctx context.Context, promotion Promotion) error {
	_, err := db.pool.Exec(ctx,
		`insert into image_promotions (
		     release_id, environment_id, image_name, tag, digest, pushed_at, job_id
		 ) values ($1, $2, $3, $4, nullif($5, ''), now(), $6)
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
func (db *DB) ListPromotions(ctx context.Context, releaseID int64) ([]Promotion, error) {
	rows, err := db.pool.Query(ctx,
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

// EnsureVersion crée une version au besoin, et rend son identifiant.
//
// Sert tant que la synchronisation Redmine (lot 1) n'est pas en place : sans
// elle, la table des versions est vide et aucune release ne peut être créée,
// alors que tout le reste de la chaîne fonctionne.
//
// La ligne créée porte `redmine_id` négatif et un statut explicite : elle est
// reconnaissable, et la synchronisation la remplacera par la vraie version sans
// que les releases déjà rattachées perdent leur lien — la jointure se fait sur le
// nom, qui est l'invariant.
func (db *DB) EnsureVersion(ctx context.Context, name string) (int64, error) {
	id, err := db.FindVersionByName(ctx, name)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrVersionNotFound) {
		return 0, err
	}

	err = db.pool.QueryRow(ctx,
		`insert into versions (redmine_id, name, status, synced_at)
		 values (-nextval('versions_id_seq'), $1, 'en-attente-de-synchronisation', now())
		 returning id`, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("création de la version provisoire %q: %w", name, err)
	}
	return id, nil
}

// EnvironmentPromotion est la dernière promotion demandée pour un environnement.
type EnvironmentPromotion struct {
	EnvironmentID string
	ReleaseTag    string
	Tag           string
	PushedAt      time.Time
	JobID         *int64
}

// LatestPromotions rend, par environnement, la dernière promotion demandée.
//
// C'est la moitié manquante de l'observation : celle-ci dit ce qui tourne,
// celle-là ce qu'on a demandé. Sans les deux, « promu mais pas encore déployé »
// et « jamais promu » s'affichent à l'identique — or le premier cas se règle en
// attendant, le second en agissant (B7, B8).
func (db *DB) LatestPromotions(ctx context.Context) (map[string]EnvironmentPromotion, error) {
	rows, err := db.pool.Query(ctx,
		`select distinct on (p.environment_id)
		        p.environment_id, r.tag, p.tag, p.pushed_at, p.job_id
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 order by p.environment_id, p.pushed_at desc`)
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

// SetManualActions enregistre le récit des gestes manuels d'une livraison.
//
// L'auteur et l'horodatage sont posés ici et non fournis par l'appelant : ce
// texte finit dans un document contractuel, et sa provenance ne doit pas dépendre
// de ce qu'un client veut bien déclarer.
func (db *DB) SetManualActions(ctx context.Context, tag, markdown, author string) error {
	tally, err := db.pool.Exec(ctx,
		`update releases
		 set manual_actions_md = nullif($2, ''),
		     manual_actions_updated_by = $3,
		     manual_actions_updated_at = now(),
		     updated_at = now()
		 where tag = $1`, tag, markdown, author)
	if err != nil {
		return fmt.Errorf("actions manuelles de %q: %w", tag, err)
	}
	if tally.RowsAffected() == 0 {
		return ErrReleaseNotFound
	}
	return nil
}

// ReleaseJob est un job rattaché à une release.
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

// ListReleaseJobs rend ce qui a été déclenché pour une release.
//
// C'est le chemin entre une promotion et sa preuve : la promotion dit ce qui a
// été demandé, le job dit ce qui s'est passé, et son run porte les journaux.
func (db *DB) ListReleaseJobs(ctx context.Context, releaseID int64) ([]ReleaseJob, error) {
	rows, err := db.pool.Query(ctx,
		`select id, kind, state, coalesce(environment_id, ''), coalesce(run_name, ''),
		        requested_by, requested_at, finished_at
		 from jobs where release_id = $1 order by requested_at desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("jobs de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var jobs []ReleaseJob
	for rows.Next() {
		var job ReleaseJob
		if err := rows.Scan(&job.ID, &job.Kind, &job.State, &job.EnvironmentID,
			&job.RunName, &job.RequestedBy, &job.RequestedAt, &job.FinishedAt); err != nil {
			return nil, fmt.Errorf("jobs de la release %d: %w", releaseID, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// EnvironmentHistory est une entrée de l'historique d'un environnement.
type EnvironmentHistory struct {
	ReleaseTag   string
	ImageVersion string
	Tag          string
	PushedAt     time.Time
	JobID        *int64
	JobState     string
	RequestedBy  string
}

// ListEnvironmentHistory rend la suite des promotions d'un environnement.
//
// La source est ce que le dashboard a demandé, et non l'historique des commits de
// `mse-kustom` que Flux écrit. Les deux diffèrent : une promotion refusée par une
// ImagePolicy n'apparaît nulle part côté Git, alors qu'elle a bien été demandée —
// et c'est précisément ce cas qu'il faut pouvoir constater.
func (db *DB) ListEnvironmentHistory(ctx context.Context, environmentID string, limit int) ([]EnvironmentHistory, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := db.pool.Query(ctx,
		`select r.tag, r.image_version, p.tag, p.pushed_at, p.job_id,
		        coalesce(j.state, ''), coalesce(j.requested_by, '')
		 from image_promotions p
		 join releases r on r.id = p.release_id
		 left join jobs j on j.id = p.job_id
		 where p.environment_id = $1
		 order by p.pushed_at desc
		 limit $2`, environmentID, limit)
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
