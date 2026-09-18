package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// These tests require a real PostgreSQL: applying a schema cannot usefully be simulated,
// and it is precisely the execution of the SQL that is being checked.
//
// They are driven by TEST_DATABASE_URL rather than by testcontainers: one dependency
// fewer is worth the manual setup, and CI already provides a PostgreSQL service. Without
// the variable the tests are skipped — visibly, not silently.
func testDB(t *testing.T) config.DatabaseConfig {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: integration test skipped")
	}
	return config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}
}

// resetSchema returns the database to nothing between two tests: goose remembers the
// applied version, and a test inheriting the previous one's state proves nothing.
func resetSchema(t *testing.T, cfg config.DatabaseConfig) {
	t.Helper()

	ctx := context.Background()
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	if _, err := db.Pool().Exec(ctx, "drop schema public cascade; create schema public;"); err != nil {
		t.Fatalf("resetting the schema: %v", err)
	}
}

func TestOpenRejectsUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// pgxpool is lazy: without Open's Ping, this configuration would produce a pool that
	// looks usable and a failure at the first query, far from its cause.
	_, err := Open(ctx, config.DatabaseConfig{
		DSN:      "postgres://nobody:nothing@127.0.0.1:1/absent",
		MaxConns: 1,
	})
	if err == nil {
		t.Fatal("an unreachable database must fail Open")
	}
}

func TestMigrateAppliesEverySchemaFile(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	state, err := MigrationState(ctx, cfg)
	if err != nil {
		t.Fatalf("migration state: %v", err)
	}
	if len(state) == 0 {
		t.Fatal("no migration collected")
	}
	for _, migration := range state {
		if !migration.Applied {
			t.Errorf("%s not applied", migration.Name)
		}
	}
}

// The tables and constraints that carry hard decisions must exist after migration:
// checking them here keeps a typo in the SQL from being discovered only when writing the
// query that depends on it.
func TestMigrateCreatesKeyConstraints(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	for _, table := range []string{
		"versions", "issues", "releases", "release_images",
		"jobs", "runs", "task_runs",
		"test_results", "test_cases", "artifacts",
		"user_roles", "sessions", "audit_log", "sync_state",
	} {
		var exists bool
		err := db.Pool().QueryRow(ctx,
			"select exists (select 1 from information_schema.tables where table_name = $1)",
			table).Scan(&exists)
		if err != nil {
			t.Fatalf("querying the catalog: %v", err)
		}
		if !exists {
			t.Errorf("table %s missing after migration", table)
		}
	}
}

// A migration that cannot undo itself is discovered at the worst moment. The test applies
// then rolls everything back, which exercises every `-- +goose Down` block.

func TestMigrationsRollBackCleanly(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("reading the migrations: %v", err)
	}
	for i := range names {
		if err := Rollback(ctx, cfg, discardLogger()); err != nil {
			t.Fatalf("annulation %d/%d: %v", i+1, len(names), err)
		}
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer db.Close()

	var remaining int
	if err := db.Pool().QueryRow(ctx,
		"select count(*) from information_schema.tables where table_schema = 'public' and table_name <> 'goose_db_version'").
		Scan(&remaining); err != nil {
		t.Fatalf("querying the catalog: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d table(s) remain after a full rollback", remaining)
	}
}

// The DSN carries the password and pgx readily quotes it in its errors, which end up in
// logs, in tickets, in screenshots.
func TestRedactErrorHidesPasswordKeepsMessage(t *testing.T) {
	dsn := "postgres://mse:s3cr3t@db.example.fr:5432/mse"
	err := redactError(
		errorString("connection failure with postgres://mse:s3cr3t@db.example.fr:5432/mse"), dsn)

	if got := err.Error(); strings.Contains(got, "s3cr3t") {
		t.Errorf("the password leaks: %s", got)
	}
	if got := err.Error(); !strings.Contains(got, "db.example.fr") {
		t.Errorf("the message lost the host, it can no longer be diagnosed: %s", got)
	}
}

func TestRedactErrorPassesThroughWhenNoPassword(t *testing.T) {
	original := errorString("connection refused")
	if got := redactError(original, "postgres://mse@db:5432/mse"); got != original {
		t.Errorf("error needlessly rewritten: %v", got)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// Migration is an initialisation step: its Job starts at the same time as the database.
// Without a wait it fails, Kubernetes restarts it, and every deployment leaves a pod in
// `Error` — a residue that teaches people to ignore failed pods.
func TestWaitForDatabaseSucceedsOnAnAvailableDatabase(t *testing.T) {
	cfg := testDB(t)

	if err := WaitForDatabase(context.Background(), cfg, 5*time.Second, discardLogger()); err != nil {
		t.Errorf("error = %v, expected none on an available database", err)
	}
}

// The message must carry the number of attempts and the duration: "connection refused"
// alone does not say whether the database is absent or slow to start.
func TestWaitForDatabaseGivesUpWithACountedMessage(t *testing.T) {
	cfg := config.DatabaseConfig{
		DSN:      "postgres://nobody:nothing@127.0.0.1:1/absent?sslmode=disable",
		MaxConns: 1,
	}

	start := time.Now()
	err := WaitForDatabase(context.Background(), cfg, 3*time.Second, discardLogger())
	if err == nil {
		t.Fatal("an unreachable database must eventually fail")
	}
	if !strings.Contains(err.Error(), "attempt") {
		t.Errorf("message = %q, expected the number of attempts", err)
	}
	// The wait is bounded: a Job waiting indefinitely cannot be told from a stuck Job.
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("gave up after %s, the bound is not honoured", elapsed)
	}
}

// The password must not leak into the failure message, which goes to the log and then
// into Kubernetes events.
func TestWaitForDatabaseRedactsTheDSN(t *testing.T) {
	cfg := config.DatabaseConfig{
		DSN:      "postgres://dashboard:tressecret@127.0.0.1:1/absent?sslmode=disable",
		MaxConns: 1,
	}

	err := WaitForDatabase(context.Background(), cfg, 2*time.Second, discardLogger())
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "tressecret") {
		t.Errorf("the password appears in %q", err)
	}
}

// Cancellation is honoured without waiting for the bound: an interrupted Job must return
// at once.
func TestWaitForDatabaseHonoursCancellation(t *testing.T) {
	cfg := config.DatabaseConfig{
		DSN:      "postgres://nobody:nothing@127.0.0.1:1/absent?sslmode=disable",
		MaxConns: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := WaitForDatabase(ctx, cfg, time.Minute, discardLogger()); err == nil {
		t.Fatal("expected failure")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("cancellation not honoured: %s", elapsed)
	}
}

// A migrated database has nothing pending.
//
// That is the exit condition of [WaitForSchema]: without it, the api and the worker would
// wait indefinitely for a schema that is in fact complete.
func TestUneBaseMigreeNaPlusDeMigrationEnAttente(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	pending, err := PendingMigrations(ctx, cfg)
	if err != nil {
		t.Fatalf("migrations en attente: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("%d migration(s) pending after migrating: %v", len(pending), pending)
	}

	// And the wait returns at once.
	if err := WaitForSchema(ctx, cfg, 5*time.Second, discardLogger()); err != nil {
		t.Errorf("waiting on an up-to-date schema: %v", err)
	}
}

// An empty database is behind, and the wait says so by naming what is missing.
//
// The real case: the migration Job and the application pod start together, and nothing
// guarantees the order. Without this wait the worker reconciled against an incomplete
// schema.
func TestUneBaseNonMigreeEstSignaleeEnRetard(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	pending, err := PendingMigrations(ctx, cfg)
	if err != nil {
		t.Fatalf("migrations en attente: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("an empty database must have pending migrations")
	}

	// The wait fails on a short bound, and its message names a missing migration: "schema
	// behind" alone would send someone to read the Job's logs without knowing what to look
	// for.
	err = WaitForSchema(ctx, cfg, 3*time.Second, discardLogger())
	if err == nil {
		t.Fatal("the wait must fail on an unmigrated database")
	}
	if !strings.Contains(err.Error(), pending[0]) {
		t.Errorf("message %q — it must name a pending migration", err)
	}
}
