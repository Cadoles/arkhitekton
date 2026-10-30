package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// The identifier comes from the path, as for a reaction: the body cannot move a schedule to
	// another project.
	var id int64
	if raw := strings.TrimSpace(r.PathValue("schedule")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
			return
		}
		id = parsed
	}
	entry := schedule.Schedule{
		ID: id, ProjectID: project.ID, Name: strings.TrimSpace(request.Name),
		Cron: strings.TrimSpace(request.Cron), Timezone: strings.TrimSpace(request.Timezone),
		Ref: strings.TrimSpace(request.Ref), Enabled: request.Enabled, Plan: request.Plan,
	}
	if err := entry.Validate(); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid schedule", err.Error())
		return
	}
	if err := h.checkScheduleRefs(r.Context(), entry); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid schedule", err.Error())
		return
	}

	var next *time.Time
	if entry.Enabled {
		at, err := entry.Next(time.Now())
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid schedule", err.Error())
			return
		}
		next = &at
	}

	identity, _ := IdentityFrom(r.Context())
	saved, err := h.schedules.SaveSchedule(r.Context(), entry, next, identity.Subject)
	switch {
	case errors.Is(err, store.ErrScheduleExists):
		writeProblem(w, http.StatusConflict, "Name already taken",
			"a schedule of this project already carries this name")
		return
	case errors.Is(err, store.ErrScheduleNotFound):
		writeProblem(w, http.StatusNotFound, "Schedule not found", "")
		return
	case err != nil:
		h.logger.Error("recording a schedule", "project", project.Slug, "name", entry.Name, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot save", "")
		return
	}

	h.audit(r, "schedule.save", project.Slug+"/"+entry.Name, map[string]any{
		"cron": entry.Cron, "timezone": entry.Timezone, "ref": entry.Ref, "enabled": entry.Enabled,
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": saved, "next_fire_at": next})
}

// DeleteSchedule removes a schedule.
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
	if err := h.schedules.DeleteSchedule(r.Context(), project.ID, id); err != nil {
		if errors.Is(err, store.ErrScheduleNotFound) {
			writeProblem(w, http.StatusNotFound, "Schedule not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot delete", "")
		return
	}
	h.audit(r, "schedule.delete", project.Slug+"/"+strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// checkScheduleRefs refuses a plan naming nothing executable, and a role: as for a reaction, a
// schedule aims at no release, and a role would advance none.
func (h *AutomationsHandler) checkScheduleRefs(ctx context.Context, entry schedule.Schedule) error {
	var problems []error
	for index, stage := range entry.Plan.Stages {
		if stage.Role != "" {
			problems = append(problems, fmt.Errorf(
				"stage %d of a schedule carries the role %q: a role only advances "+
					"a release's automation", index+1, stage.Role))
		}
	}
	problems = append(problems, h.checkPlanRefs(ctx, entry.Plan))
	return errors.Join(problems...)
}
