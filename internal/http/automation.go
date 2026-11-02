package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
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
		writeProblem(w, http.StatusInternalServerError, "Cannot read", "")
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

	identity, _ := IdentityFrom(r.Context())
	if err := h.service().SaveReleaseAutomation(r.Context(), identity.Subject, project, request.Plan); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, automationPayload{Plan: request.Plan, Declared: true})
}

// service is the plans' rules, built from what this handler was given.
func (h *AutomationsHandler) service() configure.Automations {
	return configure.Automations{
		Store: h.store, Schedules: h.schedules, Catalog: h.catalog, Registry: h.registry, Logger: h.logger,
	}
}
