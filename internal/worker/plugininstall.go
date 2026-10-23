package worker

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// KindPluginInstall is the `kind` of the jobs that install a plugin an administrator asked for.
//
// A job rather than the request itself: pulling can take a while and fail on a registry out of
// reach, and the queue gives it a trace, and a place where its failure is read.
const KindPluginInstall = "plugin-install"

// ParamInstallation names the installation a job carries out.
const ParamInstallation = "installation"

// pullTimeout bounds pulling and verifying one plugin.
const pullTimeout = 5 * time.Minute

// InstallationStore is what the installer needs from the database.
type InstallationStore interface {
	PluginInstallation(ctx context.Context, id int64) (store.PluginInstallation, error)
	CompletePluginInstallation(ctx context.Context, id int64, pluginID, version, signedBy, object string) (bool, error)
	FailPluginInstallation(ctx context.Context, id int64, reason string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// PluginInstaller pulls a plugin, verifies it, stores it and records it installed.
type PluginInstaller struct {
	store   InstallationStore
	storage Storage
	pull    func(ctx context.Context, reference string) (installed.Pulled, error)
	// check builds the capabilities as they would be with the plugin active, and says why they
	// could not be.
	check func(ctx context.Context, plugin capability.Plugin) error
	// changed puts the new capabilities in force at once, rather than at the next reload.
	changed func(ctx context.Context)
	keys    blob.Keys
	logger  *slog.Logger
}

func NewPluginInstaller(
	installations InstallationStore, storage Storage,
	pull func(ctx context.Context, reference string) (installed.Pulled, error),
	check func(ctx context.Context, plugin capability.Plugin) error,
	changed func(ctx context.Context), logger *slog.Logger,
) *PluginInstaller {
	return &PluginInstaller{
		store: installations, storage: storage, pull: pull, check: check, changed: changed, logger: logger,
	}
}

// Kind returns the `kind` this installer handles.
func (p *PluginInstaller) Kind() string { return KindPluginInstall }

// Run carries out an installation; its failure is recorded on the installation as on the job.
func (p *PluginInstaller) Run(ctx context.Context, job store.Job) error {
	id, err := strconv.ParseInt(job.Params[ParamInstallation], 10, 64)
	if err != nil {
		return fmt.Errorf("job %d names no installation", job.ID)
	}
	if err := p.install(ctx, id); err != nil {
		if recordErr := p.store.FailPluginInstallation(ctx, id, err.Error()); recordErr != nil {
			p.logger.Error("recording a failed installation", "installation", id, "error", recordErr)
		}
		return err
	}
	return nil
}

func (p *PluginInstaller) install(ctx context.Context, id int64) error {
	installation, err := p.store.PluginInstallation(ctx, id)
	if err != nil {
		return err
	}
	if installation.State != store.InstallationPending {
		return fmt.Errorf("installation %d is %s, not pending", id, installation.State)
	}

	pulling, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	pulled, err := p.pull(pulling, installation.Reference)
	if err != nil {
		return err
	}
	plugin, err := installed.Load(pulled.Files)
	if err != nil {
		return err
	}
	if err := p.check(ctx, plugin); err != nil {
		return err
	}

	key, err := p.keys.PluginLayer(plugin.ID, plugin.Version, pulled.Digest)
	if err != nil {
		return err
	}
	if err := p.storage.Put(ctx, key, bytes.NewReader(pulled.Layer), int64(len(pulled.Layer)), manifest.LayerMediaType); err != nil {
		return fmt.Errorf("storing the plugin: %w", err)
	}
	activated, err := p.store.CompletePluginInstallation(ctx, id, plugin.ID, plugin.Version, pulled.SignedBy, key)
	if err != nil {
		return err
	}

	p.logger.Info("plugin installed", "plugin", plugin.ID, "version", plugin.Version,
		"digest", pulled.Digest, "signed_by", pulled.SignedBy, "active", activated)
	if err := p.store.Audit(ctx, installation.RequestedBy, "plugin.installed", plugin.ID, map[string]any{
		"version": plugin.Version, "reference": pulled.Reference, "signed_by": pulled.SignedBy, "active": activated,
	}); err != nil {
		p.logger.Error("auditing an installation", "installation", id, "error", err)
	}
	if p.changed != nil {
		p.changed(ctx)
	}
	return nil
}
