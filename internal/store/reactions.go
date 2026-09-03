package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/reaction"
)

/*
 * Les réactions déclarées.
 *
 * Ce magasin ne valide rien d'autre que ce que la base sait vérifier :
 * `internal/reaction` porte la forme, et la couche HTTP porte les références —
 * qu'un type de fait existe, qu'un geste désigne une famille du catalogue ou une
 * action du registre.
 */

// ErrReactionNotFound signale une réaction absente.
var ErrReactionNotFound = errors.New("réaction introuvable")

// ErrReactionExists signale un nom déjà porté dans ce projet.
//
// Distingué du reste : « ce nom est pris » se corrige en une frappe, alors qu'une
// erreur de base générique envoie chercher une panne.
var ErrReactionExists = errors.New("une réaction porte déjà ce nom dans ce projet")

// ListReactions rend les réactions d'un projet, par nom.
//
// Toutes, actives ou non : un écran qui cacherait les réactions éteintes ferait
// croire qu'elles ont été supprimées, et on les réécrirait.
func (db *DB) ListReactions(ctx context.Context, projectID int64) ([]reaction.Reaction, error) {
	const query = `
		select id, project_id, name, event_type, enabled, match, plan
		from reactions where project_id = $1
		order by lower(btrim(name))`

	rows, err := db.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("réactions du projet %d: %w", projectID, err)
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

// ReactionsFor rend les réactions **actives** d'un projet qui écoutent un type de
// fait.
//
// La question que pose chaque fait constaté. Bornée aux actives : une réaction
// éteinte reste lisible dans l'écran, elle ne se déclenche simplement plus.
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
		return nil, fmt.Errorf("réactions sur %q: %w", eventType, err)
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

// GetReaction rend une réaction d'un projet.
//
// Le projet fait partie de la question : sans lui, l'identifiant seul permettrait
// de lire — et donc de modifier — la réaction d'un autre projet depuis une URL
// composée à la main.
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

// scannable couvre `pgx.Row` et `pgx.Rows`, dont seule la méthode `Scan` sert ici.
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
			"plan de la réaction %d illisible: %w", entry.ID, err)
	}
	return entry, nil
}

// SaveReaction crée une réaction, ou remplace celle qui porte cet identifiant.
//
// Rend l'identifiant écrit. Un identifiant nul crée ; un identifiant renseigné
// remplace, et le projet borne la mise à jour — une réaction d'un autre projet
// n'est pas atteignable par une URL composée à la main.
func (db *DB) SaveReaction(
	ctx context.Context, entry reaction.Reaction, actor string,
) (int64, error) {
	encoded, err := json.Marshal(entry.Plan)
	if err != nil {
		return 0, fmt.Errorf("plan illisible: %w", err)
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

// DeleteReaction retire une réaction d'un projet.
func (db *DB) DeleteReaction(ctx context.Context, projectID, id int64) error {
	tag, err := db.pool.Exec(ctx,
		`delete from reactions where project_id = $1 and id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("suppression de la réaction %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrReactionNotFound
	}
	return nil
}

// wrapReactionWrite distingue le nom déjà pris du reste.
//
// « Ce nom est pris » se corrige en une frappe ; une erreur générique envoie
// chercher une panne. La distinction se fait sur le code de contrainte plutôt que
// sur le message, qui dépend de la locale du serveur.
func wrapReactionWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrReactionExists
	}
	return fmt.Errorf("enregistrement d'une réaction: %w", err)
}
