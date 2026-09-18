package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les pipelines qu'un projet déclare vérifier.
 *
 * Le catalogue dit ce que **l'instance** sait lancer ; il ne dit pas ce que chaque
 * projet veut lancer. La distinction n'est pas théorique : un projet sans campagne
 * de charge voyait `load-tests` s'enchaîner après chaque construction, échouer
 * faute de scénario, et laisser sa release en `verifying` sur un verdict qui
 * n'arriverait jamais.
 *
 * L'écran montre donc le **registre** — ce que le binaire et son catalogue
 * savent — et, en regard, ce que le projet en retient.
 */

// ProjectPipelineStore est ce que cet écran attend de la base.
type ProjectPipelineStore interface {
	ProjectPipelines(ctx context.Context, projectID int64) (map[string]store.ProjectPipeline, error)
	SetProjectPipeline(ctx context.Context, projectID int64, kind string, enabled bool, actor string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// ProjectPipelinesHandler expose le registre et les décisions d'un projet.
type ProjectPipelinesHandler struct {
	store   ProjectPipelineStore
	catalog *catalog.Catalog
	logger  *slog.Logger
}

func NewProjectPipelinesHandler(
	pipelines ProjectPipelineStore, registry *catalog.Catalog, logger *slog.Logger,
) *ProjectPipelinesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProjectPipelinesHandler{store: pipelines, catalog: registry, logger: logger}
}

// pipelineVw est une famille du registre et ce que le projet en dit.
type pipelineVw struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Verification dit que la famille s'enchaîne après une construction réussie.
	// Les autres se lancent à la main depuis la file, et les désactiver n'a pas le
	// même effet : l'écran doit pouvoir le dire.
	Verification bool `json:"verification"`
	Enabled      bool `json:"enabled"`
	// Declared distingue « le projet a décidé » de « le projet n'a rien dit ».
	// Sans lui, l'écran ne saurait pas si l'état affiché est un choix ou un
	// défaut, et ne pourrait pas proposer de revenir au défaut.
	Declared  bool   `json:"declared"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// List rend le registre et les décisions du projet.
func (h *ProjectPipelinesHandler) List(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	decisions, err := h.store.ProjectPipelines(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des pipelines impossible", "")
		return
	}

	payload := make([]pipelineVw, 0)
	if h.catalog != nil {
		for _, pipeline := range h.catalog.Pipelines {
			entry, declared := decisions[pipeline.Kind]
			// L'absence de décision vaut **activé** : c'est ce qui fait qu'un
			// projet déclaré avant cette table continue de recevoir ce qu'il
			// recevait.
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

// Set enregistre la décision d'un projet sur une famille.
func (h *ProjectPipelinesHandler) Set(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	kind := strings.TrimSpace(r.PathValue("kind"))
	if h.catalog == nil {
		writeProblem(w, http.StatusConflict, "Catalogue indisponible", "")
		return
	}
	pipeline, found := h.catalog.Find(kind)
	if !found {
		// Refusé plutôt qu'enregistré : une ligne pour une famille inexistante
		// donnerait un écran qui dit « désactivé » d'un pipeline que personne ne
		// lance, et masquerait la faute de frappe.
		writeProblem(w, http.StatusNotFound, "Famille inconnue du catalogue", kind)
		return
	}

	var request projectPipelineRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	identity, _ := IdentityFrom(r.Context())
	if err := h.store.SetProjectPipeline(
		r.Context(), project.ID, pipeline.Kind, request.Enabled, identity.Subject); err != nil {
		h.logger.Error("enregistrement d'un pipeline de projet",
			"projet", project.Slug, "kind", pipeline.Kind, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// Consigné : désactiver une vérification change ce qu'une release aura
	// traversé avant d'être livrée, et c'est le genre de décision qu'on veut
	// pouvoir retrouver.
	h.audit(r, "project.pipeline", project.Slug+"/"+pipeline.Kind,
		map[string]any{"enabled": request.Enabled})

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
			writeProblem(w, http.StatusNotFound, "Projet inconnu", "")
			return store.Project{}, false
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
		return store.Project{}, false
	}
	return project, true
}

func (h *ProjectPipelinesHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", action, "target", target, "error", err)
	}
}
