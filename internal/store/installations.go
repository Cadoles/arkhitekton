package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// States of a plugin installation.
const (
	InstallationPending   = "pending"
	InstallationInstalled = "installed"
	InstallationFailed    = "failed"
	InstallationRemoved   = "removed"
)

// ErrInstallationNotFound reports a missing installation.
var ErrInstallationNotFound = errors.New("plugin installation not found")

// ErrVersionInstalled refuses a second installation of a version already installed.
var ErrVersionInstalled = errors.New("this version of the plugin is already installed")

// PluginInstallation is a plugin requested by reference, then pulled, verified and stored.
type PluginInstallation struct {
	ID int64
	// Reference is the repository and digest the administrator gave.
	Reference string
	Digest    string
	// PluginID, Version, SignedBy and Object are known once the plugin is pulled.
	PluginID string
	Version  string
	// SignedBy is the fingerprint of the trusted key that signed it.
	SignedBy string
	// Object is the stored layer, the plugin's directory as signed.
	Object      string
	State       string
	Error       string
	Active      bool
	RequestedBy string
	RequestedAt time.Time
	InstalledAt *time.Time
	RemovedAt   *time.Time
}

const installationColumns = `id, reference, digest, coalesce(plugin_id, ''), coalesce(version, ''),
	coalesce(signed_by, ''), coalesce(object, ''), state, error, active, requested_by, requested_at,
	installed_at, removed_at`

func scanInstallation(row pgx.Row) (PluginInstallation, error) {
	var installation PluginInstallation
	err := row.Scan(&installation.ID, &installation.Reference, &installation.Digest, &installation.PluginID,
		&installation.Version, &installation.SignedBy, &installation.Object, &installation.State,
		&installation.Error, &installation.Active, &installation.RequestedBy, &installation.RequestedAt,
		&installation.InstalledAt, &installation.RemovedAt)
	return installation, err
}

// RequestPluginInstallation records a reference to pull. The job that pulls it completes the row.
func (db *DB) RequestPluginInstallation(ctx context.Context, reference, digest, actor string) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx, `insert into plugin_installations (reference, digest, requested_by)
		values ($1, $2, $3) returning id`, reference, digest, actor).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("requesting a plugin installation: %w", err)
	}
	return id, nil
}

// PluginInstallation returns one installation.
func (db *DB) PluginInstallation(ctx context.Context, id int64) (PluginInstallation, error) {
	installation, err := scanInstallation(db.pool.QueryRow(ctx,
		`select `+installationColumns+` from plugin_installations where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return PluginInstallation{}, ErrInstallationNotFound
	}
	if err != nil {
		return PluginInstallation{}, fmt.Errorf("plugin installation %d: %w", id, err)
	}
	return installation, nil
}

// PluginInstallations lists every installation, the most recent first, removed ones included: the
// screen shows what was installed and by whom.
func (db *DB) PluginInstallations(ctx context.Context) ([]PluginInstallation, error) {
	return db.installations(ctx, `select `+installationColumns+` from plugin_installations order by id desc`)
}

// InstalledPlugins lists the installations the capabilities in force are built from.
func (db *DB) InstalledPlugins(ctx context.Context) ([]PluginInstallation, error) {
	return db.installations(ctx, `select `+installationColumns+` from plugin_installations
		where state = 'installed' order by plugin_id, id`)
}

func (db *DB) installations(ctx context.Context, query string) ([]PluginInstallation, error) {
	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("plugin installations: %w", err)
	}
	defer rows.Close()
	var installations []PluginInstallation
	for rows.Next() {
		installation, err := scanInstallation(rows)
		if err != nil {
			return nil, fmt.Errorf("plugin installations: %w", err)
		}
		installations = append(installations, installation)
	}
	return installations, rows.Err()
}

// CompletedInstallation is a plugin pulled and verified.
type CompletedInstallation struct {
	PluginID, Version, SignedBy, Object string
	// SwitchedOff installs the plugin switched off for the instance: a plugin bringing a tracker or
	// a forge, of which one each is switched on.
	SwitchedOff bool
}

// CompletePluginInstallation records a plugin pulled and verified. The first version of a plugin
// becomes active; a later one waits for an administrator to activate it, so that an update is a
// decision and not a side effect of pulling.
func (db *DB) CompletePluginInstallation(
	ctx context.Context, id int64, completed CompletedInstallation,
) (activated bool, err error) {
	pluginID, version, signedBy, object := completed.PluginID, completed.Version, completed.SignedBy, completed.Object
	err = db.changeCapabilitiesWith(ctx, func(transaction pgx.Tx) error {
		tag, err := transaction.Exec(ctx, `update plugin_installations
			set plugin_id = $2, version = $3, signed_by = $4, object = $5, state = 'installed', error = '',
			    installed_at = now(),
			    active = not exists (select 1 from plugin_installations where plugin_id = $2 and active)
			where id = $1 and state = 'pending'`, id, pluginID, version, signedBy, object)
		var violation *pgconn.PgError
		if errors.As(err, &violation) && violation.Code == "23505" {
			return ErrVersionInstalled
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("installation %d: not pending", id)
		}
		// The plugin is switched on for the instance, as a built-in plugin is at its first start.
		if _, err := transaction.Exec(ctx, `insert into plugin_settings (plugin_id, enabled)
			values ($1, $2) on conflict (plugin_id) do nothing`, pluginID, !completed.SwitchedOff); err != nil {
			return err
		}
		return transaction.QueryRow(ctx, `select active from plugin_installations where id = $1`, id).Scan(&activated)
	})
	return activated, err
}

// FailPluginInstallation records why a requested plugin was not installed.
func (db *DB) FailPluginInstallation(ctx context.Context, id int64, reason string) error {
	_, err := db.pool.Exec(ctx, `update plugin_installations set state = 'failed', error = $2
		where id = $1 and state = 'pending'`, id, reason)
	if err != nil {
		return fmt.Errorf("failing installation %d: %w", id, err)
	}
	return nil
}

// ActivatePluginInstallation makes a version the one new runs are launched with.
func (db *DB) ActivatePluginInstallation(ctx context.Context, id int64) error {
	return db.changeCapabilitiesWith(ctx, func(transaction pgx.Tx) error {
		var pluginID, state string
		err := transaction.QueryRow(ctx, `select coalesce(plugin_id, ''), state from plugin_installations
			where id = $1 for update`, id).Scan(&pluginID, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInstallationNotFound
		}
		if err != nil {
			return err
		}
		if state != InstallationInstalled {
			return fmt.Errorf("installation %d is %s, not installed", id, state)
		}
		if _, err := transaction.Exec(ctx, `update plugin_installations set active = false
			where plugin_id = $1 and active and id <> $2`, pluginID, id); err != nil {
			return err
		}
		_, err = transaction.Exec(ctx, `update plugin_installations set active = true where id = $1`, id)
		return err
	})
}

// RemovePluginInstallation removes a version. Removing the active one uninstalls the plugin: every
// version, its bindings and its settings go with it. The facts its runs produced stay: they carry
// their producer and are shown without the plugin. It returns the plugin uninstalled, if any.
func (db *DB) RemovePluginInstallation(ctx context.Context, id int64) (uninstalled string, err error) {
	err = db.changeCapabilitiesWith(ctx, func(transaction pgx.Tx) error {
		var pluginID string
		var active bool
		err := transaction.QueryRow(ctx, `select coalesce(plugin_id, ''), active from plugin_installations
			where id = $1 and state <> 'removed' for update`, id).Scan(&pluginID, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInstallationNotFound
		}
		if err != nil {
			return err
		}
		if !active {
			_, err := transaction.Exec(ctx, `update plugin_installations
				set state = 'removed', removed_at = now() where id = $1`, id)
			return err
		}
		uninstalled = pluginID
		for _, statement := range []string{
			`update plugin_installations set state = 'removed', active = false, removed_at = now()
				where plugin_id = $1 and state = 'installed'`,
			`delete from capability_bindings where split_part(ref, '/', 1) = $1`,
			`delete from project_plugins where plugin_id = $1`,
			`delete from plugin_settings where plugin_id = $1`,
		} {
			if _, err := transaction.Exec(ctx, statement, pluginID); err != nil {
				return err
			}
		}
		return nil
	})
	return uninstalled, err
}
