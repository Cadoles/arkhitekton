package mse

import (
	"context"
	"errors"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * What MSE's pipelines need to know, per project.
 *
 * The core exposed these values to the scheduler under keys naming MSE —
 * `forge.mseKustom.repoUrl`, `mse.overlayLocation` — read from its configuration. They now come
 * from the plugin's settings, where a project can override them: two projects do not clone the
 * same kustomization.
 */

// pipelineFields associates a plugin setting with the key the catalogue references.
//
// Two vocabularies, deliberately: the catalogue names what a pipeline needs, the plugin what an
// administrator sets. Merging them would force the administration screen to display
// `forge.mseKustom.repoUrl`.
var pipelineFields = map[string]string{
	"kustomRepoUrl":   "forge.mseKustom.repoUrl",
	"kustomRevision":  "forge.mseKustom.revision",
	"e2eRepoUrl":      "forge.e2e.repoUrl",
	"overlayLocation": "mse.overlayLocation",
}

// SettingsReader returns a plugin's effective settings for a project.
type SettingsReader interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
}

// PipelineSettings returns the configuration values a project adds to the parameters of its
// pipelines.
//
// Nothing when the module is switched off for it: its pipelines then have no reason to clone a
// kustomization, and a required parameter left uncovered fails the queueing with a message that
// names it.
//
// An empty value is not returned: it is the instance that supplies it, and an empty string would
// overwrite its own (see [plugin.Resolve]).
func PipelineSettings(settings SettingsReader) func(context.Context, int64) (map[string]string, error) {
	return func(ctx context.Context, projectID int64) (map[string]string, error) {
		resolved, err := settings.ResolvedPlugin(ctx, projectID, PluginID)
		switch {
		case errors.Is(err, store.ErrPluginDisabled):
			return nil, nil
		case err != nil:
			return nil, err
		}

		values := make(map[string]string, len(pipelineFields))
		for field, key := range pipelineFields {
			if value := strings.TrimSpace(resolved.Config[field]); value != "" {
				values[key] = value
			}
		}
		return values, nil
	}
}

// PipelineDefaults returns the global settings to seed from the configuration.
//
// The seeding, and not the read: the deployment's environment variables set the first value,
// the database is authoritative afterwards. Without that, a setting changed from the
// administration screen would be taken back by the next deployment.
func PipelineDefaults(kustomRepoURL, kustomRevision, e2eRepoURL, overlayLocation string) map[string]string {
	defaults := map[string]string{
		"kustomRepoUrl":   kustomRepoURL,
		"kustomRevision":  kustomRevision,
		"e2eRepoUrl":      e2eRepoURL,
		"overlayLocation": overlayLocation,
	}
	for field, value := range defaults {
		if strings.TrimSpace(value) == "" {
			delete(defaults, field)
		}
	}
	return defaults
}
