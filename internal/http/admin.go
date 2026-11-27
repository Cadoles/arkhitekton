package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Cadoles/arkhitekton/internal/auth"
	"github.com/Cadoles/arkhitekton/internal/configure"
	"github.com/Cadoles/arkhitekton/internal/store"
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
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	Directory(ctx context.Context) (map[string]string, error)
	ListAccounts(ctx context.Context) ([]store.Account, error)
	ListGroupRoles(ctx context.Context) ([]store.GroupRole, error)
	SetGroupRoleIn(ctx context.Context, group, role string, projectID *int64, grantedBy string) error
	ClearGroupRoleIn(ctx context.Context, group string, projectID *int64) error
}

// AdminHandler exposes who may use the dashboard, and granting roles.
type AdminHandler struct {
	store AdminStore
	// access writes the roles, the same way the versioned configuration does.
	access configure.Access
}

func NewAdminHandler(admin AdminStore) *AdminHandler {
	return &AdminHandler{store: admin, access: configure.Access{Store: admin}}
}

// Roles returns what each role allows. The people and their roles are read at `/admin/people`.
func (h *AdminHandler) Roles(w http.ResponseWriter, _ *http.Request) {
	// The catalogue is served: the interface must offer the possible values without hard-coding
	// them, and what a role allows is decided by internal/auth.
	catalogue := make([]map[string]any, 0, len(auth.AllRoles))
	for _, role := range auth.AllRoles {
		catalogue = append(catalogue, map[string]any{
			"role":        string(role),
			"permissions": auth.PermissionsOf(role),
			// Promoting depends on the environment's sensitivity, not on a named permission.
			"deploy": map[string]bool{
				"open":      role.Allows(auth.RoleForDeploy(false)),
				"sensitive": role.Allows(auth.RoleForDeploy(true)),
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogue": catalogue})
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

	if err := h.access.SetPerson(r.Context(), identity.Subject, request.Subject, request.Username,
		request.Role, request.Project); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": strings.TrimSpace(request.Subject), "role": request.Role,
		// The change closes the person's sessions: saying so keeps an administrator from believing
		// the old role is still in force, or the opposite.
		"sessions_closed": true,
	})
}

// Revoke removes a user's role.
func (h *AdminHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	subject := r.PathValue("subject")
	// The scope comes from the request: removing "someone's role" no longer has a single
	// meaning now that they may have one per project.
	if err := h.access.ClearPerson(r.Context(), identity.Subject, subject, r.URL.Query().Get("project")); err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subject": subject, "sessions_closed": true})
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
