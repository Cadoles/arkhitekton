package store

import (
	"context"
	"testing"
)

// Migration 0083 moves the tracker project into the tracker plugin's settings, without replacing a
// setting already made, then drops the columns: the version file is left to the plugin that used it.
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

	var shop, theme int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by,
		                       tracker_project, version_file, stack)
		 values ('shop', 'team/shop', 'Shop', 'https://forge.example', '.', 'test', ' shop-dev ', 'version.txt', 'php')
		 returning id`).Scan(&shop); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by, tracker_project)
		 values ('theme', 'team/theme', 'Theme', 'https://forge.example', '.', 'test', 'old')
		 returning id`).Scan(&theme); err != nil {
		t.Fatal(err)
	}
	// The shop's deploy plugin row exists with a setting, the theme's tracker is already chosen.
	if _, err := db.Pool().Exec(ctx,
		`insert into project_plugins (project_id, plugin_id, enabled, config)
		 values ($1, 'deploy', false, '{"deployer": "flux"}'), ($2, 'redmine', true, '{"project": "chosen"}')`,
		shop, theme); err != nil {
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
	if value, enabled := setting(shop, "redmine", "project"); value != "shop-dev" || !enabled {
		t.Errorf("the shop's tracker = %q, enabled %v", value, enabled)
	}
	if value, enabled := setting(shop, "deploy", "versionFile"); value != "" || enabled {
		t.Errorf("the shop's version file = %q, enabled %v: the row stays as it was", value, enabled)
	}
	if value, _ := setting(shop, "deploy", "deployer"); value != "flux" {
		t.Errorf("the shop's other setting = %q", value)
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
