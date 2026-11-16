package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

/*
 * An automation's switch, apart from its plan.
 *
 * Switching off goes through no validation of the plan: a reaction whose fact is no longer produced,
 * or a plan naming a kind the catalogue dropped, must stay possible to switch off. Saving the whole
 * entry would refuse it for exactly the reason one wants it silent.
 */

type switchRequest struct {
	Enabled *bool `json:"enabled"`
}

// readSwitch reads `{"enabled": bool}`; a body without the key is refused rather than read as off.
func readSwitch(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var request switchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&request); err != nil ||
		request.Enabled == nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", `expected {"enabled": true|false}`)
		return false, false
	}
	return *request.Enabled, true
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(name)), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
		return 0, false
	}
	return id, true
}

// SwitchAutomation switches a project's release automation on or off.
func (h *AutomationsHandler) SwitchAutomation(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	enabled, ok := readSwitch(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.service().SwitchReleaseAutomation(r.Context(), identity.Subject, project, enabled); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
}

// SwitchReaction switches a reaction on or off.
func (h *AutomationsHandler) SwitchReaction(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "reaction")
	if !ok {
		return
	}
	enabled, ok := readSwitch(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.service().SwitchReaction(r.Context(), identity.Subject, project, id, enabled); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "enabled": enabled})
}

// SwitchSchedule switches a schedule on or off, and returns its next firing.
func (h *AutomationsHandler) SwitchSchedule(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "schedule")
	if !ok {
		return
	}
	enabled, ok := readSwitch(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	next, err := h.service().SwitchSchedule(r.Context(), identity.Subject, project, id, enabled)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "enabled": enabled, "next_fire_at": next})
}
