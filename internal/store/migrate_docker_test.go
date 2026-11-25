package store

import (
	"context"
	"strings"
	"testing"
)

// Migration 0086 moves a project's Docker settings into the docker plugin, marks the image no
// environment takes on the plan step and the artifacts that published it, renames the plan's
// variables, then drops the columns; rolled back, it restores them from the plugin and the plan.
func TestDockerSettingsMoveToThePlugin(t *testing.T) {
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
		if name < "0086" {
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

	var mstore, kustom int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, created_by, dockerfile, build_context,
		                       image_registry, registry_secret, multi_stage, targets)
		 values ('mstore', 'team/mstore', 'Mstore', 'https://forge.example', 'test', './misc/Dockerfile', '.',
		         'reg.example/mstore', 'regcred-mstore', true,
		         '[{"stage": "web", "image": "portal"}, {"stage": "dev", "image": "portal-dev", "promote": false}]')
		 returning id`).Scan(&mstore); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, created_by)
		 values ('kustom', 'team/kustom', 'Kustom', 'https://forge.example', 'test')
		 returning id`).Scan(&kustom); err != nil {
		t.Fatal(err)
	}
	// A docker row left by 0074, switched off.
	if _, err := db.Pool().Exec(ctx,
		`insert into project_plugins (project_id, plugin_id, enabled, config) values ($1, 'docker', false, '{}')`,
		mstore); err != nil {
		t.Fatal(err)
	}
	const plan = `{"stages": [{"role": "build", "gestes": [
		{"kind": "pipeline", "ref": "docker/build", "label": "portal",
		 "params": {"image-name": "portal", "dockerfile": "{project.dockerfile}", "context": "{project.build_context}"}},
		{"kind": "pipeline", "ref": "docker/build", "label": "portal-dev",
		 "params": {"image-name": "portal-dev", "target": "dev"}}]}]}`
	if _, err := db.Pool().Exec(ctx,
		`insert into release_automations (project_id, plan) values ($1, $2)`, mstore, plan); err != nil {
		t.Fatal(err)
	}
	var run int64
	if err := db.Pool().QueryRow(ctx,
		`insert into plan_runs (trigger, event_key, plan, project_id) values ('builtin:release', 'k', $1, $2)
		 returning id`, plan, mstore).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx,
		`insert into plan_artifacts (run_id, stage, position, name, kind, reference) values
		 ($1, 0, 0, 'image', 'image', 'reg.example/mstore/portal:1.0.0-rc.1'),
		 ($1, 0, 1, 'image', 'image', 'registry:5000/mstore/portal-dev:1.0.0-rc.1')`, run); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var config map[string]string
	var enabled bool
	if err := db.Pool().QueryRow(ctx,
		`select config, enabled from project_plugins where project_id = $1 and plugin_id = 'docker'`, mstore).
		Scan(&config, &enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled || config["dockerfile"] != "./misc/Dockerfile" || config["registrySecret"] != "regcred-mstore" ||
		len(config) != 2 {
		t.Errorf("docker settings = %v, enabled %v: the context is the default, the registry was never read", config, enabled)
	}
	var rows int
	if err := db.Pool().QueryRow(ctx,
		`select count(*) from project_plugins where project_id = $1`, kustom).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a project with nothing to carry got %d row(s) (%v)", rows, err)
	}

	var declared string
	if err := db.Pool().QueryRow(ctx,
		`select plan::text from release_automations where project_id = $1`, mstore).Scan(&declared); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"{docker.dockerfile}"`, `"{docker.buildContext}"`, `"promote": false`} {
		if !strings.Contains(declared, expected) {
			t.Errorf("plan lacks %s: %s", expected, declared)
		}
	}
	var promoted []bool
	rowsOf, err := db.Pool().Query(ctx,
		`select promote from plan_artifacts where run_id = $1 order by position`, run)
	if err != nil {
		t.Fatal(err)
	}
	for rowsOf.Next() {
		var value bool
		if err := rowsOf.Scan(&value); err != nil {
			t.Fatal(err)
		}
		promoted = append(promoted, value)
	}
	if len(promoted) != 2 || !promoted[0] || promoted[1] {
		t.Errorf("artifacts promoted = %v, expected the development image held", promoted)
	}

	if err := Rollback(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var dockerfile, buildContext, secret, targets string
	var multi bool
	if err := db.Pool().QueryRow(ctx,
		`select dockerfile, build_context, registry_secret, targets::text, multi_stage from projects where id = $1`, mstore).
		Scan(&dockerfile, &buildContext, &secret, &targets, &multi); err != nil {
		t.Fatal(err)
	}
	if dockerfile != "./misc/Dockerfile" || buildContext != "." || secret != "regcred-mstore" || !multi ||
		!strings.Contains(targets, `"image": "portal-dev"`) || !strings.Contains(targets, `"promote": false`) {
		t.Errorf("restored: %q %q %q %v %s", dockerfile, buildContext, secret, multi, targets)
	}
}
