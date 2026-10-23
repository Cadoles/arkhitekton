package mse

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
)

type settingsStore struct {
	config   map[string]string
	disabled bool
}

func (r settingsStore) ResolvedPlugin(_ context.Context, _ int64, pluginID string) (host.PluginSettings, error) {
	if r.disabled {
		return host.PluginSettings{PluginID: pluginID}, host.ErrPluginDisabled
	}
	return host.PluginSettings{PluginID: pluginID, Enabled: true, Config: r.config}, nil
}

// The plugin's settings become the values the catalogue references, under the keys it names.
func TestTheSettingsBecomeTheCataloguesKeys(t *testing.T) {
	settings := PipelineSettings(settingsStore{config: map[string]string{
		"kustomRepoUrl":   "ssh://git@forge/vcarroy/kustom.git",
		"kustomRevision":  "main",
		"e2eRepoUrl":      "ssh://git@forge/vcarroy/e2e.git",
		"overlayLocation": "overlays/pipeline",
		// A setting that has nothing to do with the pipelines: it must not leak into their parameters.
		"namespace": "flux-system",
	}})

	values, err := settings(context.Background(), 1)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	expected := map[string]string{
		"forge.mseKustom.repoUrl":  "ssh://git@forge/vcarroy/kustom.git",
		"forge.mseKustom.revision": "main",
		"forge.e2e.repoUrl":        "ssh://git@forge/vcarroy/e2e.git",
		"mse.overlayLocation":      "overlays/pipeline",
	}
	if len(values) != len(expected) {
		t.Fatalf("values = %v", values)
	}
	for key, value := range expected {
		if values[key] != value {
			t.Errorf("%s = %q, expected %q", key, values[key], value)
		}
	}
}

// An empty setting is not returned: it is the instance that supplies it, and an empty string
// would overwrite its own.
func TestAnEmptySettingLeavesTheInstances(t *testing.T) {
	settings := PipelineSettings(settingsStore{config: map[string]string{
		"kustomRepoUrl": "  ", "kustomRevision": "main",
	}})

	values, err := settings(context.Background(), 1)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, present := values["forge.mseKustom.repoUrl"]; present {
		t.Errorf("an empty setting was returned: %v", values)
	}
	if values["forge.mseKustom.revision"] != "main" {
		t.Errorf("values = %v", values)
	}
}

// Module switched off for the project: no value. Its pipelines have no kustomization to clone,
// and saying so here avoids a run that would point elsewhere.
func TestAProjectWithoutTheModuleBringsNothing(t *testing.T) {
	settings := PipelineSettings(settingsStore{disabled: true})

	values, err := settings(context.Background(), 2)
	if err != nil || values != nil {
		t.Errorf("values = %v (%v), expected none", values, err)
	}
}

// A failed read is reported: a run started with another project's kustomization would be worse
// than a run refused.
func TestAFailedReadIsReported(t *testing.T) {
	settings := PipelineSettings(failingRead{})

	if _, err := settings(context.Background(), 1); err == nil {
		t.Error("a failed read must be reported")
	}
}

type failingRead struct{}

func (failingRead) ResolvedPlugin(context.Context, int64, string) (host.PluginSettings, error) {
	return host.PluginSettings{}, errors.New("database unreachable")
}

// The seeding sets only what the configuration supplies.
func TestTheSeedingIgnoresMissingValues(t *testing.T) {
	defaults := PipelineDefaults("ssh://git@forge/kustom.git", "", "", "overlays/pipeline")

	if len(defaults) != 2 || defaults["kustomRepoUrl"] == "" || defaults["overlayLocation"] == "" {
		t.Errorf("defaults = %v", defaults)
	}
}
