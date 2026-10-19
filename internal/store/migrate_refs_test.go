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
	expect(`select string_agg(kind, ',' order by id) from jobs`, "core/build,export")
	expect(`select kind from project_pipelines`, "php/static-tests")
	expect(`select kind from queue_limits`, "mse/integration-tests")
	expect(`select string_agg(ref, ',' order by position) from plan_steps`, "core/build,gitea.status")

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
		if build[0].Ref != "core/build" || build[0].Params["image-name"] != "portal" {
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
