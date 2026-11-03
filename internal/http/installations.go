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
	"forge.cadoles.com/vcarroy/arkhitekton/internal/configure"
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
	PluginInstallJobKind  = configure.PluginInstallJobKind
	PluginInstallationKey = configure.PluginInstallationKey
	PluginPurgeJobKind    = "plugin-purge"
	PluginPurgeKey        = "plugin"
)

// InstallationStore is what managing installations needs from the database.
type InstallationStore interface {
	PluginInstallations(ctx context.Context) ([]store.PluginInstallation, error)
	RequestPluginInstallation(ctx context.Context, request store.InstallationRequest) (int64, error)
	FailPluginInstallation(ctx context.Context, id int64, reason string) error
	ActivatePluginInstallation(ctx context.Context, id int64) error
	RemovePluginInstallation(ctx context.Context, id int64) (string, error)
	PluginInstallation(ctx context.Context, id int64) (store.PluginInstallation, error)
	AcceptSource(ctx context.Context, pluginID, reason, subject string) error
	RetryPluginInstallation(ctx context.Context, id int64) error
	PluginSources(ctx context.Context) ([]store.PluginSource, error)
	PluginSource(ctx context.Context, pluginID string) (store.PluginSource, error)
	SetSourcePinned(ctx context.Context, pluginID string, pinned bool) error
	RequestUpdateCheck(ctx context.Context, pluginID string) error
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
	// What a plugin follows, and its updates: by plugin, not by installation.
	mux.Handle("POST /admin/plugins/{plugin}/update", admin(h.Update))
	mux.Handle("POST /admin/plugins/{plugin}/check", admin(h.CheckUpdates))
	mux.Handle("PUT /admin/plugins/{plugin}/source", admin(h.SetSource))
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

// sourceView is what an installed plugin follows, and whether a newer version is there.
type sourceView struct {
	Plugin     string `json:"plugin"`
	Repository string `json:"repository"`
	// Pinned keeps the plugin on its version: a newer one is reported, the screen offers no update.
	Pinned        bool   `json:"pinned"`
	Version       string `json:"version,omitempty"`
	LatestVersion string `json:"latest_version,omitempty"`
	// UpdateAvailable says a newer version was found; Major that it crosses a major version.
	UpdateAvailable bool       `json:"update_available"`
	Major           bool       `json:"major,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	CheckError      string     `json:"check_error,omitempty"`
	CheckRequested  bool       `json:"check_requested,omitempty"`
}

// List returns every installation, the most recent first, and each plugin's source.
func (h *InstallationsHandler) List(w http.ResponseWriter, r *http.Request) {
	installations, err := h.store.PluginInstallations(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the installations", "")
		return
	}
	sources, err := h.store.PluginSources(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugins' sources", "")
		return
	}
	plugins := make([]sourceView, 0, len(sources))
	for _, source := range sources {
		available := source.LatestVersion != "" && source.LatestVersion != source.ActiveVersion
		plugins = append(plugins, sourceView{
			Plugin: source.PluginID, Repository: source.Repository, Pinned: source.Pinned,
			Version: source.ActiveVersion, LatestVersion: source.LatestVersion,
			UpdateAvailable: available, Major: available && installed.MajorChange(source.ActiveVersion, source.LatestVersion),
			CheckedAt: source.CheckedAt, CheckError: source.CheckError, CheckRequested: source.CheckRequested,
		})
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
	writeJSON(w, http.StatusOK, map[string]any{"installations": views, "plugins": plugins})
}

// Request installs a plugin from a repository, which it then follows, or from a tag or a digest,
// which pin it. A plugin already installed from that repository answers 409 unless `mode` says
// whether to install beside it or update it.
func (h *InstallationsHandler) Request(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reference string `json:"reference"`
		Mode      string `json:"mode"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request",
			"expected {\"reference\": \"registry/repository\"}, a tag or \"@sha256:...\" to pin it")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	id, job, reference, err := h.service().Request(r.Context(), identity.Subject, body.Reference, configure.Mode(body.Mode))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "job": job, "reference": reference})
}

// Update replaces a following plugin's active version with the newer one found in its repository.
func (h *InstallationsHandler) Update(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	id, job, err := h.service().Update(r.Context(), identity.Subject, r.PathValue("plugin"))
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "job": job})
}

// CheckUpdates asks for a check of a plugin's repository within seconds.
func (h *InstallationsHandler) CheckUpdates(w http.ResponseWriter, r *http.Request) {
	if err := h.service().CheckUpdates(r.Context(), r.PathValue("plugin")); err != nil {
		writeRefusal(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// SetSource pins a plugin to its version, or makes it follow its repository.
func (h *InstallationsHandler) SetSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	if err != nil || json.Unmarshal(raw, &body) != nil || body.Pinned == nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", `expected {"pinned": true|false}`)
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.service().SetSource(r.Context(), identity.Subject, r.PathValue("plugin"), *body.Pinned); err != nil {
		writeRefusal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Accept records that an administrator accepts a plugin whose source the instance does not
// recognise, and puts its installation back in the queue.
func (h *InstallationsHandler) Accept(w http.ResponseWriter, r *http.Request) {
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	installation, job, err := h.service().Accept(r.Context(), identity.Subject, id)
	if err != nil {
		writeRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "job": job, "plugin": installation.PluginID})
}

// Activate makes an installed version the one new runs are launched with.
func (h *InstallationsHandler) Activate(w http.ResponseWriter, r *http.Request) {
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	identity, _ := IdentityFrom(r.Context())
	if err := h.service().Activate(r.Context(), identity.Subject, id); err != nil {
		writeRefusal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *InstallationsHandler) service() configure.Installations {
	return configure.Installations{Store: h.store, Check: h.check, Reload: h.reload, Logger: h.logger}
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
