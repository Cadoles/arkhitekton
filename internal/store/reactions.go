package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
)

/*
 * The declared reactions.
 *
 * This store validates nothing other than what the database can check: `internal/reaction` carries
 * the shape, and the HTTP layer carries the references — that a fact type exists, that a step
 * names a catalogue kind or a registry action.
 */

// ErrReactionNotFound reports a missing reaction.
var ErrReactionNotFound = errors.New("reaction not found")

// ErrReactionExists reports a name already taken within this project.
//
// Told apart from the rest: "this name is taken" is fixed with a keystroke, while a generic
// database error sends someone hunting for a fault.
var ErrReactionExists = errors.New("a reaction already carries this name in this project")

// ListReactions returns a project's reactions, by name.
//
// All of them, active or not: a screen hiding switched-off reactions would suggest they had
// been deleted, and they would be rewritten.
func (db *DB) ListReactions(ctx context.Context, projectID int64) ([]reaction.Reaction, error) {
	const query = `
		select id, project_id, name, event_type, enabled, match, plan
		from reactions where project_id = $1
		order by lower(btrim(name))`

	rows, err := db.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("reactions of project %d: %w", projectID, err)
	}
	defer rows.Close()

	var reactions []reaction.Reaction
	for rows.Next() {
		entry, err := scanReaction(rows)
		if err != nil {
			return nil, err
		}
		reactions = append(reactions, entry)
	}
	return reactions, rows.Err()
}

// ReactionsFor returns a project's active reactions that listen for a fact type.
//
// The question every observed fact asks. Bounded to the active ones: a switched-off reaction
// stays readable in the screen, it simply no longer fires.
func (db *DB) ReactionsFor(
	ctx context.Context, projectID int64, eventType string,
) ([]reaction.Reaction, error) {
	const query = `
		select id, project_id, name, event_type, enabled, match, plan
		from reactions
		where project_id = $1 and event_type = $2 and enabled
		order by id`

	rows, err := db.pool.Query(ctx, query, projectID, eventType)
	if err != nil {
		return nil, fmt.Errorf("reactions on %q: %w", eventType, err)
	}
	defer rows.Close()

	var reactions []reaction.Reaction
	for rows.Next() {
		entry, err := scanReaction(rows)
		if err != nil {
			return nil, err
		}
		reactions = append(reactions, entry)
	}
	return reactions, rows.Err()
}

// GetReaction returns a project's reaction.
//
// The project is part of the question: without it, the identifier alone would allow reading
// — and therefore modifying — another project's reaction from a hand-made URL.
func (db *DB) GetReaction(ctx context.Context, projectID, id int64) (reaction.Reaction, error) {
	const query = `
		select id, project_id, name, event_type, enabled, match, plan
		from reactions where project_id = $1 and id = $2`

	entry, err := scanReaction(db.pool.QueryRow(ctx, query, projectID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reaction.Reaction{}, ErrReactionNotFound
		}
		return reaction.Reaction{}, err
	}
	return entry, nil
}

// scannable covers `pgx.Row` and `pgx.Rows`, of which only the Scan method is used here.
type scannable interface{ Scan(dest ...any) error }

func scanReaction(row scannable) (reaction.Reaction, error) {
	var (
		entry   reaction.Reaction
		encoded []byte
	)
	if err := row.Scan(&entry.ID, &entry.ProjectID, &entry.Name, &entry.EventType,
		&entry.Enabled, &entry.Match, &encoded); err != nil {
		return reaction.Reaction{}, err
	}
	if err := json.Unmarshal(encoded, &entry.Plan); err != nil {
		return reaction.Reaction{}, fmt.Errorf(
			"plan of reaction %d unreadable: %w", entry.ID, err)
	}
	return entry, nil
}

// SaveReaction creates a reaction, or replaces the one carrying this identifier.
//
// Returns the identifier written. A nil identifier creates; a set identifier replaces, and
// the project bounds the update — another project's reaction is not reachable from a
// hand-made URL.
func (db *DB) SaveReaction(
	ctx context.Context, entry reaction.Reaction, actor string,
) (int64, error) {
	encoded, err := json.Marshal(entry.Plan)
	if err != nil {
		return 0, fmt.Errorf("unreadable plan: %w", err)
	}
	match := entry.Match
	if match == nil {
		match = map[string]string{}
	}

	if entry.ID == 0 {
		const insert = `
			insert into reactions (project_id, name, event_type, enabled, match, plan, created_by)
			values ($1, $2, $3, $4, $5, $6, $7)
			returning id`

		var id int64
		err := db.pool.QueryRow(ctx, insert, entry.ProjectID, entry.Name,
			entry.EventType, entry.Enabled, match, encoded, actor).Scan(&id)
		if err != nil {
			return 0, wrapReactionWrite(err)
		}
		return id, nil
	}

	const update = `
		update reactions
		set name = $3, event_type = $4, enabled = $5, match = $6, plan = $7,
		    updated_at = now()
		where project_id = $1 and id = $2`

	tag, err := db.pool.Exec(ctx, update, entry.ProjectID, entry.ID, entry.Name,
		entry.EventType, entry.Enabled, match, encoded)
	if err != nil {
		return 0, wrapReactionWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrReactionNotFound
	}
	return entry.ID, nil
}

// DeleteReaction removes a project's reaction.
func (db *DB) DeleteReaction(ctx context.Context, projectID, id int64) error {
	tag, err := db.pool.Exec(ctx,
		`delete from reactions where project_id = $1 and id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("deleting reaction %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrReactionNotFound
	}
	return nil
}

// wrapReactionWrite tells a taken name from the rest.
//
// "This name is taken" is fixed with a keystroke; a generic error sends someone hunting for
// a fault. The distinction is made on the constraint code rather than on the message, which
// depends on the server's locale.
func wrapReactionWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrReactionExists
	}
	return fmt.Errorf("recording a reaction: %w", err)
}
