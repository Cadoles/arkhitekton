package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Le fil des faits d'un projet.
 *
 * En lecture seule, et c'est tout ce que cette tranche livre. Personne ne réagit
 * encore ; ce que l'écran apporte est un moyen de répondre à la question qui
 * viendra ensuite — « pourquoi rien ne s'est passé ? » —, et elle a deux réponses
 * qui se corrigent différemment : ou bien le fait n'a pas été émis, ou bien
 * aucune réaction ne l'appariait.
 *
 * Sans le fil, les deux sont indiscernables. C'est la raison d'être de l'écran,
 * bien avant l'agrément de voir passer des lignes.
 */

// EventStore is what the feed needs from the database.
type EventStore interface {
	ListEvents(ctx context.Context, projectID int64, limit int) ([]store.RecordedEvent, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// EventsHandler serves the fact feed.
type EventsHandler struct{ store EventStore }

func NewEventsHandler(events EventStore) *EventsHandler { return &EventsHandler{store: events} }

type eventPayload struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	// Source distinguishes the core from a plugin. Empty for the core.
	Source  string `json:"source,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Payload is returned as it is. The screen does not know its keys — they depend on the
	// type, and the day a plugin declares others, a list hard-coded here would leave them
	// invisible.
	Payload    map[string]string `json:"payload"`
	OccurredAt time.Time         `json:"occurred_at"`
	// RecordedAt is returned only when it differs from the fact's instant.
	//
	// The two coincide for everything the core observes; they will diverge when probes catch up
	// on history, and that is precisely where the gap deserves showing. Always displaying it
	// would put two identical dates on every row, which nobody would read any more.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// Depth is the chaining depth. Returned from now on, and always zero: it is what will say,
	// once reactions exist, that a fact was caused by another.
	Depth int `json:"depth"`
}

// List returns a project's feed, newest first.
func (h *EventsHandler) List(w http.ResponseWriter, r *http.Request) {
	project, err := h.store.ProjectBySlug(r.Context(),
		strings.TrimSpace(r.PathValue("project")))
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown project", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		// An unreadable bound is ignored rather than refused: it is a display convenience, and a
		// 400 on a decorative parameter would fail a screen over a mistyped URL.
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	events, err := h.store.ListEvents(r.Context(), project.ID, limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the facts", "")
		return
	}

	payload := make([]eventPayload, 0, len(events))
	for _, entry := range events {
		view := eventPayload{
			ID: entry.ID, Type: entry.Type, Source: entry.Source,
			Subject: entry.Subject, Payload: orEmptyMap(entry.Payload),
			OccurredAt: entry.OccurredAt, Depth: entry.Depth,
		}
		// To the second: both instants are written in the same transaction for a core fact, and a
		// difference of microseconds would display a gap that is not one.
		if entry.RecordedAt.Sub(entry.OccurredAt).Abs() >= time.Second {
			recorded := entry.RecordedAt
			view.RecordedAt = &recorded
		}
		payload = append(payload, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events":  payload,
		"project": project.Slug,
	})
}

func orEmptyMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}
