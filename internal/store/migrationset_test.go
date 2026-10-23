package store

import (
	"context"
	"errors"
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
