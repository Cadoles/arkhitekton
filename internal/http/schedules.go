package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The declared schedules, beside the reactions: the same plans, opened by a clock.
 *
 * The references are checked as a reaction's are, by the same function. The next firing is
 * computed here, when saved, from now: switching a schedule back on does not fire the ticks it
 * slept through.
 */

// ScheduleStore is what the schedules' routes need from the database.
type ScheduleStore interface {
	ListSchedules(ctx context.Context, projectID int64) ([]store.RecordedSchedule, error)
	SaveSchedule(ctx context.Context, entry schedule.Schedule, next *time.Time, actor string) (int64, error)
	DeleteSchedule(ctx context.Context, projectID, id int64) error
}

// WithSchedules serves the schedules.
func (h *AutomationsHandler) WithSchedules(schedules ScheduleStore) *AutomationsHandler {
	h.schedules = schedules
	return h
}

type schedulePayload struct {
	schedule.Schedule
	NextFireAt  *time.Time `json:"next_fire_at,omitempty"`
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	LastOutcome string     `json:"last_outcome,omitempty"`
}

// ListSchedules returns a project's schedules.
func (h *AutomationsHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	recorded, err := h.schedules.ListSchedules(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the schedules", "")
		return
	}
	payload := make([]schedulePayload, 0, len(recorded))
	for _, entry := range recorded {
		payload = append(payload, schedulePayload{
			Schedule: entry.Schedule, NextFireAt: entry.NextFireAt,
			LastFiredAt: entry.LastFiredAt, LastOutcome: entry.LastOutcome,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": payload})
}

// SaveSchedule creates or replaces a schedule.
func (h *AutomationsHandler) SaveSchedule(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	var request schedule.Schedule
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	// The identifier comes from the path, as a reaction's does.
	request.ID = 0
	if raw := strings.TrimSpace(r.PathValue("schedule")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
			return
		}
		request.ID = parsed
	}

	identity, _ := IdentityFrom(r.Context())
	saved, next, err := h.service().SaveSchedule(r.Context(), identity.Subject, project, request)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": saved, "next_fire_at": next})
}

func (h *AutomationsHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("schedule")), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.service().DeleteSchedule(r.Context(), identity.Subject, project, id); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}
