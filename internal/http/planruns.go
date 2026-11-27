package http

import (
	"time"

	"github.com/Cadoles/arkhitekton/internal/plan"
	"github.com/Cadoles/arkhitekton/internal/store"
)

/*
 * A plan run's view, as the journal and a pull request's page show it.
 *
 * A step's **detail** carries a third party's response excerpt: what a webhook answered, with
 * whatever that third party chose to put there. It is returned to administration only: nobody has
 * any hold on what a recipient returns, and an error message on its side may name its
 * infrastructure.
 *
 * The headers **sent** never appear in it: they carry the authentication, and nothing writes them
 * (see `internal/webhook`).
 */

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
func stepLabelAt(frozen plan.Plan, stage, position int) string {
	if stage < 0 || stage >= len(frozen.Stages) {
		return ""
	}
	steps := frozen.Stages[stage].Gestes
	if position < 0 || position >= len(steps) {
		return ""
	}
	return steps[position].Label
}
