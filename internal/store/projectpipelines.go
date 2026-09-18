package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

/*
 * Quels pipelines du registre s'appliquent à un projet.
 *
 * Le catalogue dit ce que l'instance sait lancer. Cette table dit ce que chaque
 * projet **veut** lancer — une question qui lui appartient, et que le catalogue ne
 * savait trancher que pour tout le monde à la fois.
 */

// ProjectPipeline is a project's decision about a catalogue kind.
type ProjectPipeline struct {
	Kind      string
	Enabled   bool
	UpdatedAt time.Time
	UpdatedBy string
}

// ProjectPipelines returns a project's explicit decisions, by kind.
//
// Only the kinds the project has pronounced on appear. The absence of an entry means
// enabled: treating it as refusal would stop at a stroke the verifications of every project
// declared before this table.
func (db *DB) ProjectPipelines(ctx context.Context, projectID int64) (map[string]ProjectPipeline, error) {
	const query = `
		select kind, enabled, updated_at, updated_by
		from project_pipelines
		where project_id = $1`

	rows, err := db.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("pipelines of project %d: %w", projectID, err)
	}
	defer rows.Close()

	decisions := map[string]ProjectPipeline{}
	for rows.Next() {
		var entry ProjectPipeline
		if err := rows.Scan(&entry.Kind, &entry.Enabled, &entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("pipeline of a project: %w", err)
		}
		decisions[entry.Kind] = entry
	}
	return decisions, rows.Err()
}

// SetProjectPipeline records a project's decision about a kind.
func (db *DB) SetProjectPipeline(ctx context.Context, projectID int64, kind string, enabled bool, actor string) error {
	const query = `
		insert into project_pipelines (project_id, kind, enabled, updated_at, updated_by)
		values ($1, $2, $3, now(), $4)
		on conflict (project_id, kind) do update
		set enabled = excluded.enabled, updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by`

	if _, err := db.pool.Exec(ctx, query, projectID, kind, enabled, actor); err != nil {
		return fmt.Errorf("pipeline %q of project %d: %w", kind, projectID, err)
	}
	return nil
}

// PipelineEnabled says whether a kind applies to a project.
//
// The question is asked in the database rather than derived from a map loaded once: the
// scheduler runs continuously, and a kind disabled in the interface must stop being chained
// at the next build, not at the next restart.
//
// The absence of a row returns true. That is the behaviour before this table, and it is the
// right one: a project that said nothing receives what the catalogue offers.
func (db *DB) PipelineEnabled(ctx context.Context, projectID int64, kind string) (bool, error) {
	const query = `
		select enabled from project_pipelines where project_id = $1 and kind = $2`

	var enabled bool
	switch err := db.pool.QueryRow(ctx, query, projectID, kind).Scan(&enabled); {
	case err == nil:
		return enabled, nil
	case errors.Is(err, pgx.ErrNoRows):
		return true, nil
	default:
		return false, fmt.Errorf("pipeline %q of project %d: %w", kind, projectID, err)
	}
}
