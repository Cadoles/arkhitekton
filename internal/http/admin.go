package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// AdminStore is what administration needs from the database.
type AdminStore interface {
	ListUserRoles(ctx context.Context) ([]store.UserRole, error)
	// SetRoleIn grants a role in a project, or everywhere when it is nil.
	SetRoleIn(ctx context.Context, subject, username, role string, projectID *int64, grantedBy string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	// RevokeRoleIn removes the role of one scope. The scope matters in the reasoning even when
	// it is nil: deleting every row of a subject would, while meaning to remove one project,
	// delete the global role that holds everywhere else.
	RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error
	ListAudit(ctx context.Context, target string, limit int) ([]store.AuditEntry, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	Directory(ctx context.Context) (map[string]string, error)
}

// AdminHandler exposes role granting and the audit log.
type AdminHandler struct{ store AdminStore }

func NewAdminHandler(admin AdminStore) *AdminHandler { return &AdminHandler{store: admin} }

type userRolePayload struct {
	Subject  string `json:"subject"`
	Username string `json:"username,omitempty"`
	Role     string `json:"role"`
	// GrantedBy is `bootstrap` for accounts promoted by configuration. The interface flags it:
	// removing such a role without touching the configuration makes it reappear at the next
	// restart.
	GrantedBy string    `json:"granted_by"`
	GrantedAt time.Time `json:"granted_at"`
	// Project names the scope. Empty for a global role — the vast majority.
	Project string `json:"project,omitempty"`
	// ProjectLabel is its readable label, so as not to display a bare slug.
	ProjectLabel string     `json:"project_label,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	Sessions     int        `json:"sessions"`
}

// Roles returns the granted roles and what they allow.
func (h *AdminHandler) Roles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.store.ListUserRoles(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the roles", "")
		return
	}

	who := resolveActors(r.Context(), h.store, slog.Default())

	payload := make([]userRolePayload, 0, len(roles))
	for _, role := range roles {
		payload = append(payload, userRolePayload{
			Subject: role.Subject, Username: role.Username, Role: role.Role,
			GrantedBy: who.name(role.GrantedBy), GrantedAt: role.GrantedAt,
			Project: role.ProjectSlug, ProjectLabel: role.ProjectLabel,
			LastSeen: role.LastSeen, Sessions: role.Sessions,
		})
	}

	// The catalogue of roles comes with the list: the interface must offer the possible values
	// without hard-coding them, and what a role allows is decided by internal/auth.
	catalogue := make([]map[string]any, 0, len(auth.AllRoles))
	for _, role := range auth.AllRoles {
		catalogue = append(catalogue, map[string]any{
			"role":        string(role),
			"permissions": auth.PermissionsOf(role),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"roles": payload, "catalogue": catalogue})
}

type grantRequest struct {
	// Subject is the identity provider's identifier, not the displayed name: it is what appears
	// in the token, and what authorization is about. A username changes, a subject does not.
	Subject  string `json:"subject"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Project restricts the grant to one project, named by its slug. Empty, the role holds
	// everywhere — which is still the common case, and what every earlier grant means.
	Project string `json:"project,omitempty"`
}

// Grant grants a role.
func (h *AdminHandler) Grant(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	var request grantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	request.Subject = strings.TrimSpace(request.Subject)
	if request.Subject == "" {
		writeProblem(w, http.StatusBadRequest, "Subject required",
			"the identity provider's identifier, not the displayed name")
		return
	}

	role, err := auth.ParseRole(request.Role)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Unknown role", err.Error())
		return
	}

	// The scope is optional: without it the role holds everywhere, which is still the common
	// case. A named project restricts the grant to it alone.
	var projectID *int64
	if slug := strings.TrimSpace(request.Project); slug != "" {
		project, err := h.store.ProjectBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, store.ErrProjectNotFound) {
				writeProblem(w, http.StatusNotFound, "Unknown project", slug)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
			return
		}
		projectID = &project.ID
	}

	err = h.store.SetRoleIn(r.Context(), request.Subject,
		strings.TrimSpace(request.Username), string(role), projectID, identity.Subject)
	if err != nil && projectID != nil && role == auth.RoleAdmin {
		writeProblem(w, http.StatusUnprocessableEntity, "Impossible scope",
			"the administrator role governs the accounts and the repositories, which belong to no project")
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			// 409 and not 403: the request is legitimate and the caller has the right, it is the
			// system's state that forbids it.
			writeProblem(w, http.StatusConflict, "Last administrator", err.Error())
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot assign", "")
		return
	}

	h.audit(r, store.AuditRoleGrant, request.Subject, map[string]any{
		"role": string(role), "username": request.Username,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"subject": request.Subject, "role": string(role),
		// The change closes the person's sessions: saying so keeps an administrator from believing
		// the old role is still in force, or the opposite.
		"sessions_closed": true,
	})
}

// Revoke removes a user's role.
func (h *AdminHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	subject := r.PathValue("subject")

	// The scope comes from the request: removing "someone's role" no longer has a single
	// meaning now that they may have one per project.
	var projectID *int64
	if slug := strings.TrimSpace(r.URL.Query().Get("project")); slug != "" {
		project, err := h.store.ProjectBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, store.ErrProjectNotFound) {
				writeProblem(w, http.StatusNotFound, "Unknown project", slug)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
			return
		}
		projectID = &project.ID
	}

	if err := h.store.RevokeRoleIn(r.Context(), subject, projectID); err != nil {
		switch {
		case errors.Is(err, store.ErrRoleNotFound):
			writeProblem(w, http.StatusNotFound, "Role not found", "")
		case errors.Is(err, store.ErrLastAdmin):
			writeProblem(w, http.StatusConflict, "Last administrator", err.Error())
		default:
			writeProblem(w, http.StatusInternalServerError, "Cannot remove", "")
		}
		return
	}

	h.audit(r, store.AuditRoleRevoke, subject, nil)
	writeJSON(w, http.StatusOK, map[string]any{"subject": subject, "sessions_closed": true})
}

type auditPayload struct {
	ID      int64          `json:"id"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Target  string         `json:"target,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
	At      time.Time      `json:"at"`
}

// Audit returns the log.
//
// Filterable by target: that is the read people actually perform — "what happened to this
// release" — and the index exists for it.
func (h *AdminHandler) Audit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	entries, err := h.store.ListAudit(r.Context(), r.URL.Query().Get("target"), limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the log", "")
		return
	}

	who := resolveActors(r.Context(), h.store, slog.Default())

	payload := make([]auditPayload, 0, len(entries))
	for _, entry := range entries {
		payload = append(payload, auditPayload{
			ID: entry.ID, Actor: who.name(entry.Actor), Action: entry.Action,
			Target: entry.Target, Payload: entry.Payload, At: entry.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": payload})
}

// audit records an administration action without failing it.
//
// The same rule as elsewhere: the log is a record, not a transaction. The role is already
// granted.
func (h *AdminHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
