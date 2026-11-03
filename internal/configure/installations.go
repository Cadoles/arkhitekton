package configure

import (
	"context"
	"errors"
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
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	Auditor
}

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

// Request records an installation of a reference pinned by digest, and queues its pull.
func (s Installations) Request(ctx context.Context, actor, raw string) (id, job int64, reference string, err error) {
	logger := loggerOr(s.Logger)
	reference, digest, err := installed.Pinned(raw)
	if err != nil {
		return 0, 0, "", invalid("Invalid reference", err)
	}
	id, err = s.Store.RequestPluginInstallation(ctx, store.InstallationRequest{Reference: reference, Digest: digest, Actor: actor})
	if err != nil {
		return 0, 0, "", failure(logger, "Cannot request the installation", err, "reference", reference)
	}
	job, err = s.queue(ctx, actor, id)
	if err != nil {
		return 0, 0, "", err
	}
	audit(ctx, logger, s.Store, actor, "plugin.installation.requested", reference, map[string]any{"installation": id, "job": job})
	return id, job, reference, nil
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
