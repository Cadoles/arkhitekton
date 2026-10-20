package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Administration of the tracker synchronisation, **per project**.
 *
 * Three things to watch, and none of them is a failure of the dashboard:
 *
 *   - the statuses with no declared mapping, which make the completion rate drift with nothing
 *     reporting it;
 *   - the versions several of whose names reduce to the same number, which stop a release being
 *     attached automatically;
 *   - the provisional versions never adopted, a symptom of a typo in a tag or of a version
 *     missing from the tracker.
 *
 * Displaying them is what makes them fixable. Keeping quiet about them would let them skew the
 * versions screen silently.
 *
 * # Why this screen left the general administration
 *
 * Everything it shows belongs to a project: the cursor, the status mapping, the ambiguous
 * versions. It was global because a single project was tracked — and the vocabulary mapped, "En
 * revue", belongs to the client, not to the instance. Leaving it shared would mean a status
 * declared for one client moved a completion rate displayed to another.
 */

// SyncAdminStore is what synchronisation administration needs from the database.
type SyncAdminStore interface {
	GetSyncState(ctx context.Context, source string, projectID int64) (store.SyncState, error)
	ResetSyncCursor(ctx context.Context, source string, projectID int64) error
	UnmappedStatuses(ctx context.Context, projectID int64) ([]store.UnmappedStatus, error)
	AmbiguousVersions(ctx context.Context, projectID int64) ([]store.AmbiguousVersion, error)
	PendingVersions(ctx context.Context, projectID int64) ([]string, error)
	StatusMapping(ctx context.Context, projectID int64) (map[string]string, error)
	SetStatusBucket(ctx context.Context, projectID int64, statusName, bucket string) error
	ReprojectIssues(ctx context.Context, projectID int64) (int64, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// SyncAdminHandler exposes the state of the synchronisations and their corrections.
type SyncAdminHandler struct {
	store SyncAdminStore
}

// NewSyncAdminHandler builds the handler.
//
// It no longer receives a "configured" flag: the tracker's configuration is now a plugin
// setting, specific to the project, and it is the project's declaration that says whether
// the tracker concerns it. A global flag would answer for everyone.
func NewSyncAdminHandler(admin SyncAdminStore) *SyncAdminHandler {
	return &SyncAdminHandler{store: admin}
}

// project resolves the project named in the path.
func (h *SyncAdminHandler) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	project, err := h.store.ProjectBySlug(r.Context(), strings.TrimSpace(r.PathValue("project")))
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown project", "")
			return store.Project{}, false
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
		return store.Project{}, false
	}
	return project, true
}

// State returns the synchronisation's state and what needs a human decision.
func (h *SyncAdminHandler) State(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	state, err := h.store.GetSyncState(r.Context(), "redmine", project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the state", "")
		return
	}

	unmapped, err := h.store.UnmappedStatuses(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the statuses", "")
		return
	}

	ambiguous, err := h.store.AmbiguousVersions(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the versions", "")
		return
	}

	pending, err := h.store.PendingVersions(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the provisional versions", "")
		return
	}

	mapping, err := h.store.StatusMapping(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the mapping", "")
		return
	}

	unmappedPayload := make([]map[string]any, 0, len(unmapped))
	for _, entry := range unmapped {
		unmappedPayload = append(unmappedPayload, map[string]any{
			"status_name": entry.StatusName, "issues": entry.Issues,
		})
	}

	ambiguousPayload := make([]map[string]any, 0, len(ambiguous))
	for _, entry := range ambiguous {
		ambiguousPayload = append(ambiguousPayload, map[string]any{
			"normalized": entry.Normalized, "names": entry.Names,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		// "Configured" is now read from the project: it declares its tracker, or it does not.
		// Without that distinction the screen would say "never synchronised" of a project that
		// simply has no tracker, and send someone hunting for a fault.
		"configured": strings.TrimSpace(project.TrackerProject) != "",
		"project":    project.Slug,
		"sync":       syncStateView(state),
		"unmapped":   unmappedPayload,
		"ambiguous":  ambiguousPayload,
		"pending":    orEmptyStrings(pending),
		"mapping":    mapping,
	})
}

func orEmptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// Resync clears the cursor so the next pass re-reads everything.
//
// It does not query the tracker here: synchronisation lives in the worker, under the leader
// lease, and the api has no path to it. Clearing the cursor is the signal, and the next
// pass re-reads everything.
func (h *SyncAdminHandler) Resync(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	// Bounded to the project: re-reading everything calls a third-party service, and nothing
	// justifies making one deployment bear the doubt one has about another.
	if err := h.store.ResetSyncCursor(r.Context(), "redmine", project.ID); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot reset", "")
		return
	}

	h.audit(r, "sync.redmine.reset", project.Slug, nil)

	writeJSON(w, http.StatusAccepted, map[string]any{
		// The message states the wait: without it, a user seeing nothing change would keep
		// relaunching.
		"message": "Cursor reset. The next synchronisation will read everything again, within five minutes.",
	})
}

type statusMappingRequest struct {
	Bucket string `json:"bucket"`
}

// The accepted buckets come from the store: a second enumeration here would diverge from
// the database constraint at the first addition.

// SetMapping declares a status's mapping, then reapplies it.
//
// Reapplying happens in the same gesture, and that is not a detail: without it the new rule
// would hold only for issues modified afterwards in the tracker, and the screen would mix
// two rules without saying so.
func (h *SyncAdminHandler) SetMapping(w http.ResponseWriter, r *http.Request) {
	project, ok := h.project(w, r)
	if !ok {
		return
	}

	statusName := strings.TrimSpace(r.PathValue("status"))
	if statusName == "" {
		writeProblem(w, http.StatusBadRequest, "Missing status", "")
		return
	}

	var request statusMappingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	if !store.ValidBucket(request.Bucket) {
		writeProblem(w, http.StatusBadRequest, "Invalid bucket",
			"The bucket is one of “todo”, “doing”, “client-test”, “done” or “rejected”.")
		return
	}

	if err := h.store.SetStatusBucket(r.Context(), project.ID, statusName, request.Bucket); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot save", "")
		return
	}

	// Bounded to the project, like the declaration: reapplying one's mapping must not move the
	// other's issues, whose vocabulary is not the same.
	moved, err := h.store.ReprojectIssues(r.Context(), project.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot reapply", "")
		return
	}

	// The log carries how many moved: changing a status's mapping moves a completion rate
	// displayed to the client, which deserves a record.
	h.audit(r, "sync.redmine.mapping", project.Slug+"/"+statusName, map[string]any{
		"bucket": request.Bucket, "issues_moved": moved,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status_name": statusName, "bucket": request.Bucket, "issues_moved": moved,
	})
}

func (h *SyncAdminHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
