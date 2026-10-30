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
	if _, err := db.CompletePluginInstallation(ctx, id, CompletedInstallation{
		PluginID: "gotest", Version: version, SignedBy: "sha256:key", Object: "plugins/gotest/" + version,
		MigrationsTable: "goose_gotest_db_version",
	}); err != nil {
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
	if _, err := db.CompletePluginInstallation(ctx, id, CompletedInstallation{
		PluginID: "gotest", Version: "1.0.1", SignedBy: "sha256:key", Object: "x",
	}); !errors.Is(err, ErrVersionInstalled) {
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

// A plugin bringing a tracker or a forge is installed switched off: one of each is switched on,
// and the administrator switches the other off first.
func TestATrackerOrAForgeIsInstalledSwitchedOff(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	id, err := db.RequestPluginInstallation(ctx, "registry.example/plugins/forgery@"+digest, digest, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompletePluginInstallation(ctx, id, CompletedInstallation{
		PluginID: "forgery", Version: "1.0.0", SignedBy: "sha256:key", Object: "plugins/forgery/1.0.0", SwitchedOff: true,
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := db.PluginSettingsAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if entry, found := settings["forgery"]; !found || entry.Enabled {
		t.Errorf("settings = %+v, %v", entry, found)
	}
}

// Uninstalling leaves the active version's tables behind, its layer kept to undo them; a purge or
// a new installation ends the leftover, and a purge forgets the plugin's cursors.
func TestAnUninstalledPluginLeavesItsTablesUntilPurged(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	first := installed(t, db, "1.0.0")
	if err := db.RecordSyncSuccess(ctx, "gotest.pulls", testProjectID, "cursor"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordSyncSuccess(ctx, "gotests", testProjectID, "cursor"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PluginLeftoverOf(ctx, "gotest"); !errors.Is(err, ErrNothingToPurge) {
		t.Errorf("an installed plugin left %v", err)
	}
	if _, err := db.RemovePluginInstallation(ctx, first); err != nil {
		t.Fatal(err)
	}

	leftovers, err := db.PluginLeftovers(ctx)
	if err != nil || len(leftovers) != 1 || leftovers[0].Version != "1.0.0" || leftovers[0].Table != "goose_gotest_db_version" {
		t.Fatalf("leftovers %+v, %v", leftovers, err)
	}
	if objects, _ := db.PluginObjects(ctx); !objects["plugins/gotest/1.0.0"] {
		t.Errorf("the leftover's layer is not kept: %v", objects)
	}

	// Installed again, its tables are its own again.
	second := installed(t, db, "1.0.1")
	if leftovers, _ := db.PluginLeftovers(ctx); len(leftovers) != 0 {
		t.Errorf("a reinstalled plugin still leaves %+v", leftovers)
	}
	if _, err := db.RemovePluginInstallation(ctx, second); err != nil {
		t.Fatal(err)
	}
	if leftover, err := db.PluginLeftoverOf(ctx, "gotest"); err != nil || leftover.Version != "1.0.1" {
		t.Fatalf("leftover %+v, %v", leftover, err)
	}

	if err := db.RecordPluginPurged(ctx, "gotest"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordPluginPurged(ctx, "gotest"); !errors.Is(err, ErrNothingToPurge) {
		t.Errorf("purged twice: %v", err)
	}
	if objects, _ := db.PluginObjects(ctx); len(objects) != 0 {
		t.Errorf("a purged plugin's layer is kept: %v", objects)
	}
	if state, _ := db.GetSyncState(ctx, "gotest.pulls", testProjectID); state.Cursor != "" {
		t.Errorf("the purged plugin's cursor stays: %+v", state)
	}
	if state, _ := db.GetSyncState(ctx, "gotests", testProjectID); state.Cursor != "cursor" {
		t.Errorf("another plugin's cursor went: %+v", state)
	}
}

// A source the instance does not recognise parks the installation with the plugin's id, which is
// what an administrator is then asked about and what the acceptance is recorded against.
func TestAnAcceptedSourceIsRememberedForThePlugin(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id, err := db.RequestPluginInstallation(ctx, "registry.example/plugins/junit@"+digest, digest, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ParkPluginInstallation(ctx, id, "junit", "signed by no trusted key"); err != nil {
		t.Fatal(err)
	}
	parked, err := db.PluginInstallation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if parked.State != InstallationUnrecognised || parked.PluginID != "junit" ||
		parked.Error != "signed by no trusted key" {
		t.Fatalf("parked = %+v", parked)
	}

	accepted, err := db.AcceptedSources(ctx)
	if err != nil || len(accepted) != 0 {
		t.Fatalf("accepted = %v, %v", accepted, err)
	}
	if err := db.AcceptSource(ctx, "junit", parked.Error, "admin"); err != nil {
		t.Fatal(err)
	}
	// Idempotent, and the first acceptance is the one kept: a second says nothing new.
	if err := db.AcceptSource(ctx, "junit", "another reason", "someone-else"); err != nil {
		t.Fatal(err)
	}
	if accepted, err = db.AcceptedSources(ctx); err != nil || len(accepted) != 1 || accepted[0] != "junit" {
		t.Fatalf("accepted = %v, %v", accepted, err)
	}

	if err := db.RetryPluginInstallation(ctx, id); err != nil {
		t.Fatal(err)
	}
	retried, err := db.PluginInstallation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if retried.State != InstallationPending || retried.Error != "" {
		t.Errorf("retried = %+v", retried)
	}
	// Only a parked installation is retried: it is now pending, and a second retry finds nothing.
	if err := db.RetryPluginInstallation(ctx, id); !errors.Is(err, ErrInstallationNotFound) {
		t.Errorf("a second retry: %v", err)
	}

	if err := db.WithdrawSource(ctx, "junit"); err != nil {
		t.Fatal(err)
	}
	if accepted, err = db.AcceptedSources(ctx); err != nil || len(accepted) != 0 {
		t.Errorf("withdrawn: accepted = %v, %v", accepted, err)
	}
}
