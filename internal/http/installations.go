package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Installing plugins from an OCI registry: an administrator gives a reference pinned by digest, the
 * worker pulls it, verifies its signature against the keys the operator trusts, and records it.
 *
 * The request is a job, as an export is: pulling can take a while and fail, and the queue gives it
 * a trace and a place where its failure is read.
 */

// The contract with the worker, repeated here rather than imported: the api does not depend on the
// worker. A consistency test keeps the two aligned.
const (
	PluginInstallJobKind  = "plugin-install"
	PluginInstallationKey = "installation"
	PluginPurgeJobKind    = "plugin-purge"
	PluginPurgeKey        = "plugin"
)

// InstallationStore is what managing installations needs from the database.
type InstallationStore interface {
	PluginInstallations(ctx context.Context) ([]store.PluginInstallation, error)
	RequestPluginInstallation(ctx context.Context, reference, digest, actor string) (int64, error)
	FailPluginInstallation(ctx context.Context, id int64, reason string) error
	ActivatePluginInstallation(ctx context.Context, id int64) error
	RemovePluginInstallation(ctx context.Context, id int64) (string, error)
	PluginInstallation(ctx context.Context, id int64) (store.PluginInstallation, error)
	AcceptSource(ctx context.Context, pluginID, reason, subject string) error
	RetryPluginInstallation(ctx context.Context, id int64) error
	PluginLeftovers(ctx context.Context) ([]store.PluginLeftover, error)
	PluginLeftoverOf(ctx context.Context, pluginID string) (store.PluginLeftover, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// InstallationsHandler requests, activates and removes plugin installations.
type InstallationsHandler struct {
	store InstallationStore
	// check builds the capabilities as they would be with an installed version active, and says why
	// they could not be: an activation is refused before it is recorded.
	check func(ctx context.Context, id int64) error
	// reload applies a change on this replica at once; the others follow the counter.
	reload func(ctx context.Context) error
	logger *slog.Logger
}

func NewInstallationsHandler(
	installations InstallationStore, check func(ctx context.Context, id int64) error,
	reload func(ctx context.Context) error, logger *slog.Logger,
) *InstallationsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &InstallationsHandler{store: installations, check: check, reload: reload, logger: logger}
}

// Routes are administration routes.
func (h *InstallationsHandler) Routes(mux *http.ServeMux, m *Middleware) {
	admin := func(handler http.HandlerFunc) http.Handler {
		return Chain(handler, m.Authenticate, m.Require(auth.PermAdmin))
	}
	mux.Handle("GET /admin/plugins/installations", admin(h.List))
	mux.Handle("POST /admin/plugins/installations", admin(h.Request))
	mux.Handle("POST /admin/plugins/installations/{id}/activate", admin(h.Activate))
	mux.Handle("POST /admin/plugins/installations/{id}/accept", admin(h.Accept))
	mux.Handle("DELETE /admin/plugins/installations/{id}", admin(h.Remove))
	mux.Handle("GET /admin/plugins/leftovers", admin(h.Leftovers))
	mux.Handle("POST /admin/plugins/leftovers/{plugin}/purge", admin(h.Purge))
}

type installationView struct {
	ID          int64      `json:"id"`
	Reference   string     `json:"reference"`
	Digest      string     `json:"digest"`
	Plugin      string     `json:"plugin,omitempty"`
	Version     string     `json:"version,omitempty"`
	SignedBy    string     `json:"signed_by,omitempty"`
	State       string     `json:"state"`
	Error       string     `json:"error,omitempty"`
	Active      bool       `json:"active"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	InstalledAt *time.Time `json:"installed_at,omitempty"`
	RemovedAt   *time.Time `json:"removed_at,omitempty"`
}

// List returns every installation, the most recent first.
func (h *InstallationsHandler) List(w http.ResponseWriter, r *http.Request) {
	installations, err := h.store.PluginInstallations(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the installations", "")
		return
	}
	views := make([]installationView, 0, len(installations))
	for _, installation := range installations {
		views = append(views, installationView{
			ID: installation.ID, Reference: installation.Reference, Digest: installation.Digest,
			Plugin: installation.PluginID, Version: installation.Version, SignedBy: installation.SignedBy,
			State: installation.State, Error: installation.Error, Active: installation.Active,
			RequestedBy: installation.RequestedBy, RequestedAt: installation.RequestedAt,
			InstalledAt: installation.InstalledAt, RemovedAt: installation.RemovedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"installations": views})
}

// Request records a reference to install and queues the job that pulls it.
func (h *InstallationsHandler) Request(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reference string `json:"reference"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "expected {\"reference\": \"repository@sha256:...\"}")
		return
	}
	// By digest only: a tag can be moved after the administrator looked at what it named.
	reference, digest, err := installed.Pinned(body.Reference)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid reference", err.Error())
		return
	}
	identity, _ := IdentityFrom(r.Context())
	id, err := h.store.RequestPluginInstallation(r.Context(), reference, digest, identity.Subject)
	if err != nil {
		h.logger.Error("requesting an installation", "reference", reference, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot request the installation", "")
		return
	}
	job, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind: PluginInstallJobKind, RequestedBy: identity.Subject,
		Params: map[string]string{PluginInstallationKey: strconv.FormatInt(id, 10)},
	})
	if err != nil {
		h.logger.Error("queueing an installation", "installation", id, "error", err)
		_ = h.store.FailPluginInstallation(r.Context(), id, "the installation job could not be queued")
		writeProblem(w, http.StatusInternalServerError, "Cannot queue the installation", "")
		return
	}
	h.audit(r, "plugin.installation.requested", reference, map[string]any{"installation": id, "job": job})
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "job": job, "reference": reference})
}

// Accept records that an administrator accepts a plugin whose source the instance does not
// recognise, and puts its installation back in the queue.
//
// The acceptance is recorded against the **plugin's id**, so the next version of that plugin
// installs without asking again. It is not recorded against a key: the signature format carries
// no public key, so an unknown signer leaves nothing to remember.
//
// Only an installation waiting on that decision can be accepted. An installation that failed for
// another reason is not turned into an acceptance by this route.
func (h *InstallationsHandler) Accept(w http.ResponseWriter, r *http.Request) {
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	installation, err := h.store.PluginInstallation(r.Context(), id)
	if errors.Is(err, store.ErrInstallationNotFound) {
		writeProblem(w, http.StatusNotFound, "Unknown installation", "")
		return
	}
	if err != nil {
		h.logger.Error("reading an installation", "installation", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot read the installation", "")
		return
	}
	if installation.State != store.InstallationUnrecognised {
		writeProblem(w, http.StatusConflict, "Nothing to accept",
			"this installation is not waiting for a decision on its source")
		return
	}

	identity, _ := IdentityFrom(r.Context())
	if err := h.store.AcceptSource(r.Context(), installation.PluginID, installation.Error, identity.Subject); err != nil {
		h.logger.Error("accepting a source", "plugin", installation.PluginID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot accept the source", "")
		return
	}
	if err := h.store.RetryPluginInstallation(r.Context(), id); err != nil {
		h.logger.Error("retrying an installation", "installation", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot retry the installation", "")
		return
	}
	job, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind: PluginInstallJobKind, RequestedBy: identity.Subject,
		Params: map[string]string{PluginInstallationKey: strconv.FormatInt(id, 10)},
	})
	if err != nil {
		h.logger.Error("queueing an installation", "installation", id, "error", err)
		_ = h.store.FailPluginInstallation(r.Context(), id, "the installation job could not be queued")
		writeProblem(w, http.StatusInternalServerError, "Cannot queue the installation", "")
		return
	}
	h.audit(r, "plugin.source.accepted", installation.Reference,
		map[string]any{"installation": id, "plugin": installation.PluginID, "reason": installation.Error, "job": job})
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "job": job, "plugin": installation.PluginID})
}

// Activate makes an installed version the one new runs are launched with.
func (h *InstallationsHandler) Activate(w http.ResponseWriter, r *http.Request) {
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	if err := h.check(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrInstallationNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown installation", "")
			return
		}
		writeProblem(w, http.StatusConflict, "Cannot activate this version", err.Error())
		return
	}
	if err := h.store.ActivatePluginInstallation(r.Context(), id); err != nil {
		h.logger.Error("activating an installation", "installation", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot activate this version", "")
		return
	}
	h.audit(r, "plugin.activated", strconv.FormatInt(id, 10), nil)
	h.applyNow(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// Remove removes a version; removing the active one uninstalls the plugin.
func (h *InstallationsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	uninstalled, err := h.store.RemovePluginInstallation(r.Context(), id)
	if errors.Is(err, store.ErrInstallationNotFound) {
		writeProblem(w, http.StatusNotFound, "Unknown installation", "")
		return
	}
	if err != nil {
		h.logger.Error("removing an installation", "installation", id, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot remove this version", "")
		return
	}
	action, payload := "plugin.version.removed", map[string]any{"installation": id}
	if uninstalled != "" {
		action, payload["plugin"] = "plugin.uninstalled", uninstalled
	}
	h.audit(r, action, strconv.FormatInt(id, 10), payload)
	h.applyNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"uninstalled": uninstalled})
}

type leftoverView struct {
	Plugin    string     `json:"plugin"`
	Version   string     `json:"version"`
	Table     string     `json:"table"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
}

// Leftovers lists the tables uninstalled plugins left behind.
func (h *InstallationsHandler) Leftovers(w http.ResponseWriter, r *http.Request) {
	leftovers, err := h.store.PluginLeftovers(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read what the plugins left", "")
		return
	}
	views := make([]leftoverView, 0, len(leftovers))
	for _, leftover := range leftovers {
		views = append(views, leftoverView{
			Plugin: leftover.PluginID, Version: leftover.Version, Table: leftover.Table, RemovedAt: leftover.RemovedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"leftovers": views})
}

// Purge queues the job that drops an uninstalled plugin's tables. Refused while an installation is
// pending: it may be the same plugin, whose migrations would run against tables being dropped.
func (h *InstallationsHandler) Purge(w http.ResponseWriter, r *http.Request) {
	pluginID := r.PathValue("plugin")
	leftover, err := h.store.PluginLeftoverOf(r.Context(), pluginID)
	if errors.Is(err, store.ErrNothingToPurge) {
		writeProblem(w, http.StatusNotFound, "Nothing to purge", err.Error())
		return
	}
	if err != nil {
		h.logger.Error("reading a plugin's leftover", "plugin", pluginID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot purge the plugin", "")
		return
	}
	installations, err := h.store.PluginInstallations(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the installations", "")
		return
	}
	for _, installation := range installations {
		if installation.State == store.InstallationPending {
			writeProblem(w, http.StatusConflict, "An installation is pending",
				"purge once the installations in progress have concluded")
			return
		}
	}
	identity, _ := IdentityFrom(r.Context())
	job, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind: PluginPurgeJobKind, RequestedBy: identity.Subject,
		Params: map[string]string{PluginPurgeKey: pluginID},
	})
	if err != nil {
		h.logger.Error("queueing a purge", "plugin", pluginID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot queue the purge", "")
		return
	}
	h.audit(r, "plugin.purge.requested", pluginID, map[string]any{
		"job": job, "version": leftover.Version, "table": leftover.Table,
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job, "plugin": pluginID})
}

func installationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Unknown installation", "")
		return 0, false
	}
	return id, true
}

func (h *InstallationsHandler) applyNow(ctx context.Context) {
	if h.reload == nil {
		return
	}
	if err := h.reload(ctx); err != nil && !errors.Is(err, context.Canceled) {
		h.logger.Error("applying a plugin change", "error", err)
	}
}

func (h *InstallationsHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		h.logger.Error("audit log", "action", action, "target", target, "error", err)
	}
}
