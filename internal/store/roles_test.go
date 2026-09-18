package store

import (
	"context"
	"errors"
	"testing"
)

func grantRole(t *testing.T, db *DB, subject, username, role string) {
	t.Helper()
	if err := db.SetRole(context.Background(), subject, username, role, "vcarroy"); err != nil {
		t.Fatalf("granting to %s: %v", subject, err)
	}
}

func openSession(t *testing.T, db *DB, subject string) {
	t.Helper()
	_, err := db.pool.Exec(context.Background(),
		`insert into sessions (id, subject, username, expires_at)
		 values ($1, $2, $2, now() + interval '1 hour')`, "sess-"+subject, subject)
	if err != nil {
		t.Fatalf("session of %s: %v", subject, err)
	}
}

func sessionCount(t *testing.T, db *DB, subject string) int {
	t.Helper()
	var count int
	if err := db.pool.QueryRow(context.Background(),
		`select count(*) from sessions where subject = $1`, subject).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

// Removing a role without closing the sessions lets the user exercise the old one until
// expiry. A demotion that does not take effect is worse than no demotion: one believes one
// has acted.
func TestRevokeClosesTheSessions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "alice", "alice", "admin")
	grantRole(t, db, "bob", "bob", "operator")
	openSession(t, db, "bob")

	if sessionCount(t, db, "bob") != 1 {
		t.Fatal("the initial session is missing")
	}

	if err := db.RevokeRole(ctx, "bob"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if sessionCount(t, db, "bob") != 0 {
		t.Error("the sessions must be closed with the role")
	}

	roles, _ := db.ListUserRoles(ctx)
	for _, role := range roles {
		if role.Subject == "bob" {
			t.Error("the role remains")
		}
	}
}

// A role change closes the sessions too, whichever way it goes: telling promotion from
// demotion would make forgetting cost on the wrong side.
func TestSetRoleClosesTheSessions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "bob", "bob", "operator")
	openSession(t, db, "bob")

	if err := db.SetRole(ctx, "bob", "bob", "viewer", "alice"); err != nil {
		t.Fatalf("demotion: %v", err)
	}
	if sessionCount(t, db, "bob") != 0 {
		t.Error("the sessions must be closed when the role changes")
	}
}

// Without that guard, an administrator removes themselves and leaves the dashboard with
// nobody able to grant anything: the only way out would be to edit the bootstrap
// configuration and restart.
func TestTheLastAdminCannotBeRemoved(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "alice", "alice", "admin")

	if err := db.RevokeRole(ctx, "alice"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("error = %v, expected ErrLastAdmin", err)
	}
	// Ni par retrait, ni par rétrogradation.
	if err := db.SetRole(ctx, "alice", "alice", "viewer", "alice"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demotion = %v, expected ErrLastAdmin", err)
	}

	// The role is still there: the refusal left nothing half done.
	admins, err := db.CountAdmins(ctx)
	if err != nil || admins != 1 {
		t.Errorf("administrateurs = %d, err = %v", admins, err)
	}
}

// Two administrators: removing one is legitimate.
func TestPenultimateAdminCanBeRemoved(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "alice", "alice", "admin")
	grantRole(t, db, "carol", "carol", "admin")

	if err := db.RevokeRole(ctx, "carol"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	admins, _ := db.CountAdmins(ctx)
	if admins != 1 {
		t.Errorf("administrators = %d, expected 1", admins)
	}
}

func TestRevokeAnUnknownSubject(t *testing.T) {
	db := migrated(t)

	if err := db.RevokeRole(context.Background(), "unknown"); !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("error = %v, expected ErrRoleNotFound", err)
	}
}

// The list serves to check who can ship: the most privileged first, with no manual sorting.
func TestRolesAreListedByPrivilege(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "v", "victor", "viewer")
	grantRole(t, db, "a", "alice", "admin")
	grantRole(t, db, "o", "olivia", "operator")
	grantRole(t, db, "r", "remi", "releaser")

	roles, err := db.ListUserRoles(ctx)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	order := make([]string, 0, len(roles))
	for _, role := range roles {
		order = append(order, role.Role)
	}
	expected := []string{"admin", "releaser", "operator", "viewer"}
	for index := range expected {
		if order[index] != expected[index] {
			t.Errorf("order = %v, expected %v", order, expected)
			break
		}
	}
}

// A role granted to a wrong identifier shows only by this: nobody ever used it.
func TestNeverSeenRoleIsRecognisable(t *testing.T) {
	db := migrated(t)

	grantRole(t, db, "typo", "", "operator")

	roles, _ := db.ListUserRoles(context.Background())
	for _, role := range roles {
		if role.Subject != "typo" {
			continue
		}
		if role.LastSeen != nil {
			t.Error("a role never used must carry no usage date")
		}
		if role.Sessions != 0 {
			t.Errorf("sessions = %d", role.Sessions)
		}
		return
	}
	t.Error("role not found")
}
