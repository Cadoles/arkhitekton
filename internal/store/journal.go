package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

/*
 * The journal: what happened in the application, read in one place.
 *
 * Two kinds of lines, and their difference is what the journal shows: an action is someone's
 * doing (`audit_log`), an event is what happened (`events`), whoever caused it, a forge's push as
 * well as a release created. An event says which automations it set off, which is what answers
 * "why did nothing happen?".
 */

// The two natures of a journal line.
const (
	JournalAction = "action"
	JournalEvent  = "event"
)

// JournalQuery narrows the journal. Zero values select everything.
type JournalQuery struct {
	// Nature is [JournalAction], [JournalEvent] or empty for both.
	Nature    string
	ProjectID *int64
	// Search matches, case-insensitively, an action's name and target, an event's type and
	// subject.
	Search string
	Limit  int
}

// JournalEntry is a line of the journal: an action or an event, the fields of the other empty.
type JournalEntry struct {
	Nature  string
	ID      int64
	At      time.Time
	Project string
	Payload map[string]any

	// An action's.
	Actor  string
	Action string
	Target string

	// An event's.
	Type    string
	Source  string
	Subject string
	// RecordedAt is when the event was written, which an event discovered late sets apart from
	// At.
	RecordedAt time.Time
	// Pending is an event received and not routed to the automations yet.
	Pending     bool
	DedupKey    string
	Automations []JournalAutomation
}

// JournalAutomation is a run of an automation an event set off.
type JournalAutomation struct {
	Trigger string
	// Name is a declared reaction's own name, empty for a built-in trigger.
	Name  string
	State string
}

// maxJournalPage bounds a read: a journal is browsed, not exported.
const maxJournalPage = 500

// ListJournal returns the journal, newest first: the actions and the events the query selects,
// merged by date, each event with the automations it set off.
func (db *DB) ListJournal(ctx context.Context, query JournalQuery) ([]JournalEntry, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, maxJournalPage)
	search := "%" + escapeLike(strings.TrimSpace(query.Search)) + "%"

	var entries []JournalEntry
	if query.Nature != JournalEvent {
		actions, err := db.journalActions(ctx, query.ProjectID, search, limit)
		if err != nil {
			return nil, err
		}
		entries = append(entries, actions...)
	}
	if query.Nature != JournalAction {
		events, err := db.journalEvents(ctx, query.ProjectID, search, limit)
		if err != nil {
			return nil, err
		}
		entries = append(entries, events...)
	}

	slices.SortStableFunc(entries, func(a, b JournalEntry) int { return b.At.Compare(a.At) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, db.attachAutomations(ctx, entries)
}

func (db *DB) journalActions(ctx context.Context, projectID *int64, search string, limit int) ([]JournalEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select a.id, a.at, coalesce(p.slug, ''), a.actor, a.action, coalesce(a.target, ''), a.payload
		 from audit_log a
		 left join projects p on p.id = a.project_id
		 where ($1::bigint is null or a.project_id = $1)
		   and (a.action ilike $2 or coalesce(a.target, '') ilike $2)
		 order by a.at desc, a.id desc
		 limit $3`, projectID, search, limit)
	if err != nil {
		return nil, fmt.Errorf("journal actions: %w", err)
	}
	defer rows.Close()

	var entries []JournalEntry
	for rows.Next() {
		entry := JournalEntry{Nature: JournalAction}
		var payload []byte
		if err := rows.Scan(&entry.ID, &entry.At, &entry.Project, &entry.Actor, &entry.Action,
			&entry.Target, &payload); err != nil {
			return nil, fmt.Errorf("journal actions: %w", err)
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &entry.Payload); err != nil {
				return nil, fmt.Errorf("journal actions: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (db *DB) journalEvents(ctx context.Context, projectID *int64, search string, limit int) ([]JournalEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select e.id, e.occurred_at, coalesce(p.slug, ''), e.type, e.source, e.subject, e.payload,
		        e.recorded_at, e.dispatch_pending, e.dedup_key
		 from events e
		 left join projects p on p.id = e.project_id
		 where ($1::bigint is null or e.project_id = $1)
		   and (e.type ilike $2 or e.subject ilike $2)
		 order by e.occurred_at desc, e.id desc
		 limit $3`, projectID, search, limit)
	if err != nil {
		return nil, fmt.Errorf("journal events: %w", err)
	}
	defer rows.Close()

	var entries []JournalEntry
	for rows.Next() {
		entry := JournalEntry{Nature: JournalEvent}
		var payload map[string]string
		if err := rows.Scan(&entry.ID, &entry.At, &entry.Project, &entry.Type, &entry.Source,
			&entry.Subject, &payload, &entry.RecordedAt, &entry.Pending, &entry.DedupKey); err != nil {
			return nil, fmt.Errorf("journal events: %w", err)
		}
		entry.Payload = make(map[string]any, len(payload))
		for key, value := range payload {
			entry.Payload[key] = value
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// attachAutomations sets, on each event, the automation runs its fact opened: a run names the
// fact that triggered it by its key.
func (db *DB) attachAutomations(ctx context.Context, entries []JournalEntry) error {
	var keys []string
	for _, entry := range entries {
		if entry.Nature == JournalEvent && entry.DedupKey != "" {
			keys = append(keys, entry.DedupKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	rows, err := db.pool.Query(ctx,
		`select r.event_key, r.trigger, coalesce(x.name, ''), r.state
		 from plan_runs r
		 left join reactions x on r.trigger = 'reaction:' || x.id
		 where r.event_key = any($1)
		 order by r.id`, keys)
	if err != nil {
		return fmt.Errorf("automations of the journal's events: %w", err)
	}
	defer rows.Close()

	byKey := map[string][]JournalAutomation{}
	for rows.Next() {
		var key string
		var automation JournalAutomation
		if err := rows.Scan(&key, &automation.Trigger, &automation.Name, &automation.State); err != nil {
			return fmt.Errorf("automations of the journal's events: %w", err)
		}
		byKey[key] = append(byKey[key], automation)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range entries {
		entries[index].Automations = byKey[entries[index].DedupKey]
	}
	return nil
}

// escapeLike makes a search text match itself in a `like` pattern.
func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}
