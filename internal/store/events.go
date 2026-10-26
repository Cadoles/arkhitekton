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
 * The observed facts.
 *
 * Two operations, and the first carries the whole mechanism: recording without ever overwriting.
 * A redelivery — informer resynchronisation, worker restart — goes through the same path with the
 * same key, and writes nothing.
 */

// RecordEvent records a fact, or observes that it is already known.
//
// Returns false when the fact already existed. That is not an error: it is the nominal case
// of a redelivery, and treating it as a failure would log a fault at every worker restart.
//
// The distinction also serves diagnosis: "emitted" and "already known" do not read the same
// when looking for why a reaction did not fire.
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
		// `do nothing` returns no row: the fact was already there.
		return false, nil
	default:
		return false, fmt.Errorf("recording fact %q: %w", fact.Type, err)
	}
}

// RecordedEvent is a fact read back, with what the database added to it.
type RecordedEvent struct {
	ID   int64
	Type string
	// Source distinguishes the core from a plugin. Empty for the core.
	Source     string
	ProjectID  *int64
	Subject    string
	Payload    map[string]string
	OccurredAt time.Time
	Depth      int
	// RecordedAt is the instant of the write, distinct from OccurredAt.
	//
	// The gap between the two is what will say, the day probes catch up on history, that an old
	// fact has just been discovered.
	RecordedAt time.Time
	DedupKey   string
}

// ListEvents returns a project's feed, newest first.
//
// Per project and not global: it is the feed one reads to answer "why did nothing happen on
// this release?", and mixing projects would force filtering by eye.
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
		return nil, fmt.Errorf("facts of project %d: %w", projectID, err)
	}
	defer rows.Close()

	events := make([]RecordedEvent, 0, limit)
	for rows.Next() {
		var entry RecordedEvent
		if err := rows.Scan(&entry.ID, &entry.Type, &entry.Source, &entry.ProjectID,
			&entry.Subject, &entry.Payload, &entry.OccurredAt, &entry.Depth,
			&entry.RecordedAt, &entry.DedupKey); err != nil {
			return nil, fmt.Errorf("reading a fact: %w", err)
		}
		events = append(events, entry)
	}
	return events, rows.Err()
}

// maxEventPage bounds the read.
//
// A feed is browsed, not downloaded: nobody reads two hundred facts in a row, and the bound
// keeps an old project from returning a page the browser takes a second to paint.
const maxEventPage = 200

func orEmptyPayload(payload map[string]string) map[string]string {
	if payload == nil {
		return map[string]string{}
	}
	return payload
}
