package configure

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * An instance standing on a real database, configured through the services the screen uses: the
 * export reads what they wrote, and an apply must write the same thing back.
 */

// The token typed into the screen. It must never come out of an export, in clear or sealed.
const typedToken = "glpat-typed-into-the-screen"

type instance struct {
	db       *store.DB
	registry *capabilities.Registry
	catalog  *catalog.Catalog
	cipher   *secrets.Cipher
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// emptyInstance returns a migrated, empty database, or skips the test without TEST_DATABASE_URL.
func emptyInstance(t *testing.T) instance {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: integration test skipped")
	}
	cfg := config.DatabaseConfig{DSN: dsn, MaxConns: 4, ConnMaxLifetime: time.Minute}
	ctx := context.Background()

	reset, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	if _, err := reset.Pool().Exec(ctx, "drop schema public cascade; create schema public;"); err != nil {
		t.Fatalf("resetting the schema: %v", err)
	}
	reset.Close()
	if err := store.Migrate(ctx, cfg, quiet()); err != nil {
		t.Fatalf("migration: %v", err)
	}
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	t.Cleanup(db.Close)

	registry, err := capabilities.New(forgePlugin())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := db.EnsurePlugins(ctx, pluginIDs(registry)); err != nil {
		t.Fatalf("seeding the plugins: %v", err)
	}
	cipher, err := secrets.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "core/build", PipelineRef: "build-image", Produces: []catalog.Product{catalog.ProductImage}},
		{Kind: "php/static-tests", PipelineRef: "php-static", Verification: true,
			Produces: []catalog.Product{catalog.ProductResults}},
	}}
	return instance{db: db, registry: registry, catalog: pipelines, cipher: cipher}
}

// forgePlugin stands for a forge: settings at both scopes, sensitive ones among them, and a fact a
// reaction can listen for. The core may not list the built-in plugins, so it brings its own.
func forgePlugin() capability.Plugin {
	webhookSecret := capability.Field{Name: "webhook_secret", Label: "Secret", Type: capability.FieldText, Secret: true}
	return capability.Plugin{
		ID: "gitea", Label: "Gitea", Version: "1.0.0", APIVersion: sdk.APIVersion, PerProject: true,
		Events: []capability.EventSpec{{Suffix: "push", Label: "Push"}},
		Global: []capability.Field{
			{Name: "url", Label: "URL", Type: capability.FieldURL},
			{Name: "token", Label: "Jeton", Type: capability.FieldText, Secret: true},
			webhookSecret,
		},
		Project: []capability.Field{webhookSecret},
	}
}

func pluginIDs(registry *capabilities.Registry) []string {
	var ids []string
	for _, descriptor := range registry.Plugins() {
		ids = append(ids, descriptor.ID)
	}
	return ids
}

func (i instance) services() Services {
	return Services{
		Projects:         Projects{Store: i.db, Logger: quiet()},
		Plugins:          Plugins{Store: i.db, Registry: i.registry, Cipher: i.cipher, Logger: quiet()},
		ProjectPipelines: ProjectPipelines{Store: i.db, Catalog: i.catalog, Logger: quiet()},
		QueueLimits:      QueueLimits{Store: i.db, Logger: quiet()},
		Bindings:         Bindings{Store: i.db, Registry: i.registry, Catalog: i.catalog, Logger: quiet()},
		StatusMappings:   StatusMappings{Store: i.db, Logger: quiet()},
		Automations: Automations{Store: i.db, Schedules: i.db, Catalog: i.catalog, Registry: i.registry,
			Logger: quiet()},
	}
}

func buildPlan(ref string) plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: ref}}}}}
}

// configured fills an instance the way an administrator would from the screen.
func configured(t *testing.T) instance {
	t.Helper()
	i := emptyInstance(t)
	s := i.services()
	ctx := context.Background()
	const actor = "admin"

	if _, _, err := s.Plugins.Set(ctx, actor, "gitea", PluginInput{Enabled: true, Config: map[string]string{
		"url": "https://forge.example", "token": typedToken,
	}}); err != nil {
		t.Fatalf("gitea: %v", err)
	}
	id, err := s.Projects.Create(ctx, actor, ProjectInput{
		Repository: "team/portal", Slug: "portal", Stack: "",
		Targets:      []TargetInput{{Stage: "web", Image: "portal", Promote: true}, {Image: "cli"}},
		ReleaseKinds: release.Cycle{{ID: "rc", Label: "RC", Mode: release.ModeBuild}},
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	project, err := i.db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, _, err := s.Plugins.SetProject(ctx, actor, project, "gitea", PluginInput{Enabled: true, Config: map[string]string{
		"webhook_secret": "per-project-secret",
	}}); err != nil {
		t.Fatalf("project's gitea: %v", err)
	}
	if _, err := s.ProjectPipelines.Set(ctx, actor, project, "php/static-tests", false); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if err := s.QueueLimits.Set(ctx, actor, "php/static-tests", 2); err != nil {
		t.Fatalf("queue limit: %v", err)
	}
	if _, err := s.StatusMappings.Set(ctx, actor, project, "En recette", "client-test"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := s.Automations.SaveReleaseAutomation(ctx, actor, project, plan.Plan{Stages: []plan.Stage{{
		Role: plan.RoleBuild, Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "core/build"}},
	}}}); err != nil {
		t.Fatalf("automation: %v", err)
	}
	if _, err := s.Automations.SaveReaction(ctx, actor, project, reaction.Reaction{
		Name: "Analyse à chaque push", EventType: "gitea.push", Enabled: true,
		Match: map[string]string{"branch": "main"}, Plan: buildPlan("php/static-tests"),
	}); err != nil {
		t.Fatalf("reaction: %v", err)
	}
	if _, _, err := s.Automations.SaveSchedule(ctx, actor, project, schedule.Schedule{
		Name: "Nightly", Cron: "0 3 * * *", Timezone: "Europe/Paris", Enabled: false, Plan: buildPlan("php/static-tests"),
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	return i
}
