package store

import (
	"context"
	"errors"
	"testing"
)

func TestExpectedSchemaVersionFollowsTheLastMigration(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatalf("reading the migrations: %v", err)
	}

	version, err := ExpectedSchemaVersion()
	if err != nil {
		t.Fatalf("expected version: %v", err)
	}
	if version != int64(len(names)) {
		// Migrations are numbered without gaps from 0001: if that ever stops being true, this test
		// is what should be revisited, not the probe.
		t.Errorf("expected version = %d, %d embedded migration(s)", version, len(names))
	}
}

// The case that motivates the probe: the database answers, but the migration Job has not
// run. Without the distinction the pod would declare itself ready and answer 500 on every
// missing table.
func TestCheckSchemaFailsBeforeMigration(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer db.Close()

	if err := db.CheckSchema(ctx); err == nil {
		t.Fatal("a missing schema must fail the probe")
	}
}

func TestCheckSchemaPassesAfterMigration(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer db.Close()

	if err := db.CheckSchema(ctx); err != nil {
		t.Fatalf("probe failed after a complete migration: %v", err)
	}
}

// A partially applied schema must be refused, and the reason must be recognisable: that is
// what separates "migrations behind" from a connection failure, two causes handled
// oppositely.
func TestCheckSchemaDetectsPartialMigration(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)

	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer db.Close()

	// Simulates a binary newer than the database: the last migration is marked unapplied,
	// without touching the tables.
	if _, err := db.Pool().Exec(ctx,
		"update goose_db_version set is_applied = false where version_id = (select max(version_id) from goose_db_version)"); err != nil {
		t.Fatalf("altering goose's state: %v", err)
	}

	err = db.CheckSchema(ctx)
	if !errors.Is(err, ErrSchemaOutdated) {
		t.Fatalf("error = %v, expected ErrSchemaOutdated", err)
	}
}
