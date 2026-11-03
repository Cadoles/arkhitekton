package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type updateSources struct {
	due      []store.PluginSource
	recorded map[string]store.UpdateCheck
}

func (u *updateSources) DueUpdateChecks(context.Context, time.Duration) ([]store.PluginSource, error) {
	return u.due, nil
}

func (u *updateSources) RecordUpdateCheck(_ context.Context, pluginID string, check store.UpdateCheck) error {
	u.recorded[pluginID] = check
	return nil
}

// Each source due is checked against its active version, a pinned one too; a repository that cannot
// be read records why, and one with nothing newer records nothing found.
func TestPluginsAreCheckedForANewerVersion(t *testing.T) {
	sources := &updateSources{recorded: map[string]store.UpdateCheck{}, due: []store.PluginSource{
		{PluginID: "mse", Repository: "forge.example/mse", ActiveVersion: "2.4.0"},
		{PluginID: "k6", Repository: "forge.example/k6", ActiveVersion: "1.0.0", Pinned: true},
		{PluginID: "down", Repository: "forge.example/down", ActiveVersion: "1.0.0"},
		{PluginID: "fresh", Repository: "forge.example/fresh", ActiveVersion: "3.0.0"},
		{PluginID: "pending", Repository: "forge.example/pending"},
	}}
	asked := map[string]string{}
	latest := func(_ context.Context, repository, current string) (installed.Release, bool, error) {
		asked[repository] = current
		switch repository {
		case "forge.example/down":
			return installed.Release{}, false, errors.New("registry unreachable")
		case "forge.example/fresh":
			return installed.Release{}, false, nil
		}
		return installed.Release{Version: "9.0.0", Digest: "sha256:new"}, true, nil
	}

	NewPluginUpdateChecker(sources, latest, time.Hour, slog.New(slog.DiscardHandler)).Pass(context.Background())

	if got := sources.recorded["mse"]; got.LatestVersion != "9.0.0" || got.LatestDigest != "sha256:new" {
		t.Errorf("mse = %+v", got)
	}
	if got := sources.recorded["k6"]; got.LatestVersion != "9.0.0" {
		t.Errorf("a pinned plugin is checked too: %+v", got)
	}
	if got := sources.recorded["down"]; got.Error != "registry unreachable" {
		t.Errorf("down = %+v", got)
	}
	if got, checked := sources.recorded["fresh"]; !checked || got.LatestVersion != "" {
		t.Errorf("fresh = %+v, %v", got, checked)
	}
	if _, checked := sources.recorded["pending"]; checked || asked["forge.example/pending"] != "" {
		t.Error("a plugin with no active version has nothing to compare")
	}
}
