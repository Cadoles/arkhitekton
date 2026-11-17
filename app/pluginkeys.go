package app

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * A built-in plugin's settings, as its pipelines read them.
 *
 * An installed plugin says what its pipelines read for a project through a hook of its program. A
 * built-in plugin has no program to ask: its settings, the instance's with the project's over
 * them, become configuration keys named `<plugin>.<field>`, which its capabilities bind.
 *
 * Every declared field is given, an empty one included: a binding parameter whose key is absent
 * fails the run, and a setting nobody filled in is not a configuration error unless the
 * parameter requires it. A sensitive field is never given: it is sealed, and a key passed as a
 * parameter would land in the PipelineRun's spec.
 */

type resolvedPlugins interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
}

// builtinSettings gives the settings of the built-in plugins switched on for a project.
func builtinSettings(db resolvedPlugins, plugins []capability.Plugin) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		keys := map[string]string{}
		for _, plugin := range plugins {
			fields := settingFields(plugin)
			if len(fields) == 0 {
				continue
			}
			resolved, err := db.ResolvedPlugin(ctx, projectID, plugin.ID)
			if errors.Is(err, store.ErrPluginDisabled) {
				// Switched off here: its pipelines are refused anyway, and its settings say nothing.
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("settings of plugin %s: %w", plugin.ID, err)
			}
			for _, field := range fields {
				keys[plugin.ID+"."+field.Name] = resolved.Config[field.Name]
			}
		}
		return keys, nil
	}
}

// settingFields are a plugin's non-sensitive fields, each once whether the instance, a project or
// both declare it.
func settingFields(plugin capability.Plugin) []capability.Field {
	var fields []capability.Field
	seen := map[string]bool{}
	for _, field := range append(append([]capability.Field{}, plugin.Global...), plugin.Project...) {
		if field.Secret || seen[field.Name] {
			continue
		}
		seen[field.Name] = true
		fields = append(fields, field)
	}
	return fields
}

// mergedSettings layers what several sources give for a project, the later over the earlier.
func mergedSettings(sources ...func(context.Context, int64) (map[string]string, error)) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		merged := map[string]string{}
		for _, source := range sources {
			values, err := source(ctx, projectID)
			if err != nil {
				return nil, err
			}
			maps.Copy(merged, values)
		}
		return merged, nil
	}
}
