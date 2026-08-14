package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Natures d'artefact. Le bon de livraison est un document contractuel ; les deux
// rapports sont des pièces jointes à ce document.
const (
	ArtifactDeliveryNote   = "delivery-note"
	ArtifactTestReport     = "test-report"
	ArtifactSecurityReport = "security-report"
)

// Artifact est un livrable attaché à une release.
type Artifact struct {
	ID        int64
	ReleaseID int64
	Kind      string
	Format    string
	// Object est la clé MinIO. Elle est calculée par le dashboard avant
	// génération, jamais découverte : c'est ce qui permet de la retrouver sans
	// balayer un préfixe.
	Object      string
	SizeBytes   int64
	GeneratedBy string
	GeneratedAt time.Time
}

// ErrArtifactNotFound signale un artefact absent.
var ErrArtifactNotFound = errors.New("artefact introuvable")

// RecordArtifact enregistre un livrable généré.
//
// Régénérer est légitime : les actions manuelles s'écrivent au fil de la
// livraison, et un bon de livraison produit trop tôt ne porte pas ce qui a été
// fait après. Chaque génération est donc une ligne, et la plus récente fait foi —
// les précédentes restent lisibles, ce qui compte pour un document contractuel
// déjà transmis.
func (db *DB) RecordArtifact(ctx context.Context, artifact Artifact) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into artifacts (release_id, kind, format, object, size_bytes, generated_by)
		 values ($1, $2, $3, $4, $5, $6)
		 returning id`,
		artifact.ReleaseID, artifact.Kind, artifact.Format,
		artifact.Object, artifact.SizeBytes, artifact.GeneratedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enregistrement de l'artefact %q: %w", artifact.Kind, err)
	}
	return id, nil
}

// ListArtifacts rend les livrables d'une release, du plus récent au plus ancien.
func (db *DB) ListArtifacts(ctx context.Context, releaseID int64) ([]Artifact, error) {
	rows, err := db.pool.Query(ctx,
		`select id, release_id, kind, format, object, coalesce(size_bytes, 0),
		        generated_by, generated_at
		 from artifacts where release_id = $1 order by generated_at desc, id desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("livrables de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		var artifact Artifact
		if err := rows.Scan(&artifact.ID, &artifact.ReleaseID, &artifact.Kind, &artifact.Format,
			&artifact.Object, &artifact.SizeBytes, &artifact.GeneratedBy,
			&artifact.GeneratedAt); err != nil {
			return nil, fmt.Errorf("livrables de la release %d: %w", releaseID, err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

// GetArtifact rend un livrable par son identifiant.
//
// La release est rendue avec lui : le téléchargement doit vérifier que
// l'artefact demandé appartient bien à la release du chemin, sans quoi un
// identifiant deviné donnerait accès au livrable d'une autre.
func (db *DB) GetArtifact(ctx context.Context, id int64) (Artifact, error) {
	var artifact Artifact
	err := db.pool.QueryRow(ctx,
		`select id, release_id, kind, format, object, coalesce(size_bytes, 0),
		        generated_by, generated_at
		 from artifacts where id = $1`, id).
		Scan(&artifact.ID, &artifact.ReleaseID, &artifact.Kind, &artifact.Format,
			&artifact.Object, &artifact.SizeBytes, &artifact.GeneratedBy, &artifact.GeneratedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return artifact, ErrArtifactNotFound
		}
		return artifact, fmt.Errorf("lecture de l'artefact %d: %w", id, err)
	}
	return artifact, nil
}

// GetReleaseByID rend une release par son identifiant.
//
// Le générateur d'exports part d'un job, qui porte l'identifiant et non le tag.
func (db *DB) GetReleaseByID(ctx context.Context, id int64) (Release, error) {
	var release Release
	err := db.pool.QueryRow(ctx,
		`select id, version_id, tag, kind, state, coalesce(git_ref, ''), image_version,
		        source_release_id, created_by, created_at, released_at,
		        coalesce(manual_actions_md, ''), coalesce(manual_actions_updated_by, ''),
		        manual_actions_updated_at
		 from releases where id = $1`, id).
		Scan(&release.ID, &release.VersionID, &release.Tag, &release.Kind, &release.State,
			&release.GitRef, &release.ImageVersion, &release.SourceReleaseID,
			&release.CreatedBy, &release.CreatedAt, &release.ReleasedAt,
			&release.ManualActions, &release.ManualActionsUpdatedBy, &release.ManualActionsUpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return release, ErrReleaseNotFound
		}
		return release, fmt.Errorf("lecture de la release %d: %w", id, err)
	}
	return release, nil
}

// VersionName rend le nom de la version Redmine d'une release.
func (db *DB) VersionName(ctx context.Context, versionID int64) (string, error) {
	var name string
	err := db.pool.QueryRow(ctx, `select name from versions where id = $1`, versionID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrVersionNotFound
		}
		return "", fmt.Errorf("nom de la version %d: %w", versionID, err)
	}
	return name, nil
}
