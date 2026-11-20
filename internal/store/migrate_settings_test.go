package store

import (
	"context"
	"testing"
)

// Migration 0083 moves the tracker project and the version file into their plugins' settings,
// without replacing a setting already made, then drops the columns.
func TestProjectSettingsMoveToTheirPlugins(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)
	ctx := context.Background()

	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}
	names, err := MigrationNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name < "0083" {
			continue
		}
		if err := Rollback(ctx, cfg, discardLogger()); err != nil {
			t.Fatalf("rollback: %v", err)
		}
	}

	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer db.Close()

	var mse, theme int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by,
		                       tracker_project, version_file, stack)
		 values ('mse', 'CNOUS/mse', 'MSE', 'https://forge.example', '.', 'test', ' mse-dev ', 'version.txt', 'php')
		 returning id`).Scan(&mse); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by, tracker_project)
		 values ('theme', 'CNOUS/theme', 'Theme', 'https://forge.example', '.', 'test', 'old')
		 returning id`).Scan(&theme); err != nil {
		t.Fatal(err)
	}
	// MSE's plugin row exists with another setting, the theme's tracker is already chosen.
	if _, err := db.Pool().Exec(ctx,
		`insert into project_plugins (project_id, plugin_id, enabled, config)
		 values ($1, 'mse', false, '{"deployer": "flux"}'), ($2, 'redmine', true, '{"project": "chosen"}')`,
		mse, theme); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	setting := func(project int64, plugin, key string) (string, bool) {
		t.Helper()
		var value *string
		var enabled bool
		if err := db.Pool().QueryRow(ctx,
			`select config->>$3, enabled from project_plugins where project_id = $1 and plugin_id = $2`,
			project, plugin, key).Scan(&value, &enabled); err != nil {
			t.Fatalf("%s of project %d: %v", plugin, project, err)
		}
		if value == nil {
			return "", enabled
		}
		return *value, enabled
	}
	if value, enabled := setting(mse, "redmine", "project"); value != "mse-dev" || !enabled {
		t.Errorf("MSE's tracker = %q, enabled %v", value, enabled)
	}
	if value, enabled := setting(mse, "mse", "versionFile"); value != "version.txt" || enabled {
		t.Errorf("MSE's version file = %q, enabled %v: the row keeps its switch", value, enabled)
	}
	if value, _ := setting(mse, "mse", "deployer"); value != "flux" {
		t.Errorf("MSE's other setting = %q", value)
	}
	if value, _ := setting(theme, "redmine", "project"); value != "chosen" {
		t.Errorf("the theme's tracker = %q: a setting already made wins", value)
	}

	var columns int
	if err := db.Pool().QueryRow(ctx,
		`select count(*) from information_schema.columns
		 where table_name = 'projects' and column_name in ('tracker_project', 'version_file', 'stack')`).
		Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Errorf("%d column(s) left", columns)
	}
}
