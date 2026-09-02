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

// ProjectPipeline est la décision d'un projet sur une famille du catalogue.
type ProjectPipeline struct {
	Kind      string
	Enabled   bool
	UpdatedAt time.Time
	UpdatedBy string
}

// ProjectPipelines rend les décisions explicites d'un projet, par famille.
//
// N'y figurent que les familles sur lesquelles le projet s'est prononcé.
// L'absence d'une entrée vaut **activé** : la traiter comme un refus arrêterait
// d'un coup les vérifications de tous les projets déclarés avant cette table.
func (db *DB) ProjectPipelines(ctx context.Context, projectID int64) (map[string]ProjectPipeline, error) {
	const query = `
		select kind, enabled, updated_at, updated_by
		from project_pipelines
		where project_id = $1`

	rows, err := db.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("pipelines du projet %d: %w", projectID, err)
	}
	defer rows.Close()

	decisions := map[string]ProjectPipeline{}
	for rows.Next() {
		var entry ProjectPipeline
		if err := rows.Scan(&entry.Kind, &entry.Enabled, &entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("pipeline d'un projet: %w", err)
		}
		decisions[entry.Kind] = entry
	}
	return decisions, rows.Err()
}

// SetProjectPipeline enregistre la décision d'un projet sur une famille.
func (db *DB) SetProjectPipeline(ctx context.Context, projectID int64, kind string, enabled bool, actor string) error {
	const query = `
		insert into project_pipelines (project_id, kind, enabled, updated_at, updated_by)
		values ($1, $2, $3, now(), $4)
		on conflict (project_id, kind) do update
		set enabled = excluded.enabled, updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by`

	if _, err := db.pool.Exec(ctx, query, projectID, kind, enabled, actor); err != nil {
		return fmt.Errorf("pipeline %q du projet %d: %w", kind, projectID, err)
	}
	return nil
}

// PipelineEnabled dit si une famille s'applique à un projet.
//
// La question est posée en base plutôt que déduite d'une carte chargée une fois :
// l'ordonnanceur tourne en continu, et une famille désactivée dans l'interface
// doit cesser de s'enchaîner à la construction suivante, pas au redémarrage
// suivant.
//
// L'absence de ligne rend vrai. C'est le comportement d'avant cette table, et
// c'est le bon : un projet qui n'a rien dit reçoit ce que le catalogue propose.
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
		return false, fmt.Errorf("pipeline %q du projet %d: %w", kind, projectID, err)
	}
}
