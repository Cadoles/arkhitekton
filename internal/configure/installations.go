package configure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// The job that pulls a plugin, and the parameter naming the installation it pulls.
const (
	PluginInstallJobKind  = "plugin-install"
	PluginInstallationKey = "installation"
)

// InstallationStore is what installing a plugin needs from the database.
type InstallationStore interface {
	RequestPluginInstallation(ctx context.Context, request store.InstallationRequest) (int64, error)
	FailPluginInstallation(ctx context.Context, id int64, reason string) error
	ActivatePluginInstallation(ctx context.Context, id int64) error
	PluginInstallation(ctx context.Context, id int64) (store.PluginInstallation, error)
	AcceptSource(ctx context.Context, pluginID, reason, subject string) error
	RetryPluginInstallation(ctx context.Context, id int64) error
	PluginSources(ctx context.Context) ([]store.PluginSource, error)
	PluginSource(ctx context.Context, pluginID string) (store.PluginSource, error)
	SetSourcePinned(ctx context.Context, pluginID string, pinned bool) error
	RequestUpdateCheck(ctx context.Context, pluginID string) error
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	Auditor
}

// Mode says what an installation of a plugin already installed from the same repository does.
type Mode string

const (
	// ModeAsk refuses with a conflict when the plugin is already installed, so that the
	// administrator chooses.
	ModeAsk Mode = ""
	// ModeInstall installs the version beside the active one, to activate later.
	ModeInstall Mode = "install"
	// ModeUpdate replaces the active version, its configuration kept.
	ModeUpdate Mode = "update"
)

// Installations requests, accepts and activates the plugins an administrator installs from an OCI
// registry. The pull itself is a job of the worker: it can take a while and fail, and the queue
// gives it a trace and a place where its failure is read.
type Installations struct {
	Store InstallationStore
	// Check refuses a version the instance would not build with once active.
	Check func(ctx context.Context, id int64) error
	// Reload applies an activation now rather than at the next poll. Nil: the poll applies it.
	Reload func(ctx context.Context) error
	Logger *slog.Logger
}

// Request records an installation and queues its pull. A repository alone installs its latest
// stable version, which the plugin then follows; a tag or a digest pins it. The worker resolves a
// repository or a tag to its digest: it holds the registries' credentials, the api does not.
//
// A plugin already installed from the same repository is not installed a second time without the
// administrator saying how: beside the active version, or replacing it.
func (s Installations) Request(ctx context.Context, actor, raw string, mode Mode) (id, job int64, reference string, err error) {
	logger := loggerOr(s.Logger)
	source, err := installed.ParseSource(raw)
	if err != nil {
		return 0, 0, "", invalid("Invalid reference", err)
	}
	if mode != ModeAsk && mode != ModeInstall && mode != ModeUpdate {
		return 0, 0, "", refuse(http.StatusBadRequest, "Unknown mode", `expected "install" or "update"`)
	}
	request := store.InstallationRequest{Actor: actor, Follows: !source.Pinned(), Reference: source.Repository}
	switch {
	case source.Digest != "":
		request.Reference, request.Digest = source.Reference(), source.Digest
	case source.Tag != "":
		request.Reference = source.Repository + ":" + source.Tag
	}

	sources, err := s.Store.PluginSources(ctx)
	if err != nil {
		return 0, 0, "", failure(logger, "Cannot read the plugins' sources", err)
	}
	var current *store.PluginSource
	for i := range sources {
		if sources[i].Repository == source.Repository && sources[i].ActiveID != 0 {
			current = &sources[i]
		}
	}
	switch {
	case current != nil && mode == ModeAsk:
		return 0, 0, "", refuse(http.StatusConflict, "Plugin already installed", fmt.Sprintf(
			"%s %s is installed from this repository: install this version beside it, or update it",
			current.PluginID, current.ActiveVersion))
	case current != nil && mode == ModeUpdate:
		request.Replaces = &current.ActiveID
	case current == nil && mode == ModeUpdate:
		return 0, 0, "", refuse(http.StatusNotFound, "Nothing to update", "no plugin is installed from this repository")
	}
	reference = request.Reference

	id, err = s.Store.RequestPluginInstallation(ctx, request)
	if err != nil {
		return 0, 0, "", failure(logger, "Cannot request the installation", err, "reference", reference)
	}
	job, err = s.queue(ctx, actor, id)
	if err != nil {
		return 0, 0, "", err
	}
	audit(ctx, logger, s.Store, actor, "plugin.installation.requested", reference, map[string]any{
		"installation": id, "job": job, "follows": request.Follows, "mode": string(mode),
	})
	return id, job, reference, nil
}

// Update installs the newer version a following plugin's repository publishes, in place of the
// active one, its configuration kept. A pinned plugin is not updated: the administrator pinned it.
func (s Installations) Update(ctx context.Context, actor, pluginID string) (id, job int64, err error) {
	logger := loggerOr(s.Logger)
	source, err := s.source(ctx, pluginID)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case source.Pinned:
		return 0, 0, refuse(http.StatusConflict, "Plugin pinned",
			fmt.Sprintf("%s is pinned to %s: follow its repository to update it", pluginID, source.ActiveVersion))
	case source.LatestDigest == "" || source.LatestVersion == source.ActiveVersion:
		return 0, 0, refuse(http.StatusConflict, "Up to date", "no newer version was found in its repository")
	case source.ActiveID == 0:
		return 0, 0, refuse(http.StatusConflict, "No active version", "an installation of this plugin is under way")
	}
	active := source.ActiveID
	id, err = s.Store.RequestPluginInstallation(ctx, store.InstallationRequest{
		Reference: source.Repository + "@" + source.LatestDigest, Digest: source.LatestDigest, Actor: actor,
		Follows: true, ExpectedVersion: source.LatestVersion, Replaces: &active,
	})
	if err != nil {
		return 0, 0, failure(logger, "Cannot request the update", err, "plugin", pluginID)
	}
	if job, err = s.queue(ctx, actor, id); err != nil {
		return 0, 0, err
	}
	audit(ctx, logger, s.Store, actor, "plugin.update.requested", pluginID, map[string]any{
		"installation": id, "job": job, "from": source.ActiveVersion, "to": source.LatestVersion,
	})
	return id, job, nil
}

// CheckUpdates asks the worker to read a plugin's repository now rather than at its next round.
func (s Installations) CheckUpdates(ctx context.Context, pluginID string) error {
	if _, err := s.source(ctx, pluginID); err != nil {
		return err
	}
	if err := s.Store.RequestUpdateCheck(ctx, pluginID); err != nil {
		return failure(loggerOr(s.Logger), "Cannot request the check", err, "plugin", pluginID)
	}
	return nil
}

// SetSource pins a plugin to its version, or makes it follow its repository. Nothing is installed:
// following makes a newer version offered, at the next check.
func (s Installations) SetSource(ctx context.Context, actor, pluginID string, pinned bool) error {
	logger := loggerOr(s.Logger)
	if _, err := s.source(ctx, pluginID); err != nil {
		return err
	}
	if err := s.Store.SetSourcePinned(ctx, pluginID, pinned); err != nil {
		return failure(logger, "Cannot change what the plugin follows", err, "plugin", pluginID)
	}
	action := "plugin.source.followed"
	if pinned {
		action = "plugin.source.pinned"
	}
	audit(ctx, logger, s.Store, actor, action, pluginID, nil)
	return nil
}

func (s Installations) source(ctx context.Context, pluginID string) (store.PluginSource, error) {
	source, err := s.Store.PluginSource(ctx, pluginID)
	if errors.Is(err, store.ErrSourceNotFound) {
		return source, refuse(http.StatusNotFound, "Plugin not installed", pluginID)
	}
	if err != nil {
		return source, failure(loggerOr(s.Logger), "Cannot read the plugin's source", err, "plugin", pluginID)
	}
	return source, nil
}

// Accept records that an administrator accepts a plugin whose source the instance does not
// recognise, and puts its installation back in the queue.
//
// The acceptance is recorded against the plugin's id, so the next version of that plugin installs
// without asking again. It is not recorded against a key: the signature format carries no public
// key, so an unknown signer leaves nothing to remember. Only an installation waiting on that
// decision can be accepted.
func (s Installations) Accept(ctx context.Context, actor string, id int64) (store.PluginInstallation, int64, error) {
	logger := loggerOr(s.Logger)
	installation, err := s.Store.PluginInstallation(ctx, id)
	if errors.Is(err, store.ErrInstallationNotFound) {
		return installation, 0, refuse(http.StatusNotFound, "Unknown installation", "")
	}
	if err != nil {
		return installation, 0, failure(logger, "Cannot read the installation", err, "installation", id)
	}
	if installation.State != store.InstallationUnrecognised {
		return installation, 0, refuse(http.StatusConflict, "Nothing to accept",
			"this installation is not waiting for a decision on its source")
	}
	if err := s.Store.AcceptSource(ctx, installation.PluginID, installation.Error, actor); err != nil {
		return installation, 0, failure(logger, "Cannot accept the source", err, "plugin", installation.PluginID)
	}
	if err := s.Store.RetryPluginInstallation(ctx, id); err != nil {
		return installation, 0, failure(logger, "Cannot retry the installation", err, "installation", id)
	}
	job, err := s.queue(ctx, actor, id)
	if err != nil {
		return installation, 0, err
	}
	audit(ctx, logger, s.Store, actor, "plugin.source.accepted", installation.Reference,
		map[string]any{"installation": id, "plugin": installation.PluginID, "reason": installation.Error, "job": job})
	return installation, job, nil
}

// Activate makes an installed version the one new runs are launched with.
func (s Installations) Activate(ctx context.Context, actor string, id int64) error {
	logger := loggerOr(s.Logger)
	if s.Check != nil {
		if err := s.Check(ctx, id); err != nil {
			if errors.Is(err, store.ErrInstallationNotFound) {
				return refuse(http.StatusNotFound, "Unknown installation", "")
			}
			return refuse(http.StatusConflict, "Cannot activate this version", err.Error())
		}
	}
	if err := s.Store.ActivatePluginInstallation(ctx, id); err != nil {
		return failure(logger, "Cannot activate this version", err, "installation", id)
	}
	audit(ctx, logger, s.Store, actor, "plugin.activated", strconv.FormatInt(id, 10), nil)
	if s.Reload != nil {
		if err := s.Reload(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("applying an installation", "error", err)
		}
	}
	return nil
}

func (s Installations) queue(ctx context.Context, actor string, id int64) (int64, error) {
	job, err := s.Store.Enqueue(ctx, store.JobRequest{
		Kind: PluginInstallJobKind, RequestedBy: actor,
		Params: map[string]string{PluginInstallationKey: strconv.FormatInt(id, 10)},
	})
	if err != nil {
		_ = s.Store.FailPluginInstallation(ctx, id, "the installation job could not be queued")
		return 0, failure(loggerOr(s.Logger), "Cannot queue the installation", err, "installation", id)
	}
	return job, nil
}
