package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PluginSource is what an installed plugin follows, and what the last check found there.
type PluginSource struct {
	PluginID string
	// Repository is `registry/repository`, with neither tag nor digest.
	Repository string
	// Pinned keeps the plugin on its version: a newer one is reported, never offered.
	Pinned bool
	// ActiveVersion and ActiveID are the installed version running, empty when none is active.
	ActiveVersion string
	ActiveID      int64
	// LatestVersion and LatestDigest are the highest version above the active one the last check
	// found; empty when there was none.
	LatestVersion string
	LatestDigest  string
	CheckedAt     *time.Time
	CheckError    string
	// CheckRequested says an administrator asked for a check before the next round.
	CheckRequested bool
}

// ErrSourceNotFound reports a plugin with no recorded source: not installed.
var ErrSourceNotFound = errors.New("no source recorded for this plugin")

const sourceColumns = `s.plugin_id, s.repository, s.pinned, coalesce(i.version, ''), coalesce(i.id, 0),
	s.latest_version, s.latest_digest, s.checked_at, s.check_error, s.check_requested_at is not null`

const sourceFrom = ` from plugin_sources s
	left join plugin_installations i on i.plugin_id = s.plugin_id and i.active`

func scanSource(row pgx.Row) (PluginSource, error) {
	var source PluginSource
	err := row.Scan(&source.PluginID, &source.Repository, &source.Pinned, &source.ActiveVersion, &source.ActiveID,
		&source.LatestVersion, &source.LatestDigest, &source.CheckedAt, &source.CheckError, &source.CheckRequested)
	return source, err
}

// PluginSources lists every plugin's source, by plugin.
func (db *DB) PluginSources(ctx context.Context) ([]PluginSource, error) {
	return db.sources(ctx, `select `+sourceColumns+sourceFrom+` order by s.plugin_id`)
}

// PluginSource returns one plugin's source.
func (db *DB) PluginSource(ctx context.Context, pluginID string) (PluginSource, error) {
	source, err := scanSource(db.pool.QueryRow(ctx, `select `+sourceColumns+sourceFrom+` where s.plugin_id = $1`, pluginID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PluginSource{}, ErrSourceNotFound
	}
	if err != nil {
		return PluginSource{}, fmt.Errorf("source of plugin %q: %w", pluginID, err)
	}
	return source, nil
}

// DueUpdateChecks returns the sources to check: those never checked, checked longer ago than the
// interval, or whose check an administrator asked for.
func (db *DB) DueUpdateChecks(ctx context.Context, interval time.Duration) ([]PluginSource, error) {
	return db.sources(ctx, `select `+sourceColumns+sourceFrom+`
		where s.check_requested_at is not null or s.checked_at is null
		   or s.checked_at < now() - make_interval(secs => $1)
		order by s.plugin_id`, interval.Seconds())
}

func (db *DB) sources(ctx context.Context, query string, args ...any) ([]PluginSource, error) {
	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("plugin sources: %w", err)
	}
	defer rows.Close()
	var sources []PluginSource
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("plugin sources: %w", err)
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// SetSourcePinned pins a plugin to its version, or makes it follow its repository. Nothing is
// installed: following only makes a newer version offered.
func (db *DB) SetSourcePinned(ctx context.Context, pluginID string, pinned bool) error {
	tag, err := db.pool.Exec(ctx, `update plugin_sources set pinned = $2 where plugin_id = $1`, pluginID, pinned)
	if err != nil {
		return fmt.Errorf("pinning plugin %q: %w", pluginID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSourceNotFound
	}
	return nil
}

// RequestUpdateCheck asks for a check of a plugin's repository before the next round.
func (db *DB) RequestUpdateCheck(ctx context.Context, pluginID string) error {
	tag, err := db.pool.Exec(ctx, `update plugin_sources set check_requested_at = now() where plugin_id = $1`, pluginID)
	if err != nil {
		return fmt.Errorf("requesting a check of plugin %q: %w", pluginID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSourceNotFound
	}
	return nil
}

// UpdateCheck is what a check of a repository found.
type UpdateCheck struct {
	// LatestVersion and LatestDigest are the newer version, both empty when there is none.
	LatestVersion, LatestDigest string
	// Error is why the repository could not be read; the previous finding is then kept.
	Error string
}

// RecordUpdateCheck records a check, and answers the request that asked for it.
//
// A failed check keeps what the previous one found: an unreachable registry does not make an
// available version disappear from the screen.
func (db *DB) RecordUpdateCheck(ctx context.Context, pluginID string, check UpdateCheck) error {
	query := `update plugin_sources set latest_version = $2, latest_digest = $3, check_error = '',
		checked_at = now(), check_requested_at = null where plugin_id = $1`
	args := []any{pluginID, check.LatestVersion, check.LatestDigest}
	if check.Error != "" {
		query = `update plugin_sources set check_error = $2, checked_at = now(), check_requested_at = null
			where plugin_id = $1`
		args = []any{pluginID, check.Error}
	}
	if _, err := db.pool.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("recording the check of plugin %q: %w", pluginID, err)
	}
	return nil
}
