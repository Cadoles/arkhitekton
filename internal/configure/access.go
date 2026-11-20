package configure

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Who may use the dashboard: the roles of people and of the identity provider's groups, written
 * the same way from the screen and from the versioned configuration.
 */

// AccessStore is what granting roles needs.
type AccessStore interface {
	SetRoleIn(ctx context.Context, subject, username, role string, projectID *int64, grantedBy string) error
	RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error
	SetGroupRoleIn(ctx context.Context, group, role string, projectID *int64, grantedBy string) error
	ClearGroupRoleIn(ctx context.Context, group string, projectID *int64) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	Auditor
}

// Access grants and removes roles.
type Access struct {
	Store  AccessStore
	Logger *slog.Logger
}

// scope resolves a project's slug; empty is the whole instance.
func (a Access) scope(ctx context.Context, project string) (*int64, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, nil
	}
	found, err := a.Store.ProjectBySlug(ctx, project)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			return nil, &Problem{Status: http.StatusNotFound, Title: "Unknown project", Detail: project}
		}
		return nil, err
	}
	return &found.ID, nil
}

func role(value string, project string) (auth.Role, error) {
	parsed, err := auth.ParseRole(value)
	if err != nil {
		return "", &Problem{Status: http.StatusBadRequest, Title: "Unknown role", Detail: err.Error()}
	}
	if parsed == auth.RoleAdmin && strings.TrimSpace(project) != "" {
		return "", &Problem{Status: http.StatusUnprocessableEntity, Title: "Impossible scope",
			Detail: "the administrator role governs the accounts and the repositories, which belong to no project"}
	}
	return parsed, nil
}

// SetPerson grants a person a role, everywhere or in a project. Their sessions are closed: the
// change applies at once.
func (a Access) SetPerson(ctx context.Context, actor, subject, username, value, project string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return &Problem{Status: http.StatusBadRequest, Title: "Subject required",
			Detail: "the identity provider's identifier, not the displayed name"}
	}
	granted, err := role(value, project)
	if err != nil {
		return err
	}
	projectID, err := a.scope(ctx, project)
	if err != nil {
		return err
	}
	if err := a.Store.SetRoleIn(ctx, subject, strings.TrimSpace(username), string(granted), projectID, actor); err != nil {
		return accessProblem(err)
	}
	audit(ctx, loggerOr(a.Logger), a.Store, actor, store.AuditRoleGrant, subject, map[string]any{
		"role": string(granted), "username": username, "project": strings.TrimSpace(project),
	})
	return nil
}

// ClearPerson removes a person's role at one scope.
func (a Access) ClearPerson(ctx context.Context, actor, subject, project string) error {
	projectID, err := a.scope(ctx, project)
	if err != nil {
		return err
	}
	if err := a.Store.RevokeRoleIn(ctx, subject, projectID); err != nil {
		return accessProblem(err)
	}
	audit(ctx, loggerOr(a.Logger), a.Store, actor, store.AuditRoleRevoke, subject, map[string]any{
		"project": strings.TrimSpace(project),
	})
	return nil
}

// SetGroup gives a group a role, everywhere or in a project. It applies to its members' next
// request.
func (a Access) SetGroup(ctx context.Context, actor, group, value, project string) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return &Problem{Status: http.StatusBadRequest, Title: "Group required"}
	}
	granted, err := role(value, project)
	if err != nil {
		return err
	}
	projectID, err := a.scope(ctx, project)
	if err != nil {
		return err
	}
	if err := a.Store.SetGroupRoleIn(ctx, group, string(granted), projectID, actor); err != nil {
		return err
	}
	audit(ctx, loggerOr(a.Logger), a.Store, actor, store.AuditGroupGrant, group, map[string]any{
		"role": string(granted), "project": strings.TrimSpace(project),
	})
	return nil
}

// ClearGroup removes a group's role at one scope.
func (a Access) ClearGroup(ctx context.Context, actor, group, project string) error {
	projectID, err := a.scope(ctx, project)
	if err != nil {
		return err
	}
	if err := a.Store.ClearGroupRoleIn(ctx, group, projectID); err != nil {
		return accessProblem(err)
	}
	audit(ctx, loggerOr(a.Logger), a.Store, actor, store.AuditGroupRevoke, group, map[string]any{
		"project": strings.TrimSpace(project),
	})
	return nil
}

func accessProblem(err error) error {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		// 409 and not 403: the request is legitimate and the caller has the right, it is the
		// system's state that forbids it.
		return &Problem{Status: http.StatusConflict, Title: "Last administrator", Detail: err.Error()}
	case errors.Is(err, store.ErrRoleNotFound):
		return &Problem{Status: http.StatusNotFound, Title: "Role not found"}
	}
	return err
}
