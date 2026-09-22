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
 * The plan runs.
 *
 * **An engine one cannot debug is worse than frozen behaviour**, because one no longer knows what
 * took place. This view is the editor's counterpart: it says what a reaction actually did.
 *
 * # Two levels of reading, and why
 *
 * What happened — which stage, which step, which outcome — is read **with no privilege**: it is
 * the same question as "why did my tests not start", and the verification chain now goes through
 * here. Reserving it to administration would charge a privilege for an ordinary diagnosis.
 *
 * A step's **detail**, on the other hand, carries a third party's response excerpt: what a
 * webhook answered, with whatever that third party chose to put there. It is returned to
 * administration only. It is no precaution of principle — it is that nobody has any hold on what
 * a recipient returns, and that an error message on its side may name its infrastructure.
 *
 * The headers **sent** never appear in it: they carry the authentication, and nothing writes them
 * (see `internal/webhook`).
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
