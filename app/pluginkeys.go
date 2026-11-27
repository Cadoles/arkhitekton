package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/Cadoles/arkhitekton/internal/plan"
	"github.com/Cadoles/arkhitekton/internal/plugins/docker"
	"github.com/Cadoles/arkhitekton/internal/store"
	"github.com/Cadoles/arkhitekton/sdk/capability"
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

// dockerKeys gives the registry a project publishes to, and the Secret that allows it, over the
// deployment's: `registry.project` and `pipeline.registrySecret` are what the Docker pipelines bind.
//
// A secret left empty keeps the deployment's: that is where a project naming none pushes.
func dockerKeys(db resolvedPlugins, deploymentRegistry string) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		settings, err := dockerSettings(ctx, db, projectID, deploymentRegistry)
		if err != nil {
			return nil, err
		}
		keys := map[string]string{}
		if settings.Registry != "" {
			keys["registry.project"] = settings.Registry
		}
		if settings.RegistrySecret != "" {
			keys["pipeline.registrySecret"] = settings.RegistrySecret
		}
		return keys, nil
	}
}

// dockerVariables gives a run the project's Docker settings with their defaults, as
// `{docker.dockerfile}` and `{docker.buildContext}`: a build step names them once, and a project
// that set nothing builds its `Dockerfile` from the root.
func dockerVariables(db resolvedPlugins, deploymentRegistry string) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		settings, err := dockerSettings(ctx, db, projectID, deploymentRegistry)
		if err != nil {
			return nil, err
		}
		return map[string]string{
			docker.ID + "." + docker.FieldDockerfile:   settings.Dockerfile,
			docker.ID + "." + docker.FieldBuildContext: settings.BuildContext,
			docker.ID + "." + docker.FieldRegistry:     settings.Registry,
		}, nil
	}
}

func dockerSettings(ctx context.Context, db resolvedPlugins, projectID int64, deploymentRegistry string) (docker.Settings, error) {
	resolved, err := db.ResolvedPlugin(ctx, projectID, docker.ID)
	switch {
	case errors.Is(err, store.ErrPluginDisabled):
		return docker.Resolve(nil, deploymentRegistry), nil
	case err != nil:
		return docker.Settings{}, fmt.Errorf("Docker settings of project %d: %w", projectID, err)
	}
	return docker.Resolve(resolved.Config, deploymentRegistry), nil
}

// defaultBuilds gives the build steps the plugins switched on for a project propose for it.
func defaultBuilds(db resolvedPlugins, plugins []capability.Plugin, logger *slog.Logger) func(context.Context, store.Project) []plan.Geste {
	return func(ctx context.Context, project store.Project) []plan.Geste {
		var gestes []plan.Geste
		for _, plugin := range plugins {
			if plugin.DefaultBuild == nil {
				continue
			}
			resolved, err := db.ResolvedPlugin(ctx, project.ID, plugin.ID)
			if errors.Is(err, store.ErrPluginDisabled) {
				continue
			}
			if err != nil {
				logger.Error("a plugin's settings for a default build", "project", project.Slug,
					"plugin", plugin.ID, "error", err)
				continue
			}
			for _, step := range plugin.DefaultBuild(project.Slug, resolved.Config) {
				gestes = append(gestes, plan.Geste{
					Kind: plan.GestePipeline, Ref: plugin.ID + "/" + step.Pipeline, Label: step.Label,
					Params: step.Params,
				})
			}
		}
		return gestes
	}
}
