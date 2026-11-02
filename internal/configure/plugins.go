package configure

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// PluginStore is what plugin settings need from the database.
type PluginStore interface {
	PluginSettingsAll(ctx context.Context) (map[string]store.PluginSettings, error)
	SetPluginSettings(ctx context.Context, entry store.PluginSettings, actor string) error
	ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]store.PluginSettings, error)
	SetProjectPluginSettings(ctx context.Context, projectID int64, entry store.PluginSettings, actor string) error
	ClearProjectPluginSettings(ctx context.Context, projectID int64, pluginID string) error
	Auditor
}

// PluginInput is a plugin's settings as an administrator writes them.
type PluginInput struct {
	Enabled bool
	Config  map[string]string
	// ClearedSecrets names the sensitive fields to clear. Explicit, because an empty sensitive
	// field means "unchanged": without that distinct gesture, clearing a key would be impossible,
	// and confusing it with an unfilled field would clear it on every save.
	ClearedSecrets []string
}

// Plugins writes the plugins' settings, at both scopes.
//
// A sensitive field's value is sealed before it is written and never read back in clear: a
// sensitive field left empty is kept, a sensitive field to erase is named in ClearedSecrets.
type Plugins struct {
	Store    PluginStore
	Registry capabilities.Source
	// Cipher seals the sensitive fields. Nil when no key is configured: writing a sensitive field
	// is then refused with a message naming the missing variable.
	Cipher *secrets.Cipher
	Logger *slog.Logger
}

// Descriptor returns the plugin the registry knows under this id.
func (s Plugins) Descriptor(id string) (capability.Plugin, error) {
	descriptor, err := capabilities.Of(s.Registry).Plugin(strings.TrimSpace(id))
	if err != nil {
		return capability.Plugin{}, refuse(http.StatusNotFound, "Unknown plugin", err.Error())
	}
	return descriptor, nil
}

// Set records the instance's settings for a plugin.
func (s Plugins) Set(ctx context.Context, actor, pluginID string, input PluginInput) (capability.Plugin, store.PluginSettings, error) {
	logger := loggerOr(s.Logger)
	descriptor, err := s.Descriptor(pluginID)
	if err != nil {
		return capability.Plugin{}, store.PluginSettings{}, err
	}
	if descriptor.Mandatory && !input.Enabled {
		return descriptor, store.PluginSettings{}, refuse(http.StatusConflict, "Mandatory plugin",
			fmt.Sprintf("the dashboard cannot work without plugin %q", descriptor.ID))
	}

	existing, err := s.Store.PluginSettingsAll(ctx)
	if err != nil {
		return descriptor, store.PluginSettings{}, failure(logger, "Cannot read the plugins", err)
	}
	if input.Enabled {
		if rival := s.rival(descriptor, existing); rival != "" {
			return descriptor, store.PluginSettings{}, refuse(http.StatusConflict, "Another plugin plays this part",
				fmt.Sprintf("plugin %q already brings this instance's tracker or forge: switch it off first", rival))
		}
	}

	config, err := s.merge(descriptor, plugin.ScopeGlobal, input, existing[descriptor.ID].Config, nil)
	if err != nil {
		return descriptor, store.PluginSettings{}, err
	}

	entry := store.PluginSettings{PluginID: descriptor.ID, Enabled: input.Enabled, Config: config}
	if err := s.Store.SetPluginSettings(ctx, entry, actor); err != nil {
		return descriptor, store.PluginSettings{}, failure(logger, "Cannot save", err, "plugin", descriptor.ID)
	}

	// The log carries whether it is enabled, never the configuration: that contains sensitive
	// fields, and an audit log is read with fewer precautions than a database, which is
	// precisely its purpose.
	audit(ctx, logger, s.Store, actor, "plugin.settings", descriptor.ID, map[string]any{"enabled": input.Enabled})
	return descriptor, entry, nil
}

// rival names an enabled plugin bringing the same tracker or forge part as this one.
//
// One of each per instance: a project's releases are tagged through one forge, and its versions
// read from one tracker. Two enabled would leave the choice to the registry's order.
func (s Plugins) rival(descriptor capability.Plugin, settings map[string]store.PluginSettings) string {
	for _, other := range capabilities.Of(s.Registry).Plugins() {
		if other.ID == descriptor.ID || !settings[other.ID].Enabled {
			continue
		}
		if (descriptor.Tracker != nil && other.Tracker != nil) || (descriptor.Forge != nil && other.Forge != nil) {
			return other.ID
		}
	}
	return ""
}

// SetProject records a project's decision about a plugin.
func (s Plugins) SetProject(
	ctx context.Context, actor string, project store.Project, pluginID string, input PluginInput,
) (capability.Plugin, store.PluginSettings, error) {
	logger := loggerOr(s.Logger)
	descriptor, err := s.Descriptor(pluginID)
	if err != nil {
		return capability.Plugin{}, store.PluginSettings{}, err
	}
	if !descriptor.PerProject {
		return descriptor, store.PluginSettings{}, refuse(http.StatusConflict, "Plugin not settable per project",
			"“"+descriptor.Label+"” applies to the whole instance: what it carries "+
				"is not a property of a project.")
	}

	global, err := s.Store.PluginSettingsAll(ctx)
	if err != nil {
		return descriptor, store.PluginSettings{}, failure(logger, "Cannot read the plugins", err)
	}
	if !global[descriptor.ID].Enabled {
		return descriptor, store.PluginSettings{}, refuse(http.StatusConflict, "Plugin disabled on the instance",
			"A project cannot switch back on what the instance switched off: the "+
				"deployment does not carry it, and every call would fail.")
	}

	own, err := s.Store.ProjectPluginSettings(ctx, project.ID)
	if err != nil {
		return descriptor, store.PluginSettings{}, failure(logger, "Cannot read the project's plugins", err)
	}

	// Validation is against the resolved value: a project leaving the URL empty because the
	// instance provides it must not be refused.
	config, err := s.merge(descriptor, plugin.ScopeProject, input, own[descriptor.ID].Config, global[descriptor.ID].Config)
	if err != nil {
		return descriptor, store.PluginSettings{}, err
	}

	entry := store.PluginSettings{PluginID: descriptor.ID, Enabled: input.Enabled, Config: config}
	if err := s.Store.SetProjectPluginSettings(ctx, project.ID, entry, actor); err != nil {
		return descriptor, store.PluginSettings{}, failure(logger, "Cannot save", err,
			"plugin", descriptor.ID, "project", project.Slug)
	}

	audit(ctx, logger, s.Store, actor, "plugin.project.settings", project.Slug+"/"+descriptor.ID,
		map[string]any{"enabled": input.Enabled})
	return descriptor, entry, nil
}

// ClearProject returns a project to inheriting the instance's setting.
//
// Distinct from disabling: "I have nothing particular to say" is not "no". Confusing them would
// mean a project, once departed from the common setting, could never follow it again.
func (s Plugins) ClearProject(ctx context.Context, actor string, project store.Project, pluginID string) (capability.Plugin, error) {
	logger := loggerOr(s.Logger)
	descriptor, err := s.Descriptor(pluginID)
	if err != nil {
		return capability.Plugin{}, err
	}
	if err := s.Store.ClearProjectPluginSettings(ctx, project.ID, descriptor.ID); err != nil {
		return descriptor, failure(logger, "Cannot remove", err, "plugin", descriptor.ID, "project", project.Slug)
	}
	audit(ctx, logger, s.Store, actor, "plugin.project.clear", project.Slug+"/"+descriptor.ID, nil)
	return descriptor, nil
}

// merge composes the configuration to write from the input and what exists.
//
// Three rules, each correcting a way of losing a value:
//
//   - a sensitive field absent from the input is kept: the screen never receives it, so it
//     cannot send it back;
//   - a sensitive field explicitly cleared is cleared, which requires a distinct gesture
//     rather than a field left empty;
//   - a sensitive field supplied is sealed, never written as is.
func (s Plugins) merge(
	descriptor capability.Plugin, scope plugin.Scope, input PluginInput, existing, inherited map[string]string,
) (map[string]string, error) {
	typed := plugin.Values{}
	maps.Copy(typed, input.Config)

	if err := plugin.Validate(descriptor, scope, typed, plugin.Values(inherited)); err != nil {
		return nil, invalid("Invalid settings", err)
	}

	config := map[string]string{}
	// Sensitive fields already stored are carried over as they are, sealed, without being
	// opened: reopening them to reseal would move plaintext through a process that does not
	// need it.
	for name, value := range existing {
		if plugin.IsSecret(descriptor, scope, name) {
			config[name] = value
		}
	}
	for _, name := range input.ClearedSecrets {
		delete(config, name)
	}

	for name, value := range typed {
		if !plugin.IsSecret(descriptor, scope, name) {
			config[name] = value
			continue
		}
		if strings.TrimSpace(value) == "" {
			// Empty means "unchanged". Clearing goes through ClearedSecrets.
			continue
		}
		if !s.Cipher.Configured() {
			return nil, refuse(http.StatusConflict, "Sealing not configured",
				"SECRETS_KEY is not set: the dashboard refuses to write "+
					"a sensitive field in clear text. Produce it with "+
					"“openssl rand -base64 32”, then declare it in the deployment.")
		}
		sealed, err := s.Cipher.Seal(value)
		if err != nil {
			return nil, failure(loggerOr(s.Logger), "Cannot seal", err, "plugin", descriptor.ID, "field", name)
		}
		config[name] = sealed
	}

	return config, nil
}
