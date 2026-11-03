package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const otherDigest = "sha256:" + "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// requested asks for an installation as an update or a following one would, then completes it.
func requested(t *testing.T, db *DB, request InstallationRequest, version string) (int64, error) {
	t.Helper()
	ctx := context.Background()
	if request.Reference == "" {
		request.Reference, request.Digest = "registry.example/plugins/gotest@"+otherDigest, otherDigest
	}
	request.Actor = "admin"
	id, err := db.RequestPluginInstallation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CompletePluginInstallation(ctx, id, CompletedInstallation{
		PluginID: "gotest", Version: version, SignedBy: "sha256:key", Object: "plugins/gotest/" + version,
	})
	return id, err
}

// An update activates the new version and removes the old one, the plugin's settings, its projects'
// settings and its bindings kept.
func TestAnUpdateReplacesTheActiveVersionAndKeepsTheConfiguration(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	first := installed(t, db, "1.0.0")
	if err := db.SetPluginSettings(ctx, PluginSettings{PluginID: "gotest", Enabled: true, Config: map[string]string{"url": "https://x"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProjectPluginSettings(ctx, testProjectID, PluginSettings{PluginID: "gotest", Enabled: true, Config: map[string]string{"key": "v"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCapabilityBinding(ctx, "gotest/unit-tests", json.RawMessage(`{"x":"y"}`), "admin"); err != nil {
		t.Fatal(err)
	}

	second, err := requested(t, db, InstallationRequest{Follows: true, Replaces: &first}, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if two, _ := db.PluginInstallation(ctx, second); !two.Active {
		t.Errorf("the update is not active: %+v", two)
	}
	if one, _ := db.PluginInstallation(ctx, first); one.Active || one.State != InstallationRemoved {
		t.Errorf("the replaced version: %+v", one)
	}
	settings, _ := db.PluginSettingsAll(ctx)
	if settings["gotest"].Config["url"] != "https://x" {
		t.Errorf("instance settings lost: %+v", settings["gotest"])
	}
	if resolved, err := db.ResolvedPlugin(ctx, testProjectID, "gotest"); err != nil || resolved.Config["key"] != "v" {
		t.Errorf("project settings lost: %+v, %v", resolved, err)
	}
	if bindings, _ := db.CapabilityBindings(ctx); len(bindings) != 1 {
		t.Errorf("bindings = %+v", bindings)
	}
	// Asked for by repository: the plugin now follows it.
	if source, err := db.PluginSource(ctx, "gotest"); err != nil || source.Pinned || source.ActiveVersion != "1.1.0" {
		t.Errorf("source = %+v, %v", source, err)
	}
}

// An update whose version to replace is no longer active changes nothing: another decision came
// first.
func TestAnUpdateOfAVersionNoLongerActiveIsRefused(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	first := installed(t, db, "1.0.0")
	second := installed(t, db, "1.0.1")
	if err := db.ActivatePluginInstallation(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := requested(t, db, InstallationRequest{Replaces: &first}, "1.1.0"); !errors.Is(err, ErrNothingToReplace) {
		t.Errorf("err = %v", err)
	}
	if two, _ := db.PluginInstallation(ctx, second); !two.Active {
		t.Error("the active version moved")
	}
}

// A first installation by digest pins; the source follows the version activated, and goes when the
// plugin is uninstalled.
func TestASourceIsRecordedPinnedAndGoesWithThePlugin(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	first := installed(t, db, "1.0.0")
	source, err := db.PluginSource(ctx, "gotest")
	if err != nil || !source.Pinned || source.Repository != "registry.example/plugins/gotest" || source.ActiveID != first {
		t.Fatalf("source = %+v, %v", source, err)
	}
	if err := db.SetSourcePinned(ctx, "gotest", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RemovePluginInstallation(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PluginSource(ctx, "gotest"); !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("err = %v", err)
	}
}

// A check is due until done, again once the interval has passed, and at once when asked for. A
// failed check keeps what the previous one found; the version found goes once installed.
func TestUpdateChecks(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	first := installed(t, db, "1.0.0")

	if due, _ := db.DueUpdateChecks(ctx, time.Hour); len(due) != 1 {
		t.Fatalf("due = %+v, never checked", due)
	}
	if err := db.RecordUpdateCheck(ctx, "gotest", UpdateCheck{LatestVersion: "1.1.0", LatestDigest: otherDigest}); err != nil {
		t.Fatal(err)
	}
	if due, _ := db.DueUpdateChecks(ctx, time.Hour); len(due) != 0 {
		t.Errorf("due = %+v, just checked", due)
	}
	if err := db.RequestUpdateCheck(ctx, "gotest"); err != nil {
		t.Fatal(err)
	}
	if due, _ := db.DueUpdateChecks(ctx, time.Hour); len(due) != 1 || !due[0].CheckRequested {
		t.Errorf("due = %+v, asked for", due)
	}
	if err := db.RecordUpdateCheck(ctx, "gotest", UpdateCheck{Error: "registry unreachable"}); err != nil {
		t.Fatal(err)
	}
	source, _ := db.PluginSource(ctx, "gotest")
	if source.LatestVersion != "1.1.0" || source.CheckError != "registry unreachable" || source.CheckRequested {
		t.Errorf("source = %+v", source)
	}

	if _, err := requested(t, db, InstallationRequest{Follows: true, Replaces: &first}, "1.1.0"); err != nil {
		t.Fatal(err)
	}
	if source, _ := db.PluginSource(ctx, "gotest"); source.LatestVersion != "" || source.LatestDigest != "" {
		t.Errorf("source = %+v: the version found is the one running", source)
	}
}

// An installation asked for by repository waits for the worker to resolve its digest.
func TestAnInstallationByRepositoryIsResolvedBeforeItIsPulled(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	id, err := db.RequestPluginInstallation(ctx, InstallationRequest{
		Reference: "registry.example/plugins/gotest", Actor: "admin", Follows: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ResolvePluginInstallation(ctx, id, "registry.example/plugins/gotest@"+otherDigest, otherDigest, "1.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolvePluginInstallation(ctx, id, "x@"+digest, digest, "1.0.0"); err == nil {
		t.Error("resolved twice")
	}
	installation, _ := db.PluginInstallation(ctx, id)
	if installation.Digest != otherDigest || installation.ExpectedVersion != "1.2.0" || !installation.Follows {
		t.Errorf("installation = %+v", installation)
	}
}
