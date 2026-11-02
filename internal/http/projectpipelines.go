package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The pipelines a project declares it verifies.
 *
 * The catalogue says what **the instance** can launch; it does not say what each project wants to
 * launch. The distinction is not theoretical: a project with no load campaign saw `load-tests`
 * follow every build, fail for want of a scenario, and leave its release in `verifying` on a
 * verdict that would never arrive.
 *
 * The screen therefore shows the **registry** — what the binary and its catalogue can do — and,
 * against it, what the project keeps of it.
 */

// ProjectPipelineStore is what this screen needs from the database.
type ProjectPipelineStore interface {
	ProjectPipelines(ctx context.Context, projectID int64) (map[string]store.ProjectPipeline, error)
	SetProjectPipeline(ctx context.Context, projectID int64, kind string, enabled bool, actor string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// ProjectPipelinesHandler exposes the registry and a project's decisions.
type ProjectPipelinesHandler struct {
	store   ProjectPipelineStore
	catalog catalog.Source
	logger  *slog.Logger
}

func NewProjectPipelinesHandler(
	pipelines ProjectPipelineStore, registry catalog.Source, logger *slog.Logger,
) *ProjectPipelinesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProjectPipelinesHandler{store: pipelines, catalog: registry, logger: logger}
}

// pipelineVw is a registry kind and what the project says about it.
type pipelineVw struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Verification says the kind chains after a successful build. The others are launched by
	// hand from the queue, and disabling them does not have the same effect: the screen must be
	// able to say so.
	Verification bool `json:"verification"`
	Enabled      bool `json:"enabled"`
	// Declared separates "the project decided" from "the project said nothing". Without it, the
	// screen would not know whether the state displayed is a choice or a default, and could not
	// offer a return to the default.
	Declared  bool   `json:"declared"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// List returns the registry and the project's decisions.
func (h *ProjectPipelinesHandler) List(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	decisions, err := h.store.ProjectPipelines(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the pipelines", "")
		return
	}

	payload := make([]pipelineVw, 0)
	if h.catalog != nil {
		for _, pipeline := range catalog.Of(h.catalog).Pipelines {
			entry, declared := decisions[pipeline.Kind]
			// The absence of a decision means enabled: that is what keeps a project declared before
			// this table receiving what it used to receive.
			enabled := true
			if declared {
				enabled = entry.Enabled
			}
			payload = append(payload, pipelineVw{
				Kind: pipeline.Kind, Label: pipeline.Label,
				Verification: pipeline.Verification,
				Enabled:      enabled, Declared: declared, UpdatedBy: entry.UpdatedBy,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pipelines": payload,
		"project":   project.Slug,
	})
}

type projectPipelineRequest struct {
	Enabled bool `json:"enabled"`
}

// Set records a project's decision about a kind.
func (h *ProjectPipelinesHandler) Set(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	var request projectPipelineRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	identity, _ := IdentityFrom(r.Context())
	service := configure.ProjectPipelines{Store: h.store, Catalog: h.catalog, Logger: h.logger}
	pipeline, err := service.Set(r.Context(), identity.Subject, project, r.PathValue("kind"), request.Enabled)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pipelineVw{
		Kind: pipeline.Kind, Label: pipeline.Label,
		Verification: pipeline.Verification,
		Enabled:      request.Enabled, Declared: true, UpdatedBy: identity.Subject,
	})
}

func (h *ProjectPipelinesHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	project, err := h.store.ProjectBySlug(r.Context(), strings.TrimSpace(r.PathValue("project")))
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown project", "")
			return store.Project{}, false
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
		return store.Project{}, false
	}
	return project, true
}
