package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// A plugin's migrations keep their own history: applied after the core's, read back by their own
// probe, and never mistaken for the core's.
func TestAMigrationSetKeepsItsOwnHistory(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)
	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatal(err)
	}
	set := MigrationSet{Name: "test plugin", Table: "goose_test_db_version", Files: fstest.MapFS{
		"0001_widgets.sql": {Data: []byte("-- +goose Up\ncreate table widgets (id bigint primary key);\n-- +goose Down\ndrop table widgets;\n")},
		"0002_colour.sql":  {Data: []byte("-- +goose Up\nalter table widgets add column colour text;\n-- +goose Down\nalter table widgets drop column colour;\n")},
	}}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := set.CheckSchema(db.Pool())(ctx); err == nil {
		t.Error("a set never applied passes its probe")
	}

	if err := set.Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if pending, err := set.Pending(ctx, cfg); err != nil || len(pending) != 0 {
		t.Errorf("pending %v, %v", pending, err)
	}
	if err := set.CheckSchema(db.Pool())(ctx); err != nil {
		t.Errorf("probe: %v", err)
	}
	if version, err := db.SchemaVersion(ctx); err != nil || version < 60 {
		t.Errorf("the core's version read %d, %v", version, err)
	}

	if err := set.DownTo(ctx, cfg, 1); err != nil {
		t.Fatal(err)
	}
	if err := set.CheckSchema(db.Pool())(ctx); !errors.Is(err, ErrSchemaOutdated) {
		t.Errorf("probe after rolling back: %v", err)
	}

	for _, table := range []string{gooseVersionTable, "widgets; drop table jobs"} {
		if err := (MigrationSet{Name: "bad", Table: table, Files: set.Files}).Migrate(ctx, cfg, discardLogger()); err == nil {
			t.Errorf("version table %q accepted", table)
		}
	}
}

// A purge undoes every migration, then drops the version table; one migration that undoes nothing
// refuses it before anything is undone.
func TestAPurgeUndoesTheSetAndDropsItsVersionTable(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)
	ctx := context.Background()
	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exists := func(table string) bool {
		var found bool
		if err := db.Pool().QueryRow(ctx, `select to_regclass($1) is not null`, table).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}

	files := fstest.MapFS{
		"0001_widgets.sql": {Data: []byte("-- +goose Up\ncreate table widgets (id bigint primary key);\n-- +goose Down\n-- the table, and its rows\ndrop table widgets;\n")},
		"0002_colour.sql":  {Data: []byte("-- +goose Up\nalter table widgets add column colour text;\n-- +goose Down\n-- nothing to undo\n")},
	}
	set := MigrationSet{Name: "test plugin", Table: "goose_test_db_version", Files: files}
	if err := set.Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if err := set.Purge(ctx, cfg, discardLogger()); err == nil || !strings.Contains(err.Error(), "0002_colour.sql") {
		t.Fatalf("a migration undoing nothing: %v", err)
	}
	if !exists("widgets") || !exists("goose_test_db_version") {
		t.Fatal("a refused purge undid something")
	}

	files["0002_colour.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nalter table widgets add column colour text;\n-- +goose Down\nalter table widgets drop column colour;\n")}
	if err := set.Purge(ctx, cfg, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if exists("widgets") || exists("goose_test_db_version") {
		t.Error("the purge left the tables")
	}
}
