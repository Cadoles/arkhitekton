package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// UserRole is a granted role.
type UserRole struct {
	Subject  string
	Username string
	Role     string
	// ProjectID is the scope: nil when the role holds everywhere. One person may
	// therefore appear several times in the list — one global row, one per project where
	// an exception was set.
	ProjectID *int64
	// ProjectSlug and ProjectLabel name that scope for display. Empty for a global role.
	ProjectSlug  string
	ProjectLabel string
	// GrantedBy is `bootstrap` for accounts promoted by configuration at start-up. The
	// distinction matters: those roles reappear at restart if they are removed without
	// touching the configuration.
	GrantedBy string
	GrantedAt time.Time
	// LastSeen is the last time the role was used. Nil for an authorised account that
	// never signed in — a role granted to a wrong identifier is recognised this way.
	LastSeen *time.Time
	// Sessions is the number of active sessions. A revocation closing none would leave
	// the old role in force until expiry.
	Sessions int
}

// ErrRoleNotFound reports a missing role.
var ErrRoleNotFound = errors.New("role not found")

// ErrLastAdmin refuses the removal of the last administrator.
var ErrLastAdmin = errors.New(
	"last administrator: removing them would leave the dashboard with nobody able to grant a role")

// ListUserRoles returns the granted roles, with their active sessions.
func (db *DB) ListUserRoles(ctx context.Context) ([]UserRole, error) {
	rows, err := db.pool.Query(ctx,
		`select r.subject, coalesce(r.username, ''), r.role, r.granted_by, r.granted_at,
		        r.last_seen,
		        (select count(*) from sessions s
		         where s.subject = r.subject and s.expires_at > now()),
		        r.project_id, coalesce(p.slug, ''), coalesce(p.label, '')
		 from user_roles r
		 left join projects p on p.id = r.project_id
		 -- Global roles first, then project ones: one person may now appear
		 -- several times, and reading their general scope before their
		 -- exceptions is the order one reasons in.
		 --
		 -- The most privileged at the head of each group: this is the list read
		 -- to check who can ship.
		 order by (r.project_id is not null), coalesce(p.label, ''),
		     case r.role
		     when 'admin' then 0 when 'releaser' then 1
		     when 'operator' then 2 else 3 end,
		     coalesce(r.username, r.subject)`)
	if err != nil {
		return nil, fmt.Errorf("listing the roles: %w", err)
	}
	defer rows.Close()

	var roles []UserRole
	for rows.Next() {
		var role UserRole
		if err := rows.Scan(&role.Subject, &role.Username, &role.Role,
			&role.GrantedBy, &role.GrantedAt, &role.LastSeen, &role.Sessions,
			&role.ProjectID, &role.ProjectSlug, &role.ProjectLabel); err != nil {
			return nil, fmt.Errorf("listing the roles: %w", err)
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// CountAdmins returns the number of administrators.
func (db *DB) CountAdmins(ctx context.Context) (int, error) {
	var count int
	// Global ones only: `admin` exists nowhere else — the database enforces it — but the
	// condition is explicit rather than inferred from a constraint.
	err := db.pool.QueryRow(ctx,
		`select count(*) from user_roles where role = 'admin' and project_id is null`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting the administrators: %w", err)
	}
	return count, nil
}

// RevokeRole removes a user's role and closes their sessions.
//
// Both in one transaction, and that is the point: removing a role without closing the
// sessions lets the user exercise the old one until expiry. A demotion that does not
// take effect is worse than no demotion at all — one believes one has acted.
//
// It refuses to remove the last administrator. Without that guard, an administrator
// can remove themselves and leave the dashboard with nobody able to grant anything:
// the only way out would be to edit the bootstrap configuration and restart.
func (db *DB) RevokeRole(ctx context.Context, subject string) error {
	return db.RevokeRoleIn(ctx, subject, nil)
}

// RevokeRoleIn removes the role of one scope: a project's, or the global one.
//
// The scope matters in the reasoning even when it is nil: a removal deleting every
// row of a subject would, while meaning to remove one project, delete the global role
// that holds everywhere else.
func (db *DB) RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("removing the role of %q: %w", subject, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var role string
	var err2 error
	if projectID == nil {
		err2 = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id is null for update`,
			subject).Scan(&role)
	} else {
		err2 = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id = $2 for update`,
			subject, *projectID).Scan(&role)
	}
	if err2 != nil {
		return ErrRoleNotFound
	}

	if role == "admin" {
		var admins int
		if err := transaction.QueryRow(ctx,
			// `admin` exists only globally — the database enforces it — but the condition is
			// explicit: inadvertently counting a project role would allow removing the last real
			// administrator.
			`select count(*) from user_roles where role = 'admin' and project_id is null`).Scan(&admins); err != nil {
			return fmt.Errorf("removing the role of %q: %w", subject, err)
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}

	if projectID == nil {
		_, err = transaction.Exec(ctx,
			`delete from user_roles where subject = $1 and project_id is null`, subject)
	} else {
		_, err = transaction.Exec(ctx,
			`delete from user_roles where subject = $1 and project_id = $2`, subject, *projectID)
	}
	if err != nil {
		return fmt.Errorf("removing the role of %q: %w", subject, err)
	}
	if _, err := transaction.Exec(ctx, `delete from sessions where subject = $1`, subject); err != nil {
		return fmt.Errorf("closing the sessions of %q: %w", subject, err)
	}

	return transaction.Commit(ctx)
}

// SetRole grants a role and closes the person's sessions.
//
// Sessions are closed whichever way the change goes. A promotion could do without —
// the role is re-read on every request — but telling the two cases apart would mean
// comparing ranks here, and forgetting would be paid on a demotion, that is, on the
// wrong side.
//
// It refuses to demote the last administrator, for the same reason as
// [DB.RevokeRole].
func (db *DB) SetRole(ctx context.Context, subject, username, role, grantedBy string) error {
	return db.SetRoleIn(ctx, subject, username, role, nil, grantedBy)
}

// SetRoleIn grants a role in a project, or everywhere when it is nil.
func (db *DB) SetRoleIn(
	ctx context.Context, subject, username, role string, projectID *int64, grantedBy string,
) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("granting the role to %q: %w", subject, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// `admin` governs accounts, repositories and synchronisation — none of which belongs
	// to a project. The database refuses it too; saying it here makes the message
	// readable.
	if role == "admin" && projectID != nil {
		return fmt.Errorf("role %q is not granted per project", role)
	}

	// The current role of the same scope. Reading the global role while granting a
	// project role would suggest a demotion that is not happening, and would trigger the
	// last-administrator guard on a grant that does not touch the global role.
	var current string
	if projectID == nil {
		err = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id is null for update`,
			subject).Scan(&current)
	} else {
		err = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id = $2 for update`,
			subject, *projectID).Scan(&current)
	}
	known := err == nil

	if known && current == "admin" && role != "admin" {
		var admins int
		if err := transaction.QueryRow(ctx,
			// `admin` exists only globally — the database enforces it — but the condition is
			// explicit: inadvertently counting a project role would allow removing the last real
			// administrator.
			`select count(*) from user_roles where role = 'admin' and project_id is null`).Scan(&admins); err != nil {
			return fmt.Errorf("granting the role to %q: %w", subject, err)
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}

	// The clause targets the partial index of the scope aimed at: the two uniqueness
	// rules are carried by distinct partial indexes, and `on conflict` requires naming
	// the one it targets.
	conflict := "(subject) where project_id is null"
	if projectID != nil {
		conflict = "(subject, project_id) where project_id is not null"
	}

	_, err = transaction.Exec(ctx,
		`insert into user_roles (subject, username, role, project_id, granted_by, granted_at)
		 values ($1, nullif($2, ''), $3, $4, $5, now())
		 on conflict `+conflict+` do update
		 set role = excluded.role, granted_by = excluded.granted_by,
		     granted_at = excluded.granted_at,
		     username = coalesce(excluded.username, user_roles.username)`,
		subject, username, role, projectID, grantedBy)
	if err != nil {
		return fmt.Errorf("granting the role to %q: %w", subject, err)
	}

	// The role is re-read on every request, but the session carries the identity: closing
	// it forces a new sign-in and makes the change immediate and visible.
	if _, err := transaction.Exec(ctx, `delete from sessions where subject = $1`, subject); err != nil {
		return fmt.Errorf("closing the sessions of %q: %w", subject, err)
	}

	return transaction.Commit(ctx)
}
