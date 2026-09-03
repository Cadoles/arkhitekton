package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
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

// EventStore est ce que le fil attend de la base.
type EventStore interface {
	ListEvents(ctx context.Context, projectID int64, limit int) ([]store.RecordedEvent, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// EventsHandler sert le fil des faits.
type EventsHandler struct{ store EventStore }

func NewEventsHandler(events EventStore) *EventsHandler { return &EventsHandler{store: events} }

type eventPayload struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	// Source distingue le noyau d'un plugin. Vide pour le noyau.
	Source  string `json:"source,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Payload est rendue telle quelle. L'écran n'en connaît pas les clés — elles
	// dépendent du type, et le jour où un plugin en déclarera d'autres, une liste
	// codée ici les laisserait invisibles.
	Payload    map[string]string `json:"payload"`
	OccurredAt time.Time         `json:"occurred_at"`
	// RecordedAt n'est rendu que lorsqu'il **diffère** de l'instant du fait.
	//
	// Les deux coïncident pour tout ce que le noyau constate ; ils divergeront
	// quand des sondes rattraperont de l'historique, et c'est précisément là que
	// l'écart mérite d'être montré. L'afficher toujours ferait deux dates
	// identiques sur chaque ligne, que personne ne lirait plus.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// Depth est la profondeur d'enchaînement. Rendue dès maintenant, et toujours
	// nulle : c'est elle qui dira, quand des réactions existeront, qu'un fait a
	// été causé par un autre.
	Depth int `json:"depth"`
}

// List rend le fil d'un projet, du plus récent au plus ancien.
func (h *EventsHandler) List(w http.ResponseWriter, r *http.Request) {
	project, err := h.store.ProjectBySlug(r.Context(),
		strings.TrimSpace(r.PathValue("project")))
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet inconnu", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		// Une borne illisible est ignorée plutôt que refusée : c'est un confort
		// d'affichage, et rendre 400 sur un paramètre décoratif ferait échouer un
		// écran pour une URL recopiée de travers.
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	events, err := h.store.ListEvents(r.Context(), project.ID, limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des faits impossible", "")
		return
	}

	payload := make([]eventPayload, 0, len(events))
	for _, entry := range events {
		view := eventPayload{
			ID: entry.ID, Type: entry.Type, Source: entry.Source,
			Subject: entry.Subject, Payload: orEmptyMap(entry.Payload),
			OccurredAt: entry.OccurredAt, Depth: entry.Depth,
		}
		// La seconde près : les deux instants sont écrits dans la même
		// transaction pour un fait du noyau, et une différence de microsecondes
		// afficherait un écart qui n'en est pas un.
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
