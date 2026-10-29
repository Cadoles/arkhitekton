package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// KindPluginPurge is the `kind` of the jobs that purge the tables an uninstalled plugin left.
//
// A job of the worker's, as an installation is: the plugin's migrations run under the lease, and
// undoing them is no different.
const KindPluginPurge = "plugin-purge"

// ParamPlugin names the plugin a purge concerns.
const ParamPlugin = "plugin"

// PurgeStore is what a purge needs from the database.
type PurgeStore interface {
	PluginLeftoverOf(ctx context.Context, pluginID string) (store.PluginLeftover, error)
	RecordPluginPurged(ctx context.Context, pluginID string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// Layers reads a stored plugin layer.
type Layers interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// PluginPurger undoes an uninstalled plugin's migrations with the layer of the version that was
// active, then drops its version table.
//
// What goes is the plugin's own tables. The facts its runs produced, its jobs and the audit stay:
// they are the core's, and are shown without the plugin.
type PluginPurger struct {
	store  PurgeStore
	layers Layers
	// purge undoes a plugin's migrations and drops its version table.
	purge  func(ctx context.Context, plugin installed.Plugin) error
	logger *slog.Logger
}

func NewPluginPurger(
	purges PurgeStore, layers Layers, purge func(ctx context.Context, plugin installed.Plugin) error,
	logger *slog.Logger,
) *PluginPurger {
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginPurger{store: purges, layers: layers, purge: purge, logger: logger}
}

// Kind returns the `kind` this purger handles.
func (p *PluginPurger) Kind() string { return KindPluginPurge }

// Run purges the plugin a job names; a failure is audited next to the request.
func (p *PluginPurger) Run(ctx context.Context, job store.Job) error {
	pluginID := job.Params[ParamPlugin]
	if pluginID == "" {
		return fmt.Errorf("job %d names no plugin", job.ID)
	}
	leftover, err := p.purgeOne(ctx, pluginID)
	if err != nil {
		if auditErr := p.store.Audit(ctx, job.RequestedBy, "plugin.purge.failed", pluginID,
			map[string]any{"job": job.ID, "reason": err.Error()}); auditErr != nil {
			p.logger.Error("auditing a failed purge", "plugin", pluginID, "error", auditErr)
		}
		return err
	}
	p.logger.Info("plugin purged", "plugin", pluginID, "version", leftover.Version, "table", leftover.Table)
	if err := p.store.Audit(ctx, job.RequestedBy, "plugin.purged", pluginID, map[string]any{
		"version": leftover.Version, "table": leftover.Table,
	}); err != nil {
		p.logger.Error("auditing a purge", "plugin", pluginID, "error", err)
	}
	return nil
}

func (p *PluginPurger) purgeOne(ctx context.Context, pluginID string) (store.PluginLeftover, error) {
	leftover, err := p.store.PluginLeftoverOf(ctx, pluginID)
	if err != nil {
		return leftover, err
	}
	body, err := p.layers.Get(ctx, leftover.Object)
	if err != nil {
		return leftover, fmt.Errorf("the layer of %s %s: %w", pluginID, leftover.Version, err)
	}
	defer func() { _ = body.Close() }()
	files, err := installed.Unpack(body, manifest.MaxSize)
	if err != nil {
		return leftover, fmt.Errorf("the layer of %s %s: %w", pluginID, leftover.Version, err)
	}
	// Read without its program: only its migrations are used.
	plugin, err := installed.Read(files, installed.NotRunning)
	if err != nil {
		return leftover, fmt.Errorf("the layer of %s %s: %w", pluginID, leftover.Version, err)
	}
	if plugin.ID != leftover.PluginID || plugin.MigrationsTable() != leftover.Table {
		return leftover, fmt.Errorf("the layer of %s %s declares %s, version table %q, and %q was recorded",
			pluginID, leftover.Version, plugin.ID, plugin.MigrationsTable(), leftover.Table)
	}
	if err := p.purge(ctx, plugin); err != nil {
		return leftover, err
	}
	return leftover, p.store.RecordPluginPurged(ctx, pluginID)
}
