package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

/*
 * Les réglages des plugins, aux deux portées.
 *
 * Le registre vit dans le binaire (`internal/plugin`) : ce magasin ne sait pas
 * quels plugins existent, et ne cherche pas à le savoir. Il porte ce que
 * l'exploitation en a décidé — activé ou non, réglé ainsi — pour des
 * identifiants que l'appelant lui donne.
 *
 * La séparation est volontaire. Une table qui énumérerait les plugins connus
 * demanderait une migration à chaque ajout, alors qu'ajouter un plugin est déjà
 * une reconstruction du binaire : la base apprendrait une seconde fois ce que le
 * code sait déjà, avec une occasion de plus de diverger.
 */

// PluginSettings carries a plugin's state at one scope.
type PluginSettings struct {
	PluginID string
	Enabled  bool
	// Config is a flat map of strings. Sensitive fields in it are sealed: this store
	// carries them without reading them, and the layer above opens and closes them.
	Config    map[string]string
	UpdatedAt time.Time
	// UpdatedBy is empty for what the bootstrap set. The distinction says a value comes
	// from the deployment's configuration and not from a human decision one could go and
	// question.
	UpdatedBy string
}

// EnsurePlugins writes the missing rows for the embedded plugins.
//
// Called at start-up, with the registry's identifiers. The rows are born enabled: every
// embedded plugin describes something the dashboard already does, and being born
// disabled would stop, at the first deployment, mechanisms that worked the day before.
//
// Idempotent, and without overwriting: a plugin disabled by hand stays disabled after a
// restart, otherwise the setting would not outlive a pod.
func (db *DB) EnsurePlugins(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	const query = `
		insert into plugin_settings (plugin_id, enabled)
		select unnest($1::text[]), true
		on conflict (plugin_id) do nothing`

	if _, err := db.pool.Exec(ctx, query, ids); err != nil {
		return fmt.Errorf("bootstrapping the plugins: %w", err)
	}
	return nil
}

// PluginSettingsAll returns the global settings, by identifier.
//
// A map and not a slice: the caller crosses them with the binary's registry, which is
// authoritative about what exists and in what order to display it. Returning a slice
// ordered by the database would invite using it as a catalogue, while it knows only
// what has already been set.
func (db *DB) PluginSettingsAll(ctx context.Context) (map[string]PluginSettings, error) {
	const query = `
		select plugin_id, enabled, config, updated_at, updated_by
		from plugin_settings`

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading the plugins: %w", err)
	}
	defer rows.Close()

	settings := map[string]PluginSettings{}
	for rows.Next() {
		var entry PluginSettings
		if err := rows.Scan(&entry.PluginID, &entry.Enabled, &entry.Config,
			&entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("reading a plugin: %w", err)
		}
		settings[entry.PluginID] = entry
	}
	return settings, rows.Err()
}

// SetPluginSettings records a plugin's global settings.
func (db *DB) SetPluginSettings(ctx context.Context, entry PluginSettings, actor string) error {
	const query = `
		insert into plugin_settings (plugin_id, enabled, config, updated_at, updated_by)
		values ($1, $2, $3, now(), $4)
		on conflict (plugin_id) do update
		set enabled    = excluded.enabled,
		    config     = excluded.config,
		    updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by`

	if _, err := db.pool.Exec(ctx, query, entry.PluginID, entry.Enabled,
		orEmptyConfig(entry.Config), actor); err != nil {
		return fmt.Errorf("recording plugin %q: %w", entry.PluginID, err)
	}
	return nil
}

// ProjectPluginSettings returns a project's explicit decisions.
//
// Only the plugins the project has pronounced on appear. The absence of an entry means
// inheritance from the global scope, and not refusal: treating it as refusal would
// disable at a stroke, for every project declared before these tables existed, what
// they receive today.
func (db *DB) ProjectPluginSettings(ctx context.Context, projectID int64) (map[string]PluginSettings, error) {
	const query = `
		select plugin_id, enabled, config, updated_at, updated_by
		from project_plugins
		where project_id = $1`

	rows, err := db.pool.Query(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("reading the project's plugins: %w", err)
	}
	defer rows.Close()

	settings := map[string]PluginSettings{}
	for rows.Next() {
		var entry PluginSettings
		if err := rows.Scan(&entry.PluginID, &entry.Enabled, &entry.Config,
			&entry.UpdatedAt, &entry.UpdatedBy); err != nil {
			return nil, fmt.Errorf("reading a project plugin: %w", err)
		}
		settings[entry.PluginID] = entry
	}
	return settings, rows.Err()
}

// SetProjectPluginSettings records a project's decision on a plugin.
func (db *DB) SetProjectPluginSettings(ctx context.Context, projectID int64,
	entry PluginSettings, actor string,
) error {
	const query = `
		insert into project_plugins (project_id, plugin_id, enabled, config, updated_at, updated_by)
		values ($1, $2, $3, $4, now(), $5)
		on conflict (project_id, plugin_id) do update
		set enabled    = excluded.enabled,
		    config     = excluded.config,
		    updated_at = excluded.updated_at,
		    updated_by = excluded.updated_by`

	_, err := db.pool.Exec(ctx, query, projectID, entry.PluginID, entry.Enabled,
		orEmptyConfig(entry.Config), actor)
	if err != nil {
		return fmt.Errorf("recording plugin %q of project %d: %w",
			entry.PluginID, projectID, err)
	}
	return nil
}

// ClearProjectPluginSettings removes a project's decision.
//
// The project returns to inheriting the global setting. Distinct from disabling: "I
// have nothing particular to say" is not "no", and confusing them would mean a project
// could never follow the instance's setting again once it had departed from it.
func (db *DB) ClearProjectPluginSettings(ctx context.Context, projectID int64, pluginID string) error {
	const query = `delete from project_plugins where project_id = $1 and plugin_id = $2`

	if _, err := db.pool.Exec(ctx, query, projectID, pluginID); err != nil {
		return fmt.Errorf("removing plugin %q from project %d: %w", pluginID, projectID, err)
	}
	return nil
}

// ErrPluginDisabled reports a plugin queried while it is switched off.
var ErrPluginDisabled = errors.New("plugin disabled")

// ResolvedPlugin returns a plugin's effective settings for a project.
//
// The computation is done here, in one query, rather than by the caller from two reads:
// it is the layering every outgoing call depends on, and two places computing it would
// end up computing it differently.
//
// The configuration returned stays sealed for sensitive fields. Opening them needs the
// key, and this store does not have it — deliberately: it must not be able to return
// plaintext, even by a mistaken call.
func (db *DB) ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (PluginSettings, error) {
	const query = `
		select g.enabled,
		       g.config,
		       p.enabled,
		       p.config
		from plugin_settings g
		left join project_plugins p
		       on p.plugin_id = g.plugin_id and p.project_id = $2
		where g.plugin_id = $1`

	var (
		globalEnabled  bool
		globalConfig   map[string]string
		projectEnabled *bool
		projectConfig  map[string]string
	)
	err := db.pool.QueryRow(ctx, query, pluginID, projectID).
		Scan(&globalEnabled, &globalConfig, &projectEnabled, &projectConfig)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No global settings: the plugin was never bootstrapped on this instance. Treated as
		// switched off, which it is in fact.
		return PluginSettings{PluginID: pluginID}, fmt.Errorf("%w: %s",
			ErrPluginDisabled, pluginID)
	case err != nil:
		return PluginSettings{}, fmt.Errorf("reading plugin %q: %w", pluginID, err)
	}

	// A project cannot switch back on what the instance switched off: a plugin disabled
	// globally is disabled because the deployment does not carry it, and a project forcing
	// it would fail on every call.
	enabled := globalEnabled
	if globalEnabled && projectEnabled != nil {
		enabled = *projectEnabled
	}

	resolved := PluginSettings{
		PluginID: pluginID,
		Enabled:  enabled,
		Config:   overlayConfig(globalConfig, projectConfig),
	}
	if !enabled {
		return resolved, fmt.Errorf("%w: %s", ErrPluginDisabled, pluginID)
	}
	return resolved, nil
}

// overlayConfig layers a project's settings over the instance's.
//
// An empty value on the project side does not erase the instance's: it means "nothing
// particular". Confusing it with a deletion would lose a URL the first time a form that
// did not display it was saved — a certainty for secret fields, which the API never
// returns.
func overlayConfig(global, project map[string]string) map[string]string {
	resolved := make(map[string]string, len(global)+len(project))
	for name, value := range global {
		resolved[name] = value
	}
	for name, value := range project {
		if value == "" {
			continue
		}
		resolved[name] = value
	}
	return resolved
}

// seedPlugins moves plugin configuration from the environment into the database, once.
//
// Two gestures, and the second is the one that counts. The instance's settings are born
// from the deployment's configuration — a tracker's URL and key, the forge, the Flux
// namespace. Then every project already attached to a tracker receives its own setting,
// with the identifier it already carried.
//
// Without that second gesture, an existing deployment would see its synchronisation
// stop the day the code reads settings rather than configuration: the connection would
// be there, but no project would say what to synchronise. A silent failure.
//
// Nothing is overwritten: `on conflict do nothing`. A setting already made — through the
// interface, or by an earlier pass — beats what the configuration suggests.
func (db *DB) seedPlugins(ctx context.Context, settings []PluginSettings, trackerID string) (int, error) {
	posed := 0

	for _, entry := range settings {
		const query = `
			insert into plugin_settings (plugin_id, enabled, config)
			values ($1, $2, $3)
			on conflict (plugin_id) do nothing`

		tag, err := db.pool.Exec(ctx, query, entry.PluginID, entry.Enabled,
			orEmptyConfig(entry.Config))
		if err != nil {
			return posed, fmt.Errorf("bootstrapping plugin %q: %w", entry.PluginID, err)
		}
		posed += int(tag.RowsAffected())
	}

	if strings.TrimSpace(trackerID) == "" {
		return posed, nil
	}

	// Attaching the projects. `tracker_project` stays the source: the column exists, it is
	// right, and copying it into the setting is what lets a second project name another
	// server without the first moving.
	const attach = `
		insert into project_plugins (project_id, plugin_id, enabled, config)
		select id, $1, true, jsonb_build_object('project', tracker_project)
		from projects
		where btrim(tracker_project) <> ''
		on conflict (project_id, plugin_id) do nothing`

	tag, err := db.pool.Exec(ctx, attach, trackerID)
	if err != nil {
		return posed, fmt.Errorf("attaching the projects to the tracker: %w", err)
	}
	return posed + int(tag.RowsAffected()), nil
}

// orEmptyConfig avoids writing `null` into a column declared not null.
func orEmptyConfig(config map[string]string) map[string]string {
	if config == nil {
		return map[string]string{}
	}
	return config
}
