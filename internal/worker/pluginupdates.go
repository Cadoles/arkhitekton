package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// UpdateCheckStore is what checking plugins for updates needs from the database.
type UpdateCheckStore interface {
	DueUpdateChecks(ctx context.Context, interval time.Duration) ([]store.PluginSource, error)
	RecordUpdateCheck(ctx context.Context, pluginID string, check store.UpdateCheck) error
}

// LatestRelease finds the highest version a repository publishes above current.
type LatestRelease func(ctx context.Context, repository, current string) (installed.Release, bool, error)

// PluginUpdateChecker reads the repositories of the installed plugins for a newer version.
//
// It reads tags and manifests, nothing more: the plugin itself is pulled only when an administrator
// updates. A pinned plugin is checked too, so that the screen can say it is behind.
type PluginUpdateChecker struct {
	store    UpdateCheckStore
	latest   LatestRelease
	interval time.Duration
	// poll is how soon a check an administrator asked for is served.
	poll   time.Duration
	logger *slog.Logger
}

func NewPluginUpdateChecker(sources UpdateCheckStore, latest LatestRelease, interval time.Duration, logger *slog.Logger) *PluginUpdateChecker {
	if interval < time.Minute {
		interval = time.Minute
	}
	return &PluginUpdateChecker{store: sources, latest: latest, interval: interval, poll: 5 * time.Second, logger: logger}
}

// Run checks what is due until the context ends.
func (c *PluginUpdateChecker) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		c.Pass(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Pass checks every source due: never checked, checked longer ago than the interval, or asked for.
func (c *PluginUpdateChecker) Pass(ctx context.Context) {
	due, err := c.store.DueUpdateChecks(ctx, c.interval)
	if err != nil {
		c.logger.Error("plugin update checks", "error", err)
		return
	}
	for _, source := range due {
		// No active version, nothing to compare: an installation is under way, or was removed.
		if source.ActiveVersion == "" {
			continue
		}
		checking, cancel := context.WithTimeout(ctx, time.Minute)
		release, found, err := c.latest(checking, source.Repository, source.ActiveVersion)
		cancel()
		check := store.UpdateCheck{}
		switch {
		case err != nil:
			check.Error = err.Error()
			c.logger.Warn("plugin update check", "plugin", source.PluginID, "repository", source.Repository, "error", err)
		case found:
			check.LatestVersion, check.LatestDigest = release.Version, release.Digest
			c.logger.Info("plugin update available", "plugin", source.PluginID,
				"version", source.ActiveVersion, "available", release.Version, "pinned", source.Pinned)
		}
		if err := c.store.RecordUpdateCheck(ctx, source.PluginID, check); err != nil {
			c.logger.Error("recording a plugin update check", "plugin", source.PluginID, "error", err)
		}
	}
}
