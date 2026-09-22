package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * A release's automation: what a project builds, then verifies.
 *
 * # What this screen replaces
 *
 * Three hard-coded places: a loop over the `targets` column in the API, a loop over the
 * catalogue's kinds in the observer, and a `verification` flag that stated the chaining. Here, a
 * plan — the same structure as a reaction, with a role on its stages.
 *
 * # The role is checked, not taken on trust
 *
 * A `build` stage one of whose steps does not produce an image is refused. It is the check that
 * prevents a silent change of life cycle: adding a notification action to the build stage would
 * make that stage's conclusion — hence the release moving into verification — depend on a
 * webhook.
 *
 * The catalogue already declares `produces`. The check is a comparison, not a heuristic.
 */

// automationPayload is what the screen reads and writes.
type automationPayload struct {
	Plan plan.Plan `json:"plan"`
	// Declared separates "nobody typed anything" from "somebody emptied it".
	//
	// The screen cannot infer it from an empty plan: both look identical, and only the first
	// will be recomposed from the project's targets at the worker's next start-up. Saying so
	// avoids announcing "this project no longer builds" to someone whose automation is simply
	// awaiting the catch-up.
	Declared  bool   `json:"declared"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// Automation returns a project's automation.
func (h *AutomationsHandler) Automation(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	entry, err := h.store.ReleaseAutomation(r.Context(), project.ID)
	switch {
	case errors.Is(err, store.ErrNoAutomation):
		// An empty plan and `declared: false`. This is not a 404: the project exists, and the
		// question "what does it build?" has an answer — "nothing declared for now".
		writeJSON(w, http.StatusOK, automationPayload{Plan: plan.Plan{}, Declared: false})
		return
	case err != nil:
		h.logger.Error("reading the automation", "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Lecture impossible", "")
		return
	}

	writeJSON(w, http.StatusOK, automationPayload{
		Plan: entry.Plan, Declared: true, UpdatedBy: entry.UpdatedBy,
	})
}

type automationRequest struct {
	Plan plan.Plan `json:"plan"`
}

// SaveAutomation replaces a project's automation.
func (h *AutomationsHandler) SaveAutomation(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	var request automationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	// An empty plan is accepted, deliberately: it is how to say "this project no longer
	// builds". plan.Validate would refuse it — a reaction with no stage reacts to nothing — so
	// it is only checked when it carries something.
	if len(request.Plan.Stages) > 0 {
		if err := request.Plan.Validate(); err != nil {
			writeProblem(w, http.StatusBadRequest, "Automatisation invalide", err.Error())
			return
		}
		if err := h.checkPlanRefs(r.Context(), request.Plan); err != nil {
			writeProblem(w, http.StatusBadRequest, "Automatisation invalide", err.Error())
			return
		}
		if err := h.checkRoles(request.Plan); err != nil {
			writeProblem(w, http.StatusBadRequest, "Automatisation invalide", err.Error())
			return
		}
	}

	identity, _ := IdentityFrom(r.Context())
	err := h.store.SaveReleaseAutomation(r.Context(), store.ReleaseAutomation{
		ProjectID: project.ID, Plan: request.Plan,
	}, identity.Subject)
	if err != nil {
		h.logger.Error("recording the automation",
			"project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// The log carries the count of stages and steps, not the plan: it carries templates, and an
	// audit log is read with fewer precautions than a database. The count is enough to see that
	// an automation was emptied.
	h.audit(r, "automation.save", project.Slug, map[string]any{
		"stages": len(request.Plan.Stages), "steps": stepCount(request.Plan),
	})

	writeJSON(w, http.StatusOK, automationPayload{Plan: request.Plan, Declared: true})
}

func stepCount(p plan.Plan) int {
	total := 0
	for _, stage := range p.Stages {
		total += len(stage.Gestes)
	}
	return total
}

/*
 * The role check.
 */

// roleProduct says what a step must produce to hold a role.
//
// A table and not a switch: it reads at a glance, and the error message uses it to name
// what it expected.
var roleProduct = map[plan.Role]catalog.Product{
	plan.RoleBuild: catalog.ProductImage,
	plan.RoleTest:  catalog.ProductResults,
}

// checkRoles refuses a stage whose steps do not hold its role.
//
// Without that check, a release's life cycle would change by accident. Two cases, and the
// second is the more insidious:
//
//   - a test step in a `build` stage: the release would move to verification as soon as the
//     tests concluded, that is, after them;
//   - an action in a stage with a role: a failed webhook would fail the release, although it
//     has nothing to say about its quality.
func (h *AutomationsHandler) checkRoles(entry plan.Plan) error {
	var problems []error

	for index, stage := range entry.Stages {
		attendu, porte := roleProduct[stage.Role]
		if !porte {
			// A stage with no role touches nothing: it may carry what it likes. It is what allows
			// warning a team without putting the release's state at stake.
			continue
		}

		for _, step := range stage.Gestes {
			if step.Kind != plan.GestePipeline {
				problems = append(problems, fmt.Errorf(
					"stage %d carries the role %q and the step %q is not a pipeline: "+
						"its failure would fail the release without saying anything about its quality",
					index+1, stage.Role, step.Ref))
				continue
			}
			if h.catalog == nil {
				problems = append(problems, fmt.Errorf(
					"the role of stage %d cannot be checked: no catalogue "+
						"is loaded", index+1))
				continue
			}
			pipeline, found := h.catalog.Find(step.Ref)
			if !found {
				// Already reported by the reference check. Repeating it here would give two messages for
				// one mistake.
				continue
			}
			if !pipeline.Yields(attendu) {
				problems = append(problems, fmt.Errorf(
					"stage %d carries the role %q and the pipeline %q does not produce %q "+
						"(il produit: %s)",
					index+1, stage.Role, step.Ref, attendu,
					productsOf(pipeline)))
			}
		}
	}

	return errors.Join(problems...)
}

// productsOf names what a pipeline declares it produces, for a message.
func productsOf(pipeline catalog.Pipeline) string {
	if len(pipeline.Produces) == 0 {
		return "nothing declared"
	}
	names := make([]string, 0, len(pipeline.Produces))
	for _, product := range pipeline.Produces {
		names = append(names, string(product))
	}
	return strings.Join(names, ", ")
}
