package app

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/host/hosttest"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// A plugin's migrations are applied in its own version table, and its schema is checked against
// the files it ships.
func TestAnInstalledPluginsMigrationsApply(t *testing.T) {
	database := hosttest.EmptyDatabase(t)
	plugin, err := installed.Load(fstest.MapFS{
		manifest.FileName: {Data: []byte(`apiVersion: arkhitekton.plugin/v2
id: widgets
version: 1.0.0
sdk: "1"
label: Widgets
binary: bin/plugin
protocol: 1
migrations: { dir: migrations, table: goose_widgets_db_version }
`)},
		"bin/plugin": {Data: []byte("program")},
		"migrations/0001_widgets.sql": {Data: []byte(
			"-- +goose Up\ncreate table widgets (id bigint);\n-- +goose Down\ndrop table widgets;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := pluginMigrations(plugin)
	if err != nil || migrations == nil {
		t.Fatalf("migrations %v, %v", migrations, err)
	}
	if err := migrations.CheckSchema(database.Pool)(context.Background()); err == nil {
		t.Error("a schema not yet applied reads as ready")
	}
	cfg := config.DatabaseConfig{DSN: os.Getenv("TEST_DATABASE_URL"), MaxConns: 2, ConnMaxLifetime: time.Minute}
	if err := migratePlugin(cfg, slog.New(slog.DiscardHandler))(context.Background(), plugin); err != nil {
		t.Fatal(err)
	}
	if err := migrations.CheckSchema(database.Pool)(context.Background()); err != nil {
		t.Errorf("after migrating: %v", err)
	}
	if _, err := database.Pool.Exec(context.Background(), "insert into widgets values (1)"); err != nil {
		t.Errorf("the plugin's table: %v", err)
	}
}
