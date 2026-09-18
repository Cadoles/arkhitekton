package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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
 * Ce qui s'est passé — quelle étape, quel step, quelle issue — se lit **sans
 * privilège** : c'est la même question que « pourquoi mes tests n'ont pas
 * démarré », et la chaîne de vérification passe désormais par ici. La réserver à
 * l'administration ferait payer un privilège pour un diagnostic ordinaire.
 *
 * Le **détail** d'un step, lui, porte l'extrait de réponse d'un tiers : ce qu'un
 * webhook a répondu, avec ce que ce tiers a bien voulu y mettre. Il n'est rendu
 * qu'à l'administration. Ce n'est pas une précaution de principe — c'est que
 * personne n'a de prise sur ce qu'un destinataire renvoie, et qu'un message
 * d'erreur de son côté peut nommer son infrastructure.
 *
 * Les en-têtes **envoyés** n'y figurent jamais : ils portent l'authentification,
 * et rien ne les écrit (voir `internal/webhook`).
 */

// PlanRunStore is what the view needs from the database.
type PlanRunStore interface {
	ListPlanRuns(ctx context.Context, projectID int64, limit int) ([]store.PlanRunSummary, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// PlanRunsHandler serves a project's plan runs.
type PlanRunsHandler struct{ store PlanRunStore }

func NewPlanRunsHandler(runs PlanRunStore) *PlanRunsHandler {
	return &PlanRunsHandler{store: runs}
}

type planStepPayload struct {
	Stage    int    `json:"stage"`
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	// Label comes from the plan frozen into the run, and not from the current catalogue: a
	// family renamed since must not rewrite history.
	Label string `json:"label,omitempty"`
	State string `json:"state"`
	// JobID attaches a pipeline step to its run. It is what allows reading its logs from here.
	JobID *int64 `json:"job_id,omitempty"`
	// Detail carries the reason for a failure or a third party's answer. Returned to
	// administration only.
	Detail string `json:"detail,omitempty"`
	// DetailWithheld says a detail exists and is not shown. Without it, a non-administrator
	// reader would believe there is nothing to see, and would not call someone who can look.
	DetailWithheld bool `json:"detail_withheld,omitempty"`
}

type planRunPayload struct {
	ID int64 `json:"id"`
	// Trigger says where the run came from: `builtin:verification` for the built-in chain,
	// `reaction:<id>` for a declared reaction.
	Trigger   string    `json:"trigger"`
	EventKey  string    `json:"event_key"`
	ReleaseID *int64    `json:"release_id,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	// Context is the triggering fact's payload, which bound the templates. Returned because it
	// explains what the run was about: without it, a step that sent the wrong tag cannot be
	// diagnosed.
	Context map[string]string  `json:"context"`
	Stages  []planStagePayload `json:"stages"`
}

type planStagePayload struct {
	Label string            `json:"label,omitempty"`
	Steps []planStepPayload `json:"steps"`
}

// List returns a project's runs, newest first.
func (h *PlanRunsHandler) List(w http.ResponseWriter, r *http.Request) {
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

	runs, err := h.store.ListPlanRuns(r.Context(), project.ID, limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the runs", "")
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
		// The screen must be able to say "some details are hidden from you" rather than suggest
		// there is nothing to see.
		"details_visible": full,
	})
}

// toPlanRunPayload composes a run's view.
//
// The steps are grouped by stage, in the order of the frozen plan — the one that ran.
// Reading it from the current catalogue would let a family renamed since rewrite history.
func toPlanRunPayload(run store.PlanRunSummary, full bool) planRunPayload {
	stages := make([]planStagePayload, len(run.Plan.Stages))
	for index, stage := range run.Plan.Stages {
		stages[index] = planStagePayload{Label: stage.Label, Steps: []planStepPayload{}}
	}

	for _, step := range run.Steps {
		if step.Stage < 0 || step.Stage >= len(stages) {
			// A step outside the frozen plan. It should not happen — the plan is copied into its run
			// precisely for that — but hiding it would leave a step invisible in a view that exists to
			// show.
			continue
		}

		view := planStepPayload{
			Stage: step.Stage, Position: step.Position, Kind: step.Kind,
			Ref: step.Ref, State: step.State, JobID: step.JobID,
			Label: stepLabelAt(run.Plan, step.Stage, step.Position),
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

// stepLabelAt reads a step's label from the frozen plan.
func stepLabelAt(sujet plan.Plan, stage, position int) string {
	if stage < 0 || stage >= len(sujet.Stages) {
		return ""
	}
	steps := sujet.Stages[stage].Gestes
	if position < 0 || position >= len(steps) {
		return ""
	}
	return steps[position].Label
}
