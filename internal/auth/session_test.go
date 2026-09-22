package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Sessions and roles live in the database: faking them would prove nothing about
// the queries, which are most of the code under test here.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: integration test skipped")
	}

	// The schema is applied here rather than assumed: the store tests reset the
	// database, and nothing guarantees the order between packages. A package that
	// depends on a schema must know how to establish it.
	if err := store.Migrate(context.Background(), config.DatabaseConfig{DSN: dsn, MaxConns: 2}, discardTestLogger()); err != nil {
		t.Fatalf("migrating the schema: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	t.Cleanup(pool.Close)

	// Each test starts from a clean state: a session inherited from the previous test
	// would make a test pass while proving nothing. `bootstrap_grants` is part of it: a
	// declaration consumed by the previous test would leave the next with nothing to
	// claim, and its failure would point at resolution rather than at isolation.
	if _, err := pool.Exec(context.Background(),
		"truncate sessions, user_roles, bootstrap_grants"); err != nil {
		t.Fatalf("nettoyage: %v", err)
	}
	return pool
}

func TestSessionLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	if err := store.GrantRole(ctx, "sub-1", "alice", RoleOperator, "test"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}

	session, err := store.Create(ctx, "sub-1", "alice", "alice@example.fr")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(session.ID) < 32 {
		t.Errorf("session identifier too short: %q", session.ID)
	}

	found, role, err := store.Lookup(ctx, session.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found.Subject != "sub-1" || found.Username != "alice" || role != RoleOperator {
		t.Errorf("session inattendue: %+v role=%s", found, role)
	}

	if err := store.Revoke(ctx, session.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, err := store.Lookup(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("revoked session still valid: %v", err)
	}
}

// An expired session must not be returnable and then forgotten by a distracted
// caller: the filtering happens in SQL.
func TestLookupRejectsExpiredSession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	store := NewSessionStore(pool, time.Hour)
	current := time.Now()
	store.now = func() time.Time { return current }

	if err := store.GrantRole(ctx, "sub-2", "bob", RoleViewer, "test"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	session, err := store.Create(ctx, "sub-2", "bob", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	current = current.Add(2 * time.Hour)

	if _, _, err := store.Lookup(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

// Authenticated without a role: the user must be identifiable so as to appear
// awaiting a grant, rather than receive an opaque error.
func TestLookupReportsMissingRoleButKeepsIdentity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	session, err := store.Create(ctx, "sub-3", "charlie", "charlie@example.fr")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, role, err := store.Lookup(ctx, session.ID)
	if !errors.Is(err, ErrNoRole) {
		t.Fatalf("expected ErrNoRole, got %v", err)
	}
	if found.Username != "charlie" {
		t.Errorf("identity lost: %+v", found)
	}
	if role != "" {
		t.Errorf("unexpected role: %q", role)
	}
}

// Removing a role, or responding to an incident, must be able to cut all of a
// user's sessions at once.
func TestRevokeSubjectClosesEverySession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	if err := store.GrantRole(ctx, "sub-4", "dana", RoleReleaser, "test"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	first, _ := store.Create(ctx, "sub-4", "dana", "")
	second, _ := store.Create(ctx, "sub-4", "dana", "")

	if err := store.RevokeSubject(ctx, "sub-4"); err != nil {
		t.Fatalf("RevokeSubject: %v", err)
	}

	for _, session := range []Session{first, second} {
		if _, _, err := store.Lookup(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("session %s toujours valide", session.ID)
		}
	}
}

// An account deliberately demoted must not become an administrator again at its
// next sign-in.
func TestBootstrapDoesNotOverrideExistingRole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	if err := store.GrantRole(ctx, "121", "vcarroy", RoleViewer, "admin"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}

	declare(t, pool, "vcarroy", "admin")

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted != "" {
		t.Errorf("an existing role must not be overwritten, role returned = %q", granted)
	}

	session, _ := store.Create(ctx, "121", "vcarroy", "")
	_, role, err := store.Lookup(ctx, session.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if role != RoleViewer {
		t.Errorf("the role was overwritten by the bootstrap: %s", role)
	}
}

// The failure being fixed: the provider puts its numeric identifier in `sub`, where
// a human writes an account name in the configuration. The earlier version created
// a role for the name, the user signed in under the number, and nobody had any
// permission.
func TestBootstrapMatchesTheUsernameNotOnlyTheSubject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "vcarroy", "admin")

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted == "" {
		t.Fatal("a declared account name must promote the matching subject")
	}

	// The role carries the real subject, not the name: it is what will carry
	// authorization on subsequent requests.
	session, _ := store.Create(ctx, "121", "vcarroy", "")
	_, role, err := store.Lookup(ctx, session.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if role != RoleAdmin {
		t.Errorf("role = %s, expected admin", role)
	}
}

// The subject stays accepted: it is immutable, so safer for whoever knows it.
func TestBootstrapAlsoMatchesTheSubject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "121", "admin")

	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil || granted == "" {
		t.Fatalf("granted = %q, err = %v", granted, err)
	}
}

// An account absent from the list gains nothing: otherwise the bootstrap would
// promote the first comer.
func TestBootstrapIgnoresUndeclaredAccounts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "vcarroy", "admin")

	granted, err := store.ApplyBootstrap(ctx, "999", "intrus")
	if err != nil {
		t.Fatalf("ApplyBootstrap: %v", err)
	}
	if granted != "" {
		t.Errorf("an undeclared account must not be promoted, role = %q", granted)
	}

	session, _ := store.Create(ctx, "999", "intrus", "")
	_, role, _ := store.Lookup(ctx, session.ID)
	if role == RoleAdmin {
		t.Error("administrator role granted to an undeclared account")
	}
}

func TestPurgeExpiredRemovesOnlyExpiredSessions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	store := NewSessionStore(pool, time.Hour)
	current := time.Now()
	store.now = func() time.Time { return current }

	old, _ := store.Create(ctx, "sub-5", "eve", "")
	current = current.Add(2 * time.Hour)
	fresh, _ := store.Create(ctx, "sub-6", "frank", "")

	removed, err := store.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 session purged, got %d", removed)
	}

	if _, _, err := store.Lookup(ctx, old.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Error("the expired session remains")
	}
	if _, _, err := store.Lookup(ctx, fresh.ID); !errors.Is(err, ErrNoRole) {
		t.Errorf("the valid session was purged: %v", err)
	}
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// declare writes a bootstrap declaration, as the migration does.
//
// Roles no longer come from a list passed on every sign-in but from a table
// written once: that is what stops an environment variable from granting a role
// indefinitely.
func declare(t *testing.T, pool *pgxpool.Pool, identifier, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`insert into bootstrap_grants (identifier, role) values ($1, $2)
		 on conflict (identifier) do nothing`, identifier, role); err != nil {
		t.Fatalf("declaring %q: %v", identifier, err)
	}
}

// A consumed declaration promotes nobody again.
//
// That is the difference from a configuration re-read on every sign-in: it gave
// whoever could edit it the power to grant themselves a role indefinitely, with no
// trace but a sign-in. A declaration is claimed once.
func TestADeclarationIsClaimedOnlyOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "vcarroy", "admin")

	if granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy"); err != nil || granted == "" {
		t.Fatalf("first claim: %q, %v", granted, err)
	}

	// An administrator demotes the account.
	if err := store.GrantRole(ctx, "121", "vcarroy", RoleViewer, "admin"); err != nil {
		t.Fatalf("demotion: %v", err)
	}

	// Signing in again must not re-promote them.
	granted, err := store.ApplyBootstrap(ctx, "121", "vcarroy")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if granted != "" {
		t.Errorf("role returned = %q: a consumed declaration promoted again", granted)
	}

	session, _ := store.Create(ctx, "121", "vcarroy", "")
	_, role, _ := store.Lookup(ctx, session.ID)
	if role != RoleViewer {
		t.Errorf("role = %s, expected viewer: the demotion was undone", role)
	}
}

// The declared role is the one written, not merely "administrator".
func TestTheDeclaredRoleIsTheOneGranted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	declare(t, pool, "alice", "releaser")

	granted, err := store.ApplyBootstrap(ctx, "204", "alice")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if granted != string(RoleReleaser) {
		t.Fatalf("role returned = %q, expected releaser", granted)
	}

	session, _ := store.Create(ctx, "204", "alice", "")
	_, role, _ := store.Lookup(ctx, session.ID)
	if role != RoleReleaser {
		t.Errorf("role = %s, expected releaser", role)
	}
}

// A global role and a project role coexist for one account.
//
// That is what the two partial indexes guarantee: at most one global role, at most
// one per project. A composite key would have let several global ones through,
// `null` never equalling `null`.
func TestBothScopesCoexist(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	var projectID int64
	if err := pool.QueryRow(ctx,
		// `projects` is not purged between this package's tests — only sessions, roles and
		// declarations are. The insert is therefore idempotent rather than dependent on
		// execution order.
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/theme', 'Theme', 'https://forge.example', '.', 'test')
		 on conflict (repository) do update set label = excluded.label
		 returning id`).Scan(&projectID); err != nil {
		t.Fatalf("projet: %v", err)
	}

	if err := store.GrantRole(ctx, "121", "vcarroy", RoleViewer, "test"); err != nil {
		t.Fatalf("global role: %v", err)
	}
	if err := store.GrantRoleIn(ctx, "121", "vcarroy", RoleReleaser, &projectID, "test"); err != nil {
		t.Fatalf("project role: %v", err)
	}

	session, err := store.Create(ctx, "121", "vcarroy", "v@example.fr")
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	_, global, projects, err := store.LookupRoles(ctx, session.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if global != RoleViewer {
		t.Errorf("global role = %q, expected viewer", global)
	}
	if projects[projectID] != RoleReleaser {
		t.Errorf("project role = %q, expected releaser", projects[projectID])
	}

	// And replacement stays per scope: re-granting the global role does not touch the
	// project one.
	if err := store.GrantRole(ctx, "121", "vcarroy", RoleOperator, "test"); err != nil {
		t.Fatalf("global re-grant: %v", err)
	}
	_, global, projects, err = store.LookupRoles(ctx, session.ID)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if global != RoleOperator || projects[projectID] != RoleReleaser {
		t.Errorf("global = %q, projet = %q", global, projects[projectID])
	}
}

// `admin` is not granted per project: it governs accounts, repositories and
// synchronisation, none of which belongs to a project.
func TestAdminIsNotGrantedPerProject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewSessionStore(pool, time.Hour)

	var projectID int64
	if err := pool.QueryRow(ctx,
		// `projects` is not purged between this package's tests — only sessions, roles and
		// declarations are. The insert is therefore idempotent rather than dependent on
		// execution order.
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/theme', 'Theme', 'https://forge.example', '.', 'test')
		 on conflict (repository) do update set label = excluded.label
		 returning id`).Scan(&projectID); err != nil {
		t.Fatalf("projet: %v", err)
	}

	if err := store.GrantRoleIn(ctx, "121", "vcarroy", RoleAdmin, &projectID, "test"); err == nil {
		t.Error("a project administrator must not be grantable")
	}
}
