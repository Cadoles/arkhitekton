package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func installed(t *testing.T, db *DB, version string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := db.RequestPluginInstallation(ctx, "registry.example/plugins/gotest@"+digest, digest, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompletePluginInstallation(ctx, id, "gotest", version, "sha256:key", "plugins/gotest/"+version); err != nil {
		t.Fatal(err)
	}
	return id
}

// The first version becomes active, a later one waits to be activated; one version is active at a
// time, and each change moves the counter.
func TestPluginVersionsLiveSideBySide(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	before, _ := db.CapabilityVersion(ctx)

	first := installed(t, db, "1.0.0")
	second := installed(t, db, "1.0.1")
	if one, _ := db.PluginInstallation(ctx, first); !one.Active {
		t.Error("the first version is not active")
	}
	if two, _ := db.PluginInstallation(ctx, second); two.Active || two.State != InstallationInstalled {
		t.Errorf("the second version: %+v", two)
	}
	if err := db.ActivatePluginInstallation(ctx, second); err != nil {
		t.Fatal(err)
	}
	if one, _ := db.PluginInstallation(ctx, first); one.Active {
		t.Error("two versions active")
	}
	if after, _ := db.CapabilityVersion(ctx); after != before+3 {
		t.Errorf("version %d then %d", before, after)
	}

	settings, err := db.PluginSettingsAll(ctx)
	if err != nil || !settings["gotest"].Enabled {
		t.Errorf("settings %+v, %v", settings["gotest"], err)
	}

	id, err := db.RequestPluginInstallation(ctx, "registry.example/plugins/gotest@"+digest, digest, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompletePluginInstallation(ctx, id, "gotest", "1.0.1", "sha256:key", "x"); !errors.Is(err, ErrVersionInstalled) {
		t.Errorf("a version installed twice: %v", err)
	}
	if err := db.FailPluginInstallation(ctx, id, "already installed"); err != nil {
		t.Fatal(err)
	}
	if failed, _ := db.PluginInstallation(ctx, id); failed.State != InstallationFailed || failed.Error != "already installed" {
		t.Errorf("failed = %+v", failed)
	}
}

// Removing a version that is not active keeps the plugin; removing the active one uninstalls it,
// its bindings and settings with it.
func TestRemovingTheActiveVersionUninstalls(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	first := installed(t, db, "1.0.0")
	second := installed(t, db, "1.0.1")
	if err := db.SetCapabilityBinding(ctx, "gotest/unit-tests", json.RawMessage(`{"ref":"gotest/unit-tests"}`), "admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCapabilityBinding(ctx, "gotests/unit", json.RawMessage(`{"ref":"gotests/unit"}`), "admin"); err != nil {
		t.Fatal(err)
	}

	if uninstalled, err := db.RemovePluginInstallation(ctx, second); err != nil || uninstalled != "" {
		t.Fatalf("removing the inactive version: %q, %v", uninstalled, err)
	}
	if plugins, _ := db.InstalledPlugins(ctx); len(plugins) != 1 || plugins[0].ID != first {
		t.Errorf("installed = %+v", plugins)
	}

	if uninstalled, err := db.RemovePluginInstallation(ctx, first); err != nil || uninstalled != "gotest" {
		t.Fatalf("removing the active version: %q, %v", uninstalled, err)
	}
	if plugins, _ := db.InstalledPlugins(ctx); len(plugins) != 0 {
		t.Errorf("installed = %+v", plugins)
	}
	bindings, _ := db.CapabilityBindings(ctx)
	if len(bindings) != 1 || !strings.HasPrefix(bindings[0].Ref, "gotests/") {
		t.Errorf("bindings = %+v", bindings)
	}
	if settings, _ := db.PluginSettingsAll(ctx); settings["gotest"].PluginID != "" {
		t.Errorf("settings kept: %+v", settings["gotest"])
	}
	if _, err := db.RemovePluginInstallation(ctx, first); !errors.Is(err, ErrInstallationNotFound) {
		t.Errorf("removed twice: %v", err)
	}
}

// Only the installed versions' layers are referenced: a removed version's is an orphan.
func TestOnlyInstalledLayersAreReferenced(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	installed(t, db, "1.0.0")
	second := installed(t, db, "1.0.1")
	if _, err := db.RemovePluginInstallation(ctx, second); err != nil {
		t.Fatal(err)
	}
	objects, err := db.PluginObjects(ctx)
	if err != nil || len(objects) != 1 || !objects["plugins/gotest/1.0.0"] {
		t.Errorf("objects %v, %v", objects, err)
	}
}
