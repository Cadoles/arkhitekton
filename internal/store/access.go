package store

import (
	"context"
	"fmt"
	"time"
)

/*
 * Who may use the dashboard: the accounts that signed in, and the roles given to groups.
 *
 * An account is remembered at every sign-in, with or without a role: a person waiting for one must
 * be listable. A group is one the identity provider puts in the token, a forge's organisation or
 * team: its role applies to its members, resolved at each request (internal/auth.Resolve).
 */

// Account is a person who signed in at least once.
type Account struct {
	Subject   string
	Username  string
	Email     string
	Groups    []string
	FirstSeen time.Time
	LastSeen  time.Time
	// Sessions is the number of active sessions.
	Sessions int
}

// ListAccounts returns the accounts that signed in, most recently seen first.
func (db *DB) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := db.pool.Query(ctx,
		`select a.subject, coalesce(a.username, ''), coalesce(a.email, ''), a.groups,
		        a.first_seen, a.last_seen,
		        (select count(*) from sessions s where s.subject = a.subject and s.expires_at > now())
		 from accounts a
		 order by a.last_seen desc, a.subject`)
	if err != nil {
		return nil, fmt.Errorf("listing the accounts: %w", err)
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var account Account
		if err := rows.Scan(&account.Subject, &account.Username, &account.Email, &account.Groups,
			&account.FirstSeen, &account.LastSeen, &account.Sessions); err != nil {
			return nil, fmt.Errorf("listing the accounts: %w", err)
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// GroupRole is a role given to a group, everywhere or in one project.
type GroupRole struct {
	Group string
	Role  string
	// ProjectID is nil for the whole instance; ProjectSlug and ProjectLabel name the project.
	ProjectID    *int64
	ProjectSlug  string
	ProjectLabel string
	GrantedBy    string
	GrantedAt    time.Time
}

// ListGroupRoles returns the roles given to groups, by group, the instance's role first.
func (db *DB) ListGroupRoles(ctx context.Context) ([]GroupRole, error) {
	rows, err := db.pool.Query(ctx,
		`select g.group_name, g.role, g.project_id, coalesce(p.slug, ''), coalesce(p.label, ''),
		        g.granted_by, g.granted_at
		 from group_roles g
		 left join projects p on p.id = g.project_id
		 order by g.group_name, (g.project_id is not null), coalesce(p.label, '')`)
	if err != nil {
		return nil, fmt.Errorf("listing the group roles: %w", err)
	}
	defer rows.Close()

	var roles []GroupRole
	for rows.Next() {
		var role GroupRole
		if err := rows.Scan(&role.Group, &role.Role, &role.ProjectID, &role.ProjectSlug,
			&role.ProjectLabel, &role.GrantedBy, &role.GrantedAt); err != nil {
			return nil, fmt.Errorf("listing the group roles: %w", err)
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// SetGroupRoleIn gives a group a role in a project, or everywhere when projectID is nil.
//
// No session is closed: a group's role is read at each request, so the change applies to the
// next one.
func (db *DB) SetGroupRoleIn(ctx context.Context, group, role string, projectID *int64, grantedBy string) error {
	if group == "" {
		return fmt.Errorf("a group role needs a group")
	}
	if role == "admin" && projectID != nil {
		return fmt.Errorf("role %q is not granted per project", role)
	}
	conflict := "(group_name) where project_id is null"
	if projectID != nil {
		conflict = "(group_name, project_id) where project_id is not null"
	}
	_, err := db.pool.Exec(ctx,
		`insert into group_roles (group_name, role, project_id, granted_by, granted_at)
		 values ($1, $2, $3, $4, now())
		 on conflict `+conflict+` do update
		 set role = excluded.role, granted_by = excluded.granted_by, granted_at = excluded.granted_at`,
		group, role, projectID, grantedBy)
	if err != nil {
		return fmt.Errorf("giving %q the role %q: %w", group, role, err)
	}
	return nil
}

// ClearGroupRoleIn removes a group's role at one scope.
func (db *DB) ClearGroupRoleIn(ctx context.Context, group string, projectID *int64) error {
	tag, err := db.pool.Exec(ctx,
		`delete from group_roles
		 where group_name = $1 and project_id is not distinct from $2`, group, projectID)
	if err != nil {
		return fmt.Errorf("removing the role of %q: %w", group, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRoleNotFound
	}
	return nil
}
