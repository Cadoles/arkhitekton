// Package hosttest is what an extension's tests borrow from the dashboard: a throwaway database
// with the dashboard's schema, an authenticated request, the plugins' switches, and its parsers
// and interpreters, so that a reading is checked as the dashboard will run it.
package hosttest

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parsers"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// Logger discards what it is given.
func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Database is a PostgreSQL the test may empty, named by TEST_DATABASE_URL.
type Database struct {
	Config host.DatabaseConfig
	Pool   *pgxpool.Pool
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
	cfg := host.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Pool().Exec(ctx, "drop schema public cascade; create schema public;"); err != nil {
		t.Fatalf("resetting the schema: %v", err)
	}
	return &Database{Config: cfg, Pool: db.Pool(), core: db}
}

// MigrateCore applies the dashboard's migrations, which an extension's follow.
func (d *Database) MigrateCore(t *testing.T) {
	t.Helper()
	if err := store.Migrate(context.Background(), d.Config, Logger()); err != nil {
		t.Fatalf("the dashboard's migrations: %v", err)
	}
}

// Store is the dashboard's store on this database.
func (d *Database) Store() host.Store { return d.core }

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

// WithIdentity sets an identity on a context as authentication would.
func WithIdentity(ctx context.Context, identity host.Identity) context.Context {
	return dashhttp.ContextWithIdentity(ctx, identity)
}

// Authorizer decides with the given environment policies.
func Authorizer(policies host.ProjectPolicies) *host.Authorizer { return auth.NewAuthorizer(policies) }

// Modules answers whether a plugin is switched on, from the settings a reader returns, for the
// given plugins.
func Modules(t *testing.T, reader host.PluginReader, plugins ...capability.Plugin) *host.Modules {
	t.Helper()
	registry, err := capabilities.New(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	return dashhttp.NewModules(registry, reader, Logger())
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

// CheckReading reads a recorded case with a declared reading, as `arkhitekton check-reading`
// does: the facts produced, and those the case expects.
func CheckReading(reading []byte, dir string) (got, expected []byte, err error) {
	return declarative.Check(reading, dir)
}

// SameFacts says whether two serialised fact lists say the same.
func SameFacts(got, expected []byte) bool { return declarative.Same(got, expected) }
