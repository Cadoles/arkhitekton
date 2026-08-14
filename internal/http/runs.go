package http

import (
	"context"
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
		payload = append(payload, runPayload{
			ID:          run.ID,
			Name:        run.Name,
			Pipeline:    run.Pipeline,
			Status:      run.Status,
			Reason:      run.Reason,
			StartedAt:   run.StartedAt,
			CompletedAt: run.CompletedAt,
			Observed:    run.Observed,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": payload})
}
