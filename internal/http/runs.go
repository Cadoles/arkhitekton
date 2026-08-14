package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// Runs décrit ce que le handler attend de la base.
type Runs interface {
	ListRuns(ctx context.Context, limit int) ([]store.RunSummary, error)
}

// RunsHandler expose les runs observés.
type RunsHandler struct {
	runs Runs
}

func NewRunsHandler(runs Runs) *RunsHandler {
	return &RunsHandler{runs: runs}
}

// runPayload est la représentation transmise au frontend.
//
// Un type dédié plutôt que la structure de `store` : le contrat d'API ne doit pas
// suivre les évolutions du schéma, et les noms passent en snake_case, convention
// du reste de l'API.
type runPayload struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Pipeline    string     `json:"pipeline"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Observed distingue un run que le dashboard a lancé d'un run qu'il
	// constate : l'interface n'offre ni annulation ni relance sur le second.
	Observed bool `json:"observed"`
}

// List rend les runs les plus récents.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
		// Une limite illisible est ignorée plutôt que refusée : le défaut est
		// sûr, et un 400 sur un paramètre d'affichage n'apprend rien.
	}

	runs, err := h.runs.ListRuns(r.Context(), limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des exécutions impossible", "")
		return
	}

	payload := make([]runPayload, 0, len(runs))
	for _, run := range runs {
		payload = append(payload, toRunPayload(run))
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": payload})
}

// RunEvents est ce que le flux d'état attend de la base.
type RunEvents interface {
	RunsUpdatedSince(ctx context.Context, since time.Time) ([]store.RunSummary, time.Time, error)
}

// runPollInterval espace deux relectures de la table des runs.
const runPollInterval = 2 * time.Second

// Stream pousse les changements d'état des runs.
//
// Le flux ne transmet que ce qui a changé : renvoyer la liste entière à chaque
// tour ferait retransmettre des centaines de runs figés pour un seul changement,
// et l'interface ne saurait pas lequel a bougé.
//
// L'identifiant de reprise est l'horodatage de la dernière modification vue. Un
// client qui se reconnecte ne redemande donc que la différence — mais le
// mécanisme n'a rien d'une garantie de livraison : ce flux accélère l'affichage,
// il ne le fonde pas. La liste reste la source de vérité, et l'interface s'en
// contente si le flux tombe.
func (h *RunsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	events, ok := h.runs.(RunEvents)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "Flux d'état indisponible", "")
		return
	}

	stream, streaming := newSSEWriter(w)
	if !streaming {
		writeProblem(w, http.StatusInternalServerError, "Flux non supporté", "")
		return
	}

	// Sans reprise, le flux part de maintenant : l'état initial vient de la liste,
	// que l'interface a déjà chargée. Rejouer tout l'historique doublerait ce
	// travail pour rien.
	since := time.Now()
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, resume); err == nil {
			since = parsed
		}
	}

	ticker := time.NewTicker(runPollInterval)
	defer ticker.Stop()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		updated, latest, err := events.RunsUpdatedSince(r.Context(), since)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			_ = stream.event("error", "", "lecture des exécutions impossible")
			return
		}
		since = latest

		for _, run := range updated {
			payload, err := json.Marshal(toRunPayload(run))
			if err != nil {
				continue
			}
			if err := stream.event("run", latest.Format(time.RFC3339Nano), string(payload)); err != nil {
				return
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := stream.comment("battement"); err != nil {
				return
			}
		case <-ticker.C:
		}
	}
}

func toRunPayload(run store.RunSummary) runPayload {
	return runPayload{
		ID:          run.ID,
		Name:        run.Name,
		Pipeline:    run.Pipeline,
		Status:      run.Status,
		Reason:      run.Reason,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
		Observed:    run.Observed,
	}
}
