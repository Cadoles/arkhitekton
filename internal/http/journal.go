package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The journal: what happened in the application, for its administrators.
 *
 * Actions (who did what) and events (what happened, whoever caused it) in one feed. An event
 * carries the automations it set off: "why did nothing happen?" has two answers that are fixed
 * differently, the fact was not emitted or nothing reacted to it, and the journal tells them
 * apart.
 */

// JournalStore is what the journal needs from the database.
type JournalStore interface {
	ListJournal(ctx context.Context, query store.JournalQuery) ([]store.JournalEntry, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	DirectoryReader
}

// JournalHandler serves the journal.
type JournalHandler struct {
	store  JournalStore
	logger *slog.Logger
}

func NewJournalHandler(journal JournalStore, logger *slog.Logger) *JournalHandler {
	return &JournalHandler{store: journal, logger: logger}
}

// journalEntry is a line of the journal; the fields of the other nature are left out.
type journalEntry struct {
	Nature  string         `json:"nature"`
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	Project string         `json:"project,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`

	Actor  string `json:"actor,omitempty"`
	Action string `json:"action,omitempty"`
	Target string `json:"target,omitempty"`

	Type string `json:"type,omitempty"`
	// Source distinguishes the core from a plugin. Empty for the core.
	Source  string `json:"source,omitempty"`
	Subject string `json:"subject,omitempty"`
	// RecordedAt is returned only when it differs from the event's instant: an event discovered
	// late.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// Pending is an event received and not routed yet: no automation could have reacted.
	Pending     bool                `json:"pending,omitempty"`
	Automations []journalAutomation `json:"automations,omitempty"`
}

// journalAutomation is a run with its steps, in full: the journal is read by administrators only.
type journalAutomation struct {
	planRunPayload
	// Name is the declared reaction's or schedule's own name.
	Name string `json:"name,omitempty"`
}

// List returns the journal, newest first. `nature` (`action`, `event`, `automation`), `project`
// (a slug), `q` (a text searched in names, targets and subjects) and `limit` narrow it.
func (h *JournalHandler) List(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := store.JournalQuery{Search: values.Get("q")}

	switch nature := values.Get("nature"); nature {
	case "", store.JournalAction, store.JournalEvent, store.JournalRun:
		query.Nature = nature
	default:
		writeProblem(w, http.StatusBadRequest, "Unknown nature", "expected action, event or automation")
		return
	}
	if raw := values.Get("limit"); raw != "" {
		// An unreadable bound is ignored rather than refused: a display convenience.
		if parsed, err := strconv.Atoi(raw); err == nil {
			query.Limit = parsed
		}
	}
	if slug := strings.TrimSpace(values.Get("project")); slug != "" {
		project, err := h.store.ProjectBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, store.ErrProjectNotFound) {
				writeProblem(w, http.StatusNotFound, "Unknown project", slug)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
			return
		}
		query.ProjectID = &project.ID
	}

	entries, err := h.store.ListJournal(r.Context(), query)
	if err != nil {
		h.logger.Error("reading the journal", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot read the journal", "")
		return
	}

	who := resolveActors(r.Context(), h.store, h.logger)
	payload := make([]journalEntry, 0, len(entries))
	for _, entry := range entries {
		view := journalEntry{
			Nature: entry.Nature, ID: entry.ID, At: entry.At, Project: entry.Project,
			Payload: entry.Payload, Target: entry.Target, Action: entry.Action,
			Type: entry.Type, Source: entry.Source, Subject: entry.Subject, Pending: entry.Pending,
		}
		if entry.Nature == store.JournalAction {
			view.Actor = who.name(entry.Actor)
		}
		// To the second: both instants are written in the same transaction for a core event, and
		// a difference of microseconds would display a gap that is not one.
		if entry.Nature == store.JournalEvent && entry.RecordedAt.Sub(entry.At).Abs() >= time.Second {
			recorded := entry.RecordedAt
			view.RecordedAt = &recorded
		}
		for _, run := range entry.Automations {
			view.Automations = append(view.Automations, journalAutomation{
				planRunPayload: toPlanRunPayload(run, true), Name: run.Name,
			})
		}
		payload = append(payload, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": payload})
}

func orEmptyMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}
