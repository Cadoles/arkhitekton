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
 * well as a release created. An event carries the automation runs it set off, steps included,
 * which is what answers "why did nothing happen?" and "what did my reaction do?".
 *
 * A run no event opened, a schedule's tick, is a third kind of line: without it, what a schedule
 * did would be read nowhere.
 */

// The natures of a journal line, and of the query.
const (
	JournalAction = "action"
	JournalEvent  = "event"
	// JournalRun is a line for a run no event opened. As a query, it selects what automations
	// did: those runs, and the events that set off at least one.
	JournalRun = "automation"
)

// JournalQuery narrows the journal. Zero values select everything.
type JournalQuery struct {
	// Nature is [JournalAction], [JournalEvent], [JournalRun] or empty for all.
	Nature    string
	ProjectID *int64
	// Search matches, case-insensitively, an action's name and target, an event's type and
	// subject, a run's trigger and name.
	Search string
	Limit  int
}

// JournalEntry is a line of the journal: an action, an event or a run, the others' fields empty.
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
	// Agent is the AI agent that acted on the actor's behalf, empty when the actor acted.
	Agent string

	// An event's.
	Type    string
	Source  string
	Subject string
	// RecordedAt is when the event was written, which an event discovered late sets apart from
	// At.
	RecordedAt time.Time
	// Pending is an event received and not routed to the automations yet.
	Pending  bool
	DedupKey string

	// Automations are the runs an event set off, or a run line's own.
	Automations []PlanRunSummary
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

	nature := query.Nature
	var entries []JournalEntry
	if nature == "" || nature == JournalAction {
		actions, err := db.journalActions(ctx, query.ProjectID, search, limit)
		if err != nil {
			return nil, err
		}
		entries = append(entries, actions...)
	}
	if nature == "" || nature == JournalEvent || nature == JournalRun {
		events, err := db.journalEvents(ctx, query.ProjectID, search, nature == JournalRun, limit)
		if err != nil {
			return nil, err
		}
		entries = append(entries, events...)
	}
	if nature == "" || nature == JournalRun {
		runs, err := db.journalRuns(ctx, query.ProjectID, search, limit)
		if err != nil {
			return nil, err
		}
		entries = append(entries, runs...)
	}

	slices.SortStableFunc(entries, func(a, b JournalEntry) int { return b.At.Compare(a.At) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, db.attachAutomations(ctx, entries)
}

func (db *DB) journalActions(ctx context.Context, projectID *int64, search string, limit int) ([]JournalEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select a.id, a.at, coalesce(p.slug, ''), a.actor, a.action, coalesce(a.target, ''), a.payload,
		        coalesce(a.agent, '')
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
			&entry.Target, &payload, &entry.Agent); err != nil {
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

func (db *DB) journalEvents(ctx context.Context, projectID *int64, search string, withRuns bool, limit int) ([]JournalEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select e.id, e.occurred_at, coalesce(p.slug, ''), e.type, e.source, e.subject, e.payload,
		        e.recorded_at, e.dispatch_pending, e.dedup_key
		 from events e
		 left join projects p on p.id = e.project_id
		 where ($1::bigint is null or e.project_id = $1)
		   and (e.type ilike $2 or e.subject ilike $2)
		   and (not $4 or exists (select 1 from plan_runs r where r.event_key = e.dedup_key))
		 order by e.occurred_at desc, e.id desc
		 limit $3`, projectID, search, limit, withRuns)
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

// journalRuns returns the runs no event opened: a schedule's ticks.
func (db *DB) journalRuns(ctx context.Context, projectID *int64, search string, limit int) ([]JournalEntry, error) {
	runs, err := db.planRunsWhere(ctx,
		`($1::bigint is null or r.project_id = $1)
		 and (r.trigger ilike $2 or coalesce(x.name, s.name, '') ilike $2)
		 and not exists (select 1 from events e where e.dedup_key = r.event_key)`,
		limit, projectID, search)
	if err != nil {
		return nil, fmt.Errorf("journal runs: %w", err)
	}

	slugs, err := db.projectSlugs(ctx, runs)
	if err != nil {
		return nil, err
	}
	entries := make([]JournalEntry, 0, len(runs))
	for _, run := range runs {
		entry := JournalEntry{Nature: JournalRun, ID: run.ID, At: run.CreatedAt,
			Automations: []PlanRunSummary{run}}
		if run.ProjectID != nil {
			entry.Project = slugs[*run.ProjectID]
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// projectSlugs names the projects of some runs.
func (db *DB) projectSlugs(ctx context.Context, runs []PlanRunSummary) (map[int64]string, error) {
	var ids []int64
	for _, run := range runs {
		if run.ProjectID != nil {
			ids = append(ids, *run.ProjectID)
		}
	}
	slugs := map[int64]string{}
	if len(ids) == 0 {
		return slugs, nil
	}
	rows, err := db.pool.Query(ctx, `select id, slug from projects where id = any($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("projects of the journal's runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, fmt.Errorf("projects of the journal's runs: %w", err)
		}
		slugs[id] = slug
	}
	return slugs, rows.Err()
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

	// Every run of the page's events: a page is bounded, and an event opens a handful of runs.
	runs, err := db.planRunsWhere(ctx, "r.event_key = any($1)", maxJournalPage*4, keys)
	if err != nil {
		return fmt.Errorf("automations of the journal's events: %w", err)
	}
	byKey := map[string][]PlanRunSummary{}
	// Oldest first under an event: the order they were set off in.
	for index := len(runs) - 1; index >= 0; index-- {
		byKey[runs[index].EventKey] = append(byKey[runs[index].EventKey], runs[index])
	}
	for index := range entries {
		if entries[index].Nature == JournalEvent {
			entries[index].Automations = byKey[entries[index].DedupKey]
		}
	}
	return nil
}

// escapeLike makes a search text match itself in a `like` pattern.
func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}
