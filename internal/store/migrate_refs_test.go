package store

import (
	"context"
	"encoding/json"
	"testing"
)

// Migration 0055 names pipelines by their capability. Rows written under the old names are seeded
// one version back, then migrated: every place a kind is compared by equality must move, and the
// action steps must not.
func TestStoredPipelineKindsAreQualified(t *testing.T) {
	cfg := testDB(t)
	resetSchema(t, cfg)
	ctx := context.Background()

	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration: %v", err)
	}
	// Back to just before 0055, however many migrations follow it.
	names, err := MigrationNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name < "0055" {
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

	const plan = `{"stages":[
		{"role":"build","gestes":[{"kind":"pipeline","ref":"build","params":{"image-name":"portal"}}]},
		{"role":"test","gestes":[
			{"kind":"pipeline","ref":"unit-tests","label":"Unitaires"},
			{"kind":"action","ref":"gitea.status"},
			{"kind":"pipeline","ref":"custom-kind"}]}]}`

	var project, run int64
	seed := []struct {
		query string
		args  []any
		into  *int64
	}{
		{`insert into projects (slug, repository, label, forge_url, dockerfile, build_context, created_by)
		  values ('refs', 'vcarroy/refs', 'Refs', 'https://forge.example', 'Dockerfile', '.', 'test')
		  returning id`, nil, &project},
		{`insert into jobs (kind, queue_class, state, params, requested_by)
		  values ('build', 'build', 'queued', '{}', 'test'), ('export', 'local', 'queued', '{}', 'test')`, nil, nil},
		{`insert into project_pipelines (project_id, kind, enabled) values ($1, 'static-tests', false)`,
			[]any{&project}, nil},
		{`insert into queue_limits (kind, max_concurrent, updated_by) values ('integration-tests', 1, 'test')`, nil, nil},
		{`insert into release_automations (project_id, plan) values ($1, $2)`, []any{&project, plan}, nil},
		{`insert into reactions (project_id, name, event_type, plan) values ($1, 'r', 'gitea.pull_request.opened', $2)`,
			[]any{&project, plan}, nil},
		{`insert into plan_runs (trigger, event_key, plan) values ('builtin:release', 'k', $1) returning id`,
			[]any{plan}, &run},
		{`insert into plan_steps (run_id, stage, position, kind, ref)
		  values ($1, 0, 0, 'pipeline', 'build'), ($1, 1, 1, 'action', 'gitea.status')`, []any{&run}, nil},
	}
	for _, step := range seed {
		args := make([]any, len(step.args))
		for i, arg := range step.args {
			if pointer, ok := arg.(*int64); ok {
				args[i] = *pointer
			} else {
				args[i] = arg
			}
		}
		if step.into != nil {
			if err := db.Pool().QueryRow(ctx, step.query, args...).Scan(step.into); err != nil {
				t.Fatalf("seeding %q: %v", step.query, err)
			}
			continue
		}
		if _, err := db.Pool().Exec(ctx, step.query, args...); err != nil {
			t.Fatalf("seeding %q: %v", step.query, err)
		}
	}

	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration 0055: %v", err)
	}

	expect := func(query, want string) {
		t.Helper()
		var got string
		if err := db.Pool().QueryRow(ctx, query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Errorf("%s = %q, expected %q", query, got, want)
		}
	}
	expect(`select string_agg(kind, ',' order by id) from jobs`, "docker/build,export")
	expect(`select kind from project_pipelines`, "php/static-tests")
	expect(`select kind from queue_limits`, "mse/integration-tests")
	expect(`select string_agg(ref, ',' order by position) from plan_steps`, "docker/build,gitea.status")

	for _, table := range []string{"release_automations", "reactions", "plan_runs"} {
		var raw []byte
		if err := db.Pool().QueryRow(ctx, `select plan from `+table).Scan(&raw); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		var migrated struct {
			Stages []struct {
				Gestes []struct {
					Kind   string            `json:"kind"`
					Ref    string            `json:"ref"`
					Label  string            `json:"label"`
					Params map[string]string `json:"params"`
				} `json:"gestes"`
			} `json:"stages"`
		}
		if err := json.Unmarshal(raw, &migrated); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		build, test := migrated.Stages[0].Gestes, migrated.Stages[1].Gestes
		if build[0].Ref != "docker/build" || build[0].Params["image-name"] != "portal" {
			t.Errorf("%s: build step = %+v", table, build[0])
		}
		if test[0].Ref != "php/unit-tests" || test[0].Label != "Unitaires" {
			t.Errorf("%s: unit step = %+v", table, test[0])
		}
		if test[1].Ref != "gitea.status" || test[2].Ref != "custom-kind" {
			t.Errorf("%s: action and unknown kind must stay: %+v", table, test[1:])
		}
	}
}

// Migration 0074 moves the image pipelines from `core` to `docker`. Rows written under the old names
// are seeded one version back: the plans, the bindings and the plugin's own settings must follow,
// and the action steps must not.
func TestStoredImagePipelinesMoveToDocker(t *testing.T) {
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
		if name < "0074" {
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

	const plan = `{"stages":[{"gestes":[
		{"kind":"pipeline","ref":"core/build","params":{"image-name":"portal"}},
		{"kind":"pipeline","ref":"php/unit-tests"},
		{"kind":"action","ref":"gitea.release"}]}]}`

	var project, run int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context, created_by)
		 values ('refs', 'vcarroy/refs', 'Refs', 'https://forge.example', 'Dockerfile', '.', 'test')
		 returning id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx,
		`insert into plan_runs (trigger, event_key, plan) values ('builtin:release', 'k', $1) returning id`,
		plan).Scan(&run); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`insert into jobs (kind, capability, queue_class, state, params, requested_by)
		  values ('core/retag', 'core/retag', 'parallel', 'queued', '{}', 'test')`, nil},
		{`insert into project_pipelines (project_id, kind, enabled) values ($1, 'core/build', false)`, []any{project}},
		{`insert into queue_limits (kind, max_concurrent, updated_by) values ('core/build', 1, 'test')`, nil},
		{`insert into capability_bindings (ref, binding, updated_by) values ('core/retag', '{}', 'test')`, nil},
		{`insert into plugin_settings (plugin_id, enabled) values ('core', true)`, nil},
		{`insert into project_plugins (project_id, plugin_id) values ($1, 'core')`, []any{project}},
		{`insert into release_automations (project_id, plan) values ($1, $2)`, []any{project, plan}},
		{`insert into reactions (project_id, name, event_type, plan) values ($1, 'r', 'gitea.push', $2)`, []any{project, plan}},
		{`insert into schedules (project_id, name, cron, plan) values ($1, 's', '0 8 * * *', $2)`, []any{project, plan}},
		{`insert into plan_steps (run_id, stage, position, kind, ref)
		  values ($1, 0, 0, 'pipeline', 'core/build'), ($1, 0, 1, 'action', 'gitea.release')`, []any{run}},
	} {
		if _, err := db.Pool().Exec(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seeding %q: %v", seed.query, err)
		}
	}

	if err := Migrate(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("migration 0074: %v", err)
	}

	for query, want := range map[string]string{
		`select kind || ' ' || capability from jobs`:                       "docker/retag docker/retag",
		`select kind from project_pipelines`:                               "docker/build",
		`select kind from queue_limits`:                                    "docker/build",
		`select ref from capability_bindings`:                              "docker/retag",
		`select plugin_id from plugin_settings where plugin_id = 'docker'`: "docker",
		`select plugin_id from project_plugins`:                            "docker",
		`select string_agg(ref, ',' order by position) from plan_steps`:    "docker/build,gitea.release",
	} {
		var got string
		if err := db.Pool().QueryRow(ctx, query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Errorf("%s = %q, expected %q", query, got, want)
		}
	}

	for _, table := range []string{"release_automations", "reactions", "schedules", "plan_runs"} {
		var refs string
		if err := db.Pool().QueryRow(ctx, `select string_agg(g ->> 'ref', ',')
			from `+table+`, jsonb_array_elements(plan -> 'stages' -> 0 -> 'gestes') g`).Scan(&refs); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if refs != "docker/build,php/unit-tests,gitea.release" {
			t.Errorf("%s: %s", table, refs)
		}
	}
}
