package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/module"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// hookedDB switches the plugin on everywhere but project 8.
type hookedDB struct{}

func (hookedDB) ResolvedPlugin(_ context.Context, projectID int64, _ string) (store.PluginSettings, error) {
	if projectID == 8 {
		return store.PluginSettings{}, store.ErrPluginDisabled
	}
	return store.PluginSettings{Enabled: true}, nil
}

func (hookedDB) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	return store.Project{ID: 7, Slug: slug}, nil
}

// hookedPlugin builds the test plugin and makes it the active version of `deploy`.
func hookedPlugin(t *testing.T) pluginHooks {
	t.Helper()
	program := filepath.Join(t.TempDir(), "plugin")
	build := exec.Command("go", "build", "-o", program, "./testdata/hooked")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	raw, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	plugin := installed.Plugin{
		Plugin: capability.Plugin{ID: "deploy", Version: "1.0.0"},
		Native: &installed.Native{
			Manifest: manifest.Manifest{Binary: "bin/plugin", Hooks: manifest.Hooks, Module: true},
			Digest:   "sha256:" + hex.EncodeToString(sum[:]),
			Files:    fstest.MapFS{"bin/plugin": {Data: raw}},
		},
	}
	programs := newPrograms(config.Config{}, rpc.RoleWorker, slog.New(slog.DiscardHandler))
	t.Cleanup(programs.close)
	programs.lend(native.HostDeps{Store: nil})
	programs.register(plugin)
	programs.activate([]installed.Plugin{plugin})
	return pluginHooks{programs: programs, db: hookedDB{}, logger: slog.New(slog.DiscardHandler)}
}

func TestTheInstalledPluginsAnswerTheHooks(t *testing.T) {
	hooks := hookedPlugin(t)
	ctx := context.Background()

	deployed, err := hooks.deployments()(ctx, 7)
	if err != nil || len(deployed["1.0.0"]) != 1 || deployed["1.0.0"][0] != "staging" {
		t.Errorf("deployments %v, %v", deployed, err)
	}
	if deployed, _ := hooks.deployments()(ctx, 8); len(deployed) != 0 {
		t.Errorf("a project switching the plugin off heard it: %v", deployed)
	}
	promotions, err := hooks.promotions()(ctx, 7, 3)
	if err != nil || len(promotions) != 1 || promotions[0].Tag != "r3" {
		t.Errorf("promotions %v, %v", promotions, err)
	}
	if suggested := hooks.suggestions()(ctx, "app"); len(suggested) != 1 || suggested[0].Label != "Staging de app" {
		t.Errorf("suggestions %v", suggested)
	}
	if err := hooks.schemeCheck()(ctx, versionpkg.Opaque{}); err == nil {
		t.Error("a scheme the plugin refuses accepted")
	}
	if err := hooks.schemeCheck()(ctx, versionpkg.SemVer{}); err != nil {
		t.Errorf("semver refused: %v", err)
	}
	settings, err := hooks.projectSettings()(ctx, 7)
	if err != nil || settings["deploy.repoUrl"] != "ssh://deploy" {
		t.Errorf("settings %v, %v", settings, err)
	}

	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fact := pluginPromotionFacts{hooks: hooks}.Promoted(at, store.JobReleaseContext{ProjectID: 7, JobID: 12, Tag: "1.0.0", Environment: "staging"})
	if fact.Type != "deploy.release.promoted" || fact.Source != "deploy" || fact.Subject != "1.0.0" ||
		fact.DedupKey != "deploy.release.promoted:12" || !fact.OccurredAt.Equal(at) || fact.Payload["environment"] != "staging" {
		t.Errorf("fact = %+v", fact)
	}
	if fact := (pluginPromotionFacts{hooks: hooks}).Promoted(at, store.JobReleaseContext{ProjectID: 8}); fact.Type != "" {
		t.Errorf("a project switching the plugin off stated %+v", fact)
	}
}

// The module runs in the program as long as the supervisor's run lasts.
func TestAnInstalledModuleRunsInItsProgram(t *testing.T) {
	hooks := hookedPlugin(t)
	modules := hooks.programs.modules()
	if len(modules) != 1 || modules[0].Plugin() != "deploy" {
		t.Fatalf("modules = %v", modules)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() {
		ended <- modules[0].Run(ctx, module.State{Projects: []module.Project{{Project: store.Project{ID: 7, Slug: "app"}}}})
	}()
	select {
	case err := <-ended:
		t.Fatalf("the module ended on its own: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the module did not stop")
	}

	if err := modules[0].Run(context.Background(), module.State{}); err == nil {
		t.Error("the program's failure did not reach the supervisor")
	}
}
