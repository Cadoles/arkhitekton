package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les exécutions de plan.
 *
 * **Un moteur qu'on ne peut pas déboguer est pire qu'un comportement figé**, parce
 * qu'on ne sait plus ce qui a eu lieu (doc 14 §14.10). Cette vue est la
 * contrepartie de l'éditeur : elle dit ce qu'une réaction a réellement fait.
 *
 * # Deux niveaux de lecture, et pourquoi
 *
 * Ce qui s'est passé — quelle étape, quel geste, quelle issue — se lit **sans
 * privilège** : c'est la même question que « pourquoi mes tests n'ont pas
 * démarré », et la chaîne de vérification passe désormais par ici. La réserver à
 * l'administration ferait payer un privilège pour un diagnostic ordinaire.
 *
 * Le **détail** d'un geste, lui, porte l'extrait de réponse d'un tiers : ce qu'un
 * webhook a répondu, avec ce que ce tiers a bien voulu y mettre. Il n'est rendu
 * qu'à l'administration. Ce n'est pas une précaution de principe — c'est que
 * personne n'a de prise sur ce qu'un destinataire renvoie, et qu'un message
 * d'erreur de son côté peut nommer son infrastructure.
 *
 * Les en-têtes **envoyés** n'y figurent jamais : ils portent l'authentification,
 * et rien ne les écrit (voir `internal/webhook`).
 */

// PlanRunStore est ce que la vue attend de la base.
type PlanRunStore interface {
	ListPlanRuns(ctx context.Context, projectID int64, limit int) ([]store.PlanRunSummary, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// PlanRunsHandler sert les exécutions de plan d'un projet.
type PlanRunsHandler struct{ store PlanRunStore }

func NewPlanRunsHandler(runs PlanRunStore) *PlanRunsHandler {
	return &PlanRunsHandler{store: runs}
}

type planStepPayload struct {
	Stage    int    `json:"stage"`
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	// Label vient du plan figé dans l'exécution, et non du catalogue courant :
	// une famille renommée depuis ne doit pas réécrire l'histoire.
	Label string `json:"label,omitempty"`
	State string `json:"state"`
	// JobID rattache un geste de pipeline à son exécution. C'est ce qui permet
	// d'aller lire ses journaux depuis ici.
	JobID *int64 `json:"job_id,omitempty"`
	// Detail porte le motif d'un échec ou la réponse d'un tiers. Rendu à
	// l'administration seulement.
	Detail string `json:"detail,omitempty"`
	// DetailWithheld dit qu'un détail existe et n'est pas montré. Sans lui, un
	// lecteur non administrateur croirait qu'il n'y a rien à voir, et
	// n'appellerait pas quelqu'un qui peut regarder.
	DetailWithheld bool `json:"detail_withheld,omitempty"`
}

type planRunPayload struct {
	ID int64 `json:"id"`
	// Trigger dit d'où vient l'exécution : `builtin:verification` pour la chaîne
	// intégrée, `reaction:<id>` pour une réaction déclarée.
	Trigger   string    `json:"trigger"`
	EventKey  string    `json:"event_key"`
	ReleaseID *int64    `json:"release_id,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	// Context est la charge du fait déclencheur, qui a lié les gabarits. Rendue
	// parce qu'elle explique **ce sur quoi** l'exécution a porté : sans elle, un
	// geste qui a envoyé le mauvais tag ne se diagnostique pas.
	Context map[string]string  `json:"context"`
	Stages  []planStagePayload `json:"stages"`
}

type planStagePayload struct {
	Label string            `json:"label,omitempty"`
	Steps []planStepPayload `json:"steps"`
}

// List rend les exécutions d'un projet, de la plus récente à la plus ancienne.
func (h *PlanRunsHandler) List(w http.ResponseWriter, r *http.Request) {
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

	runs, err := h.store.ListPlanRuns(r.Context(), project.ID, limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des exécutions impossible", "")
		return
	}

	identity, _ := IdentityFrom(r.Context())
	full := identity.RoleIn(project.ID) == auth.RoleAdmin

	payload := make([]planRunPayload, 0, len(runs))
	for _, run := range runs {
		payload = append(payload, toPlanRunPayload(run, full))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"runs":    payload,
		"project": project.Slug,
		// L'écran doit pouvoir dire « certains détails vous sont masqués » plutôt
		// que de laisser croire qu'il n'y a rien à voir.
		"details_visible": full,
	})
}

// toPlanRunPayload compose la vue d'une exécution.
//
// Les gestes sont regroupés par étape, dans l'ordre du **plan figé** — celui qui
// a été exécuté. Le relire depuis le catalogue courant ferait réécrire l'histoire
// par une famille renommée depuis.
func toPlanRunPayload(run store.PlanRunSummary, full bool) planRunPayload {
	stages := make([]planStagePayload, len(run.Plan.Stages))
	for index, stage := range run.Plan.Stages {
		stages[index] = planStagePayload{Label: stage.Label, Steps: []planStepPayload{}}
	}

	for _, step := range run.Steps {
		if step.Stage < 0 || step.Stage >= len(stages) {
			// Un geste hors du plan figé. Ne devrait pas arriver — le plan est
			// copié dans son exécution précisément pour cela — mais le taire
			// laisserait un geste invisible dans une vue qui existe pour montrer.
			continue
		}

		view := planStepPayload{
			Stage: step.Stage, Position: step.Position, Kind: step.Kind,
			Ref: step.Ref, State: step.State, JobID: step.JobID,
			Label: gesteLabelAt(run.Plan, step.Stage, step.Position),
		}
		switch {
		case full:
			view.Detail = step.Detail
		case step.Detail != "":
			view.DetailWithheld = true
		}
		stages[step.Stage].Steps = append(stages[step.Stage].Steps, view)
	}

	return planRunPayload{
		ID: run.ID, Trigger: run.Trigger, EventKey: run.EventKey,
		ReleaseID: run.ReleaseID, State: run.State, CreatedAt: run.CreatedAt,
		Context: orEmptyMap(run.Context), Stages: stages,
	}
}

// gesteLabelAt lit le libellé d'un geste dans le plan figé.
func gesteLabelAt(sujet plan.Plan, stage, position int) string {
	if stage < 0 || stage >= len(sujet.Stages) {
		return ""
	}
	gestes := sujet.Stages[stage].Gestes
	if position < 0 || position >= len(gestes) {
		return ""
	}
	return gestes[position].Label
}
