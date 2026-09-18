package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Artifact kinds. The delivery note is a contractual document; the two reports are
// attachments to it.
const (
	ArtifactDeliveryNote   = "delivery-note"
	ArtifactTestReport     = "test-report"
	ArtifactSecurityReport = "security-report"
)

// Artifact is a deliverable attached to a release.
type Artifact struct {
	ID        int64
	ReleaseID int64
	Kind      string
	Format    string
	// Object is the store key. It is computed by the dashboard before generation, never
	// discovered: that is what allows finding it again without sweeping a prefix.
	Object      string
	SizeBytes   int64
	GeneratedBy string
	GeneratedAt time.Time
}

// ErrArtifactNotFound signale un artefact absent.
var ErrArtifactNotFound = errors.New("artefact introuvable")

// RecordArtifact records a generated deliverable.
//
// Regenerating is legitimate: manual actions are written as the delivery proceeds, and a
// delivery note produced too early does not carry what was done afterwards. Each
// generation is therefore a row, and the most recent prevails — the previous ones stay
// readable, which matters for a contractual document already sent.
func (db *DB) RecordArtifact(ctx context.Context, artifact Artifact) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`insert into artifacts (release_id, kind, format, object, size_bytes, generated_by)
		 values ($1, $2, $3, $4, $5, $6)
		 returning id`,
		artifact.ReleaseID, artifact.Kind, artifact.Format,
		artifact.Object, artifact.SizeBytes, artifact.GeneratedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("recording artifact %q: %w", artifact.Kind, err)
	}
	return id, nil
}

// ListArtifacts returns a release's deliverables, newest first.
func (db *DB) ListArtifacts(ctx context.Context, releaseID int64) ([]Artifact, error) {
	rows, err := db.pool.Query(ctx,
		`select id, release_id, kind, format, object, coalesce(size_bytes, 0),
		        generated_by, generated_at
		 from artifacts where release_id = $1 order by generated_at desc, id desc`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("deliverables of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		var artifact Artifact
		if err := rows.Scan(&artifact.ID, &artifact.ReleaseID, &artifact.Kind, &artifact.Format,
			&artifact.Object, &artifact.SizeBytes, &artifact.GeneratedBy,
			&artifact.GeneratedAt); err != nil {
			return nil, fmt.Errorf("deliverables of release %d: %w", releaseID, err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

// GetArtifact returns a deliverable by its identifier.
//
// The release comes with it: the download must check that the artifact asked for really
// belongs to the release in the path, otherwise a guessed identifier would give access to
// another's deliverable.
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
		return artifact, fmt.Errorf("reading artifact %d: %w", id, err)
	}
	return artifact, nil
}

// GetReleaseByID returns a release by its identifier.
//
// The export generator starts from a job, which carries the identifier and not the tag.
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
		return release, fmt.Errorf("reading release %d: %w", id, err)
	}
	return release, nil
}

// VersionName returns the tracker version's name for a release.
func (db *DB) VersionName(ctx context.Context, versionID int64) (string, error) {
	var name string
	err := db.pool.QueryRow(ctx, `select name from versions where id = $1`, versionID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrVersionNotFound
		}
		return "", fmt.Errorf("name of version %d: %w", versionID, err)
	}
	return name, nil
}
