package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
)

/*
 * Les faits constatés.
 *
 * Deux opérations, et la première porte tout le mécanisme : enregistrer sans
 * jamais écraser. Une redélivrance — resynchronisation de l'informer, redémarrage
 * du worker — repasse par le même chemin avec la même clé, et n'écrit rien.
 */

// RecordEvent enregistre un fait, ou constate qu'il est déjà connu.
//
// Rend `false` quand le fait existait déjà. Ce n'est **pas une erreur** : c'est
// le cas nominal d'une redélivrance, et le traiter comme un échec ferait
// journaliser une panne à chaque redémarrage du worker.
//
// La distinction sert aussi au diagnostic : « émis » et « déjà connu » ne se
// lisent pas pareil quand on cherche pourquoi une réaction ne s'est pas
// déclenchée.
func (db *DB) RecordEvent(ctx context.Context, fact event.Event) (bool, error) {
	if err := fact.Validate(); err != nil {
		return false, err
	}

	const query = `
		insert into events (type, source, project_id, subject, payload,
		                    occurred_at, depth, dedup_key)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (dedup_key) do nothing
		returning id`

	var id int64
	err := db.pool.QueryRow(ctx, query,
		string(fact.Type), fact.Source, fact.ProjectID, fact.Subject,
		orEmptyPayload(fact.Payload), fact.OccurredAt, fact.Depth, fact.DedupKey,
	).Scan(&id)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, pgx.ErrNoRows):
		// `do nothing` ne rend aucune ligne : le fait était déjà là.
		return false, nil
	default:
		return false, fmt.Errorf("enregistrement du fait %q: %w", fact.Type, err)
	}
}

// RecordedEvent est un fait relu, avec ce que la base y a ajouté.
type RecordedEvent struct {
	ID   int64
	Type string
	// Source distingue le noyau d'un plugin. Vide pour le noyau.
	Source     string
	ProjectID  *int64
	Subject    string
	Payload    map[string]string
	OccurredAt time.Time
	Depth      int
	// RecordedAt est l'instant de l'écriture, distinct d'`OccurredAt`.
	//
	// L'écart entre les deux est ce qui dira, le jour où des sondes rattraperont
	// de l'historique, qu'un fait ancien vient d'être découvert.
	RecordedAt time.Time
	DedupKey   string
}

// ListEvents rend le fil d'un projet, du plus récent au plus ancien.
//
// Par projet et non global : c'est le fil qu'on vient lire pour répondre à
// « pourquoi rien ne s'est passé sur cette release ? », et mélanger les projets
// obligerait à filtrer à l'œil.
func (db *DB) ListEvents(ctx context.Context, projectID int64, limit int) ([]RecordedEvent, error) {
	if limit <= 0 || limit > maxEventPage {
		limit = maxEventPage
	}

	const query = `
		select id, type, source, project_id, subject, payload,
		       occurred_at, depth, recorded_at, dedup_key
		from events
		where project_id = $1
		order by occurred_at desc, id desc
		limit $2`

	rows, err := db.pool.Query(ctx, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("faits du projet %d: %w", projectID, err)
	}
	defer rows.Close()

	events := make([]RecordedEvent, 0, limit)
	for rows.Next() {
		var entry RecordedEvent
		if err := rows.Scan(&entry.ID, &entry.Type, &entry.Source, &entry.ProjectID,
			&entry.Subject, &entry.Payload, &entry.OccurredAt, &entry.Depth,
			&entry.RecordedAt, &entry.DedupKey); err != nil {
			return nil, fmt.Errorf("lecture d'un fait: %w", err)
		}
		events = append(events, entry)
	}
	return events, rows.Err()
}

// maxEventPage borne la lecture.
//
// Un fil se parcourt, il ne se télécharge pas : personne ne lit deux cents faits
// d'affilée, et la borne évite qu'un projet ancien ne rende une page que le
// navigateur met une seconde à peindre.
const maxEventPage = 200

func orEmptyPayload(payload map[string]string) map[string]string {
	if payload == nil {
		return map[string]string{}
	}
	return payload
}
