package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/redmine"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/worker"
)

/*
 * The bridge between the deployment's configuration and the plugins' settings.
 *
 * The same act as `SeedProjects` before it, and for the same reason: what lived in environment
 * variables is now set from the interface, and the database must describe what the dashboard
 * **actually** does rather than what an administrator will have thought to retype into it.
 *
 * Applied once. The database is then authoritative: a setting corrected in the interface is
 * not overwritten by a restart, and an environment variable forgotten in the deployment does
 * not come back through it.
 */

// The identifiers are named here rather than written literally at the points of use: a typo
// would produce a setting no plugin reads back, and the screen would say "recorded" on a dead
// value.
const (
	trackerPluginID = "redmine"
	forgePluginID   = "gitea"
)

// pluginIDs lists the embedded plugins, for seeding the missing rows.
func pluginIDs() []string {
	registry := plugin.Registry()
	ids := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		ids = append(ids, descriptor.ID)
	}
	return ids
}

// openCipher builds the cipher for the sensitive settings.
//
// Its absence does not prevent start-up, deliberately: a deployment that has no secret to
// protect yet must not be punished, and the message would be read far from whoever can act.
// The refusal happens at write time, in the screen where the field is typed.
//
// A **malformed** key, on the other hand, is a refusal to start: it says someone believed
// they had configured the sealing, and letting it through would write in clear text what they
// thought protected.
func openCipher(cfg config.Config, logger *slog.Logger) (*secrets.Cipher, error) {
	if !cfg.Secrets.Configured() {
		logger.Warn("settings sealing not configured",
			"reason", "SECRETS_KEY missing",
			"consequence", "the plugins' sensitive fields cannot be recorded",
			"remede", "openssl rand -base64 32")
		return nil, nil
	}

	cipher, err := secrets.New(cfg.Secrets.Key, cfg.Secrets.RetiredKeys...)
	if err != nil {
		return nil, fmt.Errorf("settings sealing: %w", err)
	}
	if len(cfg.Secrets.RetiredKeys) > 0 {
		logger.Info("key rotation in progress",
			"retirees", len(cfg.Secrets.RetiredKeys),
			"note", "the values are resealed as they are written")
	}
	return cipher, nil
}

// configuredPlugins derives the instance's settings from the configuration.
//
// Only the plugins whose configuration says something are set. The others — the attestation,
// the delivery note — have no setting: their rows are born enabled by `EnsurePlugins`, which
// preserves exactly the previous behaviour, where they applied without anyone having to ask.
func configuredPlugins(cfg config.Config, cipher *secrets.Cipher, logger *slog.Logger) ([]store.PluginSettings, error) {
	var settings []store.PluginSettings

	tracker := map[string]string{}
	if url := strings.TrimSpace(cfg.Tracker.URL); url != "" {
		tracker["url"] = url
	}
	if key := strings.TrimSpace(cfg.Tracker.APIKey); key != "" {
		sealed, err := seal(cipher, key, "the tracker's API key")
		if err != nil {
			return nil, err
		}
		tracker["api_key"] = sealed
	}
	if len(tracker) > 0 {
		settings = append(settings, store.PluginSettings{
			PluginID: trackerPluginID, Enabled: true, Config: tracker,
		})
	}

	forge := map[string]string{}
	if url := strings.TrimSpace(cfg.Forge.URL); url != "" {
		forge["url"] = url
	}
	if token := strings.TrimSpace(cfg.Forge.Token); token != "" {
		sealed, err := seal(cipher, token, "the forge's token")
		if err != nil {
			return nil, err
		}
		forge["token"] = sealed
	}
	if len(forge) > 0 {
		settings = append(settings, store.PluginSettings{
			PluginID: forgePluginID, Enabled: true, Config: forge,
		})
	}

	if namespace := strings.TrimSpace(cfg.Flux.Namespace); namespace != "" {
		settings = append(settings, store.PluginSettings{
			PluginID: plugin.ModuleMSE, Enabled: true,
			Config: map[string]string{"namespace": namespace},
		})
	}

	// The review statuses accompany the tracker, but **per project**: the vocabulary belongs to
	// the client, and two clients do not have the same. They are therefore not set globally — the
	// seeding leaves them empty, and the project declares them. The code's default value keeps
	// applying in the meantime.
	if len(cfg.Tracker.ReviewStatuses) > 0 && cfg.Tracker.Configured() {
		logger.Info("review statuses to redeclare per project",
			"statuses", cfg.Tracker.ReviewStatuses,
			"reason", "the vocabulary belongs to the client, not to the instance")
	}

	return settings, nil
}

// trackerConnector builds a tracker client for a resolved connection.
//
// The call timeout stays a value of the deployment: it bounds the effect of a slow tracker on
// the worker, which is a property of the dashboard and not of the client. The rest — server,
// key, project — comes from the settings.
func trackerConnector(cfg config.Config) worker.TrackerConnector {
	return func(connection worker.TrackerConnection) (worker.TrackerReader, error) {
		if strings.TrimSpace(connection.URL) == "" || strings.TrimSpace(connection.APIKey) == "" {
			// Named rather than left to the client: "invalid URL" would send someone hunting for a typo
			// where it is a setting that was never filled in.
			return nil, fmt.Errorf(
				"tracker not configured: set the URL and the API key of plugin %q, "+
					"on the instance or on the project", trackerPluginID)
		}
		return redmine.New(redmine.Options{
			URL: connection.URL, APIKey: connection.APIKey,
			Timeout: cfg.Tracker.Timeout,
		})
	}
}

// seal seals a value, or refuses the seeding for want of a key.
//
// Refuse rather than write in clear text, and refuse rather than skip the value: a seeding
// that sets the URL without the key would produce a half-configured tracker, which fails at
// every call with an authentication message. The step is marked as done and will not be
// replayed — hence the outright failure.
func seal(cipher *secrets.Cipher, value, what string) (string, error) {
	if !cipher.Configured() {
		return "", fmt.Errorf(
			"SECRETS_KEY is required to seed %s: declare it "+
				"(openssl rand -base64 32) then run the migration again", what)
	}
	sealed, err := cipher.Seal(value)
	if err != nil {
		return "", fmt.Errorf("sealing %s: %w", what, err)
	}
	return sealed, nil
}

// secretResolver opens a sensitive field named by an action step.
//
// The designation is written `plugin.field` — `redmine.api_key`. It is the only path by which
// a secret reaches an outgoing call: it passes through no template, no body, no log.
type secretResolver struct {
	store  *store.DB
	cipher *secrets.Cipher
}

// ResolveSecret returns the clear-text value of a named field.
//
// The error message never quotes the value — it is precisely the one that could not be opened
// — and names the designation, which is public.
func (r secretResolver) ResolveSecret(
	ctx context.Context, projectID *int64, reference string,
) (string, error) {
	pluginID, field, found := strings.Cut(reference, ".")
	if !found || pluginID == "" || field == "" {
		return "", fmt.Errorf("designation %q expected in the form `plugin.field`", reference)
	}

	descriptor, err := plugin.Get(pluginID)
	if err != nil {
		return "", err
	}

	// A field **not declared sensitive** is refused rather than opened. Without this check, a
	// designation could extract an ordinary value through a path designed for secrets — and the
	// screen would show nothing.
	scope := plugin.ScopeProject
	if projectID == nil {
		scope = plugin.ScopeGlobal
	}
	if !descriptor.IsSecret(scope, field) && !descriptor.IsSecret(plugin.ScopeGlobal, field) {
		return "", fmt.Errorf("the field %q of plugin %q is not declared sensitive",
			field, pluginID)
	}

	if projectID == nil {
		return "", fmt.Errorf("designation %q outside a project", reference)
	}

	settings, err := r.store.ResolvedPlugin(ctx, *projectID, pluginID)
	if err != nil {
		return "", err
	}
	return r.cipher.Open(settings.Config[field])
}
