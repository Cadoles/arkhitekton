package mse

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

/*
 * Acceptance sign-off.
 *
 * The sign-off is the concatenation of two halves: the automatic checks and the human
 * validation. These routes carry the second.
 *
 * In the plugin because an opinion bears on a release **in an environment**: the table has a
 * foreign key to `environments`, and without the module there is no environment to give an
 * opinion on.
 */

// SignoffStore is what the sign-off routes need from the database.
type SignoffStore interface {
	host.ProjectResolver
	GetRelease(ctx context.Context, projectID int64, tag string) (host.Release, error)
	ListSignoffs(ctx context.Context, releaseID int64) ([]storage.Signoff, error)
	SetSignoff(ctx context.Context, releaseID int64, environmentID, role, status, detail, decidedBy string) error
	DeleteSignoff(ctx context.Context, releaseID int64, environmentID, role string) error
	SignoffEnvironmentExists(ctx context.Context, projectID int64, environmentID string) (bool, error)
	ListEnvironments(ctx context.Context, projectID int64) ([]storage.Environment, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// SignoffsHandler serves the human validation of a release.
type SignoffsHandler struct {
	store  SignoffStore
	logger *slog.Logger
}

func NewSignoffsHandler(signoffs SignoffStore, logger *slog.Logger) *SignoffsHandler {
	return &SignoffsHandler{store: signoffs, logger: logger}
}

// Routes mounts the sign-off routes.
//
// Giving an opinion is a delivery decision: `release:edit`, the same permission as the manual
// actions, because they are the same people who run an acceptance campaign. Requiring
// `release:create:final` would reserve validation to those who deliver, whereas validating and
// delivering are the two acts one wants performed by distinct people.
func (h *SignoffsHandler) Routes(mux *http.ServeMux, m *host.Middleware) {
	module := m.RequireModule(PluginID)

	mux.Handle("GET /projects/{project}/releases/{tag}/signoffs", host.Chain(http.HandlerFunc(h.List),
		m.Authenticate, module, m.RequireIn(host.PermRead)))
	mux.Handle("PUT /projects/{project}/releases/{tag}/signoffs/{env}", host.Chain(http.HandlerFunc(h.Set),
		m.Authenticate, module, m.CSRF, m.RequireIn(host.PermReleaseEdit)))
	mux.Handle("DELETE /projects/{project}/releases/{tag}/signoffs/{env}", host.Chain(http.HandlerFunc(h.Clear),
		m.Authenticate, module, m.CSRF, m.RequireIn(host.PermReleaseEdit)))
}

type signoffVw struct {
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	DecidedBy string    `json:"decided_by"`
	DecidedAt time.Time `json:"decided_at"`
}

type environmentSignoffsVw struct {
	Environment string      `json:"environment"`
	Label       string      `json:"label"`
	Signoffs    []signoffVw `json:"signoffs"`
}

// release reads the release named in the path, or writes the response.
func (h *SignoffsHandler) release(w http.ResponseWriter, r *http.Request) (host.Release, int64, bool) {
	projectID, ok := host.RequestProject(w, r, h.store)
	if !ok {
		return host.Release{}, 0, false
	}
	release, err := h.store.GetRelease(r.Context(), projectID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, host.ErrReleaseNotFound) {
			host.WriteProblem(w, http.StatusNotFound, "Release not found", "")
			return host.Release{}, 0, false
		}
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return host.Release{}, 0, false
	}
	return release, projectID, true
}

// List returns a release's sign-offs, grouped by environment.
//
// Every environment is returned, including those with no opinion: the interface must tell
// "nobody has given an opinion" from "this environment does not exist", and the waiting is
// precisely the useful information during acceptance.
func (h *SignoffsHandler) List(w http.ResponseWriter, r *http.Request) {
	release, projectID, ok := h.release(w, r)
	if !ok {
		return
	}

	environments, err := h.store.ListEnvironments(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the environments", "")
		return
	}

	signoffs, err := h.store.ListSignoffs(r.Context(), release.ID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the sign-offs", "")
		return
	}

	byEnvironment := map[string][]signoffVw{}
	for _, signoff := range signoffs {
		byEnvironment[signoff.EnvironmentID] = append(byEnvironment[signoff.EnvironmentID], signoffVw{
			Role: signoff.Role, Status: signoff.Status, Detail: signoff.Detail,
			DecidedBy: signoff.DecidedBy, DecidedAt: signoff.DecidedAt,
		})
	}

	// The order is that of the promotion flow, as `ListEnvironments` returns it.
	payload := make([]environmentSignoffsVw, 0, len(environments))
	for _, environment := range environments {
		entries := byEnvironment[environment.ID]
		if entries == nil {
			// An empty array and not `null`: the interface iterates over it with no guard.
			entries = []signoffVw{}
		}
		payload = append(payload, environmentSignoffsVw{
			Environment: environment.ID, Label: environment.Label, Signoffs: entries,
		})
	}

	host.WriteJSON(w, http.StatusOK, map[string]any{"environments": payload})
}

type signoffRequest struct {
	Role   string `json:"role"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// signoffStatuses bounds the recordable opinions.
//
// `na` is absent from it: waiting is the absence of a row, not a row that says "waiting".
// Accepting it would create two representations of the same state.
var signoffStatuses = map[string]bool{"pass": true, "warn": true, "fail": true}

// Set records a participant's opinion on a release in an environment.
func (h *SignoffsHandler) Set(w http.ResponseWriter, r *http.Request) {
	identity, ok := host.IdentityFrom(r.Context())
	if !ok {
		host.WriteProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	var request signoffRequest
	// 16 KiB: a reason for refusal is a sentence, not a report.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		host.WriteProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	role := strings.TrimSpace(request.Role)
	detail := strings.TrimSpace(request.Detail)

	if role == "" {
		host.WriteProblem(w, http.StatusBadRequest, "Missing role",
			"State who is giving the opinion: “Client”, “Recette”, “Exploitation”.")
		return
	}
	if !signoffStatuses[request.Status] {
		host.WriteProblem(w, http.StatusBadRequest, "Invalid sign-off",
			"The opinion is “pass”, “warn” or “fail”. Waiting is obtained by removing the sign-off.")
		return
	}
	// A reservation or a refusal with no reason tells whoever reads it later nothing. Required by
	// the API and not by the database: the rule may change, the data written may not.
	if request.Status != "pass" && detail == "" {
		host.WriteProblem(w, http.StatusBadRequest, "Missing reason",
			"A reservation or a refusal must state its reason.")
		return
	}

	release, projectID, ok := h.release(w, r)
	if !ok {
		return
	}

	environmentID := r.PathValue("env")
	exists, err := h.store.SignoffEnvironmentExists(r.Context(), projectID, environmentID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the environment", "")
		return
	}
	if !exists {
		host.WriteProblem(w, http.StatusNotFound, "Environment not found", environmentID)
		return
	}

	if err := h.store.SetSignoff(r.Context(), release.ID, environmentID, role,
		request.Status, detail, identity.Subject); err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot save", "")
		return
	}

	// The log carries the whole opinion, reason included: the table keeps only the last state, and
	// "who had validated before retracting" has an answer only here.
	h.audit(r, identity, AuditSignoffSet, release.Tag, map[string]any{
		"environment": environmentID, "role": role, "status": request.Status, "detail": detail,
	})

	host.WriteJSON(w, http.StatusNoContent, nil)
}

// Clear removes an opinion and puts the role back to waiting.
//
// Removing is not refusing: an opinion given on the wrong release must be able to disappear
// without leaving a refusal that would block the delivery.
func (h *SignoffsHandler) Clear(w http.ResponseWriter, r *http.Request) {
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	if role == "" {
		host.WriteProblem(w, http.StatusBadRequest, "Missing role",
			"State the role whose opinion is to be removed.")
		return
	}

	release, _, ok := h.release(w, r)
	if !ok {
		return
	}

	environmentID := r.PathValue("env")
	if err := h.store.DeleteSignoff(r.Context(), release.ID, environmentID, role); err != nil {
		if errors.Is(err, storage.ErrSignoffNotFound) {
			host.WriteProblem(w, http.StatusNotFound, "Sign-off not found", "")
			return
		}
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot remove", "")
		return
	}

	if identity, ok := host.IdentityFrom(r.Context()); ok {
		h.audit(r, identity, AuditSignoffClear, release.Tag, map[string]any{
			"environment": environmentID, "role": role,
		})
	}

	host.WriteJSON(w, http.StatusNoContent, nil)
}

// audit records a decision without ever failing it.
func (h *SignoffsHandler) audit(
	r *http.Request, identity host.Identity, action, target string, payload map[string]any,
) {
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
