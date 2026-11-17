package app

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// resolvedFixture answers the settings already layered, as the store does, per plugin.
type resolvedFixture map[string]map[string]string

func (f resolvedFixture) ResolvedPlugin(_ context.Context, _ int64, pluginID string) (store.PluginSettings, error) {
	config, found := f[pluginID]
	if !found {
		return store.PluginSettings{PluginID: pluginID}, store.ErrPluginDisabled
	}
	return store.PluginSettings{PluginID: pluginID, Enabled: true, Config: config}, nil
}

// A built-in plugin's settings reach its pipelines as `<plugin>.<field>`: every declared field, an
// empty one included, never a sensitive one, and nothing of a plugin switched off.
func TestABuiltinPluginsSettingsBecomeKeys(t *testing.T) {
	plugins := []capability.Plugin{
		{ID: "review",
			Global:  []capability.Field{{Name: "endpoint"}, {Name: "lead"}, {Name: "token", Secret: true}},
			Project: []capability.Field{{Name: "lead"}, {Name: "diffExclude"}}},
		{ID: "off", Global: []capability.Field{{Name: "url"}}},
		{ID: "bare"},
	}
	db := resolvedFixture{"review": {"endpoint": "https://llm/v1", "lead": "qwen", "token": "sealed"}}

	keys, err := builtinSettings(db, plugins)(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"review.endpoint": "https://llm/v1", "review.lead": "qwen", "review.diffExclude": ""}
	if len(keys) != len(expected) {
		t.Fatalf("keys = %v", keys)
	}
	for key, value := range expected {
		if got, found := keys[key]; !found || got != value {
			t.Errorf("%s = %q (%v), expected %q", key, got, found, value)
		}
	}
}

// The installed plugins answer after the built-in ones: a later source wins on a key both give.
func TestSettingsSourcesLayerInOrder(t *testing.T) {
	first := func(context.Context, int64) (map[string]string, error) {
		return map[string]string{"a": "1", "b": "1"}, nil
	}
	second := func(context.Context, int64) (map[string]string, error) {
		return map[string]string{"b": "2"}, nil
	}
	merged, err := mergedSettings(first, second)(context.Background(), 1)
	if err != nil || merged["a"] != "1" || merged["b"] != "2" {
		t.Errorf("%v, %v", merged, err)
	}
}
