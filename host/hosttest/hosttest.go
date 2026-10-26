// Package hosttest is what an installed plugin's tests borrow from the dashboard: a throwaway
// database with the dashboard's schema and the plugin's migrations applied as the dashboard applies
// them, its catalogue as an instance reads it, and its parsers and interpreters, so that a reading
// is checked as the dashboard will run it.
package hosttest

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parsers"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// Logger discards what it is given.
func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Database is a PostgreSQL the test may empty, named by TEST_DATABASE_URL.
type Database struct {
	Pool   *pgxpool.Pool
	config config.DatabaseConfig
	core   *store.DB
}

// EmptyDatabase returns the database emptied of everything, or skips the test when
// TEST_DATABASE_URL names none: the queries carry joins and constraints a fake would not check.
func EmptyDatabase(t *testing.T) *Database {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: integration test skipped")
	}
	cfg := config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Pool().Exec(ctx, "drop schema public cascade; create schema public;"); err != nil {
		t.Fatalf("resetting the schema: %v", err)
	}
	return &Database{Pool: db.Pool(), config: cfg, core: db}
}

// MigrateCore applies the dashboard's migrations, which a plugin's follow.
func (d *Database) MigrateCore(t *testing.T) {
	t.Helper()
	if err := store.Migrate(context.Background(), d.config, Logger()); err != nil {
		t.Fatalf("the dashboard's migrations: %v", err)
	}
}

// Release creates a release of a project, its version included, and returns its id.
func (d *Database) Release(t *testing.T, projectID int64, tag, imageVersion string) int64 {
	t.Helper()
	ctx := context.Background()
	versionID, err := d.core.EnsureVersion(ctx, projectID, "1.8.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	id, err := d.core.CreateRelease(ctx, store.Release{
		ProjectID: projectID, VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: imageVersion, CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
	return id
}

// Migrations are a plugin's, as its manifest declares them: the files of its `migrations.dir`, its
// version table. The dashboard applies them when the plugin is installed; a test applies them
// here, the same way.
type Migrations struct {
	Plugin string
	Table  string
	Files  fs.FS
}

func (m Migrations) set() store.MigrationSet {
	return store.MigrationSet{Name: m.Plugin, Table: m.Table, Files: m.Files}
}

// Migrate applies them all.
func (m Migrations) Migrate(ctx context.Context, d *Database) error {
	return m.set().Migrate(ctx, d.config, Logger())
}

// Rollback undoes the last one applied.
func (m Migrations) Rollback(ctx context.Context, d *Database) error {
	return m.set().Rollback(ctx, d.config)
}

// UpTo applies them up to a version, to seed rows a later migration rewrites.
func (m Migrations) UpTo(ctx context.Context, d *Database, version int64) error {
	return m.set().UpTo(ctx, d.config, version)
}

// DownTo undoes them down to a version.
func (m Migrations) DownTo(ctx context.Context, d *Database, version int64) error {
	return m.set().DownTo(ctx, d.config, version)
}

// CheckSchema is the readiness probe the dashboard runs for the plugin, `schema-<id>`.
func (m Migrations) CheckSchema(ctx context.Context, d *Database) error {
	return m.set().CheckSchema(d.Pool)(ctx)
}

// Pending lists those not applied yet.
func (m Migrations) Pending(ctx context.Context, d *Database) ([]string, error) {
	return m.set().Pending(ctx, d.config)
}

// Record parses a report as the dashboard does for a member declared with this parser.
func Record(tool string, parser parse.ID, raw []byte) parse.Record {
	parsing, _ := parsers.Lookup(parser)
	payload, err := parsing(raw)
	return parse.Record{Tool: tool, Parser: parser, Object: "raw/run/" + tool, Payload: payload, Err: err}
}

// Interpret reads a run with one of a plugin's pipelines as the dashboard does, its declared
// reading or module compiled, and checks the facts it returns.
func Interpret(t *testing.T, plugin capability.Plugin, pipeline string, run capability.RunState, records ...parse.Record) []facts.Set {
	t.Helper()
	registry, err := capabilities.New(plugin)
	if err != nil {
		t.Fatal(err)
	}
	found, _, ok := registry.Pipeline(capability.Ref{Plugin: plugin.ID, Name: pipeline})
	if !ok || found.Interpreter == nil {
		t.Fatalf("%s/%s reads no run", plugin.ID, pipeline)
	}
	sets, err := found.Interpreter.Interpret(context.Background(), run, records)
	if err != nil {
		t.Fatalf("%s/%s: %v", plugin.ID, pipeline, err)
	}
	for _, set := range sets {
		if err := set.Validate(); err != nil {
			t.Errorf("%s/%s: invalid set: %v", plugin.ID, pipeline, err)
		}
	}
	return sets
}

// Catalogue loads a catalogue file as an instance with these plugins installed reads it: against
// the built-in plugins and theirs.
func Catalogue(t *testing.T, path string, installed ...capability.Plugin) *catalog.Catalog {
	t.Helper()
	plugins := append(builtin.Plugins(), installed...)
	registry, err := capabilities.New(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := catalog.Load(path, catalog.WithCapabilities(registry))
	if err != nil {
		t.Fatalf("catalogue %s: %v", path, err)
	}
	return loaded
}

// CheckReading reads a recorded case with a declared reading, as `arkhitekton check-reading`
// does: the facts produced, and those the case expects.
func CheckReading(reading []byte, dir string) (got, expected []byte, err error) {
	return declarative.Check(reading, dir)
}

// SameFacts says whether two serialised fact lists say the same.
func SameFacts(got, expected []byte) bool { return declarative.Same(got, expected) }
