package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/oauth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The AI agents: the consent a person gives to an agent that asks to act on their behalf, and the
 * list of the agents they authorised, to withdraw one.
 *
 * Signed-in routes like the others: an agent's own token is accepted on `/mcp` only, and none of
 * these routes is one of its tools.
 */

// AgentAuthority is what the routes need from the authorisation server.
type AgentAuthority interface {
	Pending(ctx context.Context, id string) (oauth.Pending, error)
	Approve(ctx context.Context, id string, identity auth.Identity, scopes []string) (string, error)
	Deny(ctx context.Context, id string) (string, error)
	Revoke(ctx context.Context, id int64, actor, subject string) error
	Resource(r *http.Request) string
}

// AgentGrants lists the agents authorised.
type AgentGrants interface {
	DirectoryReader
	ListOAuthGrants(ctx context.Context, subject string) ([]store.OAuthGrant, error)
}

// AgentsHandler carries the consent and the agents' list.
type AgentsHandler struct {
	authority AgentAuthority
	grants    AgentGrants
	logger    *slog.Logger
}

func NewAgentsHandler(authority AgentAuthority, grants AgentGrants, logger *slog.Logger) *AgentsHandler {
	return &AgentsHandler{authority: authority, grants: grants, logger: logger}
}

// Mount adds the routes to the API router.
func (h *AgentsHandler) Mount(mux *http.ServeMux, middleware *Middleware) {
	mux.Handle("GET /oauth/requests/{id}", Chain(http.HandlerFunc(h.Request), middleware.Authenticate))
	mux.Handle("POST /oauth/requests/{id}/approve", Chain(http.HandlerFunc(h.Approve),
		middleware.Authenticate, middleware.CSRF))
	mux.Handle("POST /oauth/requests/{id}/deny", Chain(http.HandlerFunc(h.Deny),
		middleware.Authenticate, middleware.CSRF))
	// Everyone manages their own agents; an administrator sees and withdraws everyone's.
	mux.Handle("GET /agents", Chain(http.HandlerFunc(h.List), middleware.Authenticate))
	mux.Handle("DELETE /agents/{id}", Chain(http.HandlerFunc(h.Revoke),
		middleware.Authenticate, middleware.CSRF))
}

// Request shows an authorisation request to the person asked to consent.
func (h *AgentsHandler) Request(w http.ResponseWriter, r *http.Request) {
	pending, err := h.authority.Pending(r.Context(), r.PathValue("id"))
	if errors.Is(err, oauth.ErrUnknownRequest) {
		writeProblem(w, http.StatusNotFound, "Unknown or expired request",
			"Start the connection again from the agent.")
		return
	}
	if err != nil {
		h.logger.Error("reading an authorisation request", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Authorisation unavailable", "")
		return
	}
	writeJSON(w, http.StatusOK, pending)
}

// Approve grants the request with the scopes kept, and returns where to send the browser.
func (h *AgentsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	var body struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	redirect, err := h.authority.Approve(r.Context(), r.PathValue("id"), identity, body.Scopes)
	h.answer(w, redirect, err)
}

// Deny refuses the request, and returns where to send the browser.
func (h *AgentsHandler) Deny(w http.ResponseWriter, r *http.Request) {
	redirect, err := h.authority.Deny(r.Context(), r.PathValue("id"))
	h.answer(w, redirect, err)
}

func (h *AgentsHandler) answer(w http.ResponseWriter, redirect string, err error) {
	if errors.Is(err, oauth.ErrUnknownRequest) {
		writeProblem(w, http.StatusNotFound, "Unknown or expired request",
			"Start the connection again from the agent.")
		return
	}
	if err != nil {
		h.logger.Error("answering an authorisation request", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Authorisation unavailable", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": redirect})
}

type agentView struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	Owner    string     `json:"owner,omitempty"`
	Scopes   []string   `json:"scopes"`
	Created  time.Time  `json:"created_at"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

// List returns the agents the caller authorised, or everyone's to an administrator.
func (h *AgentsHandler) List(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	admin := identity.Role.Allows(auth.RoleAdmin)
	subject := identity.Subject
	if admin {
		subject = ""
	}
	grants, err := h.grants.ListOAuthGrants(r.Context(), subject)
	if err != nil {
		h.logger.Error("listing the agents", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot read the agents", "")
		return
	}

	who := resolveActors(r.Context(), h.grants, h.logger)
	views := make([]agentView, 0, len(grants))
	for _, grant := range grants {
		view := agentView{
			ID: grant.ID, Name: grant.ClientName, Scopes: grant.Scopes, Created: grant.CreatedAt,
			LastUsed: grant.LastUsed,
		}
		if admin {
			view.Owner = who.name(grant.Subject)
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mcp_url": h.authority.Resource(r),
		"all":     admin,
		"agents":  views,
	})
}

// Revoke withdraws an agent: the caller's own, or anyone's for an administrator.
func (h *AgentsHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Unknown agent", "")
		return
	}
	subject := identity.Subject
	if identity.Role.Allows(auth.RoleAdmin) {
		subject = ""
	}
	err = h.authority.Revoke(r.Context(), id, identity.Subject, subject)
	if errors.Is(err, store.ErrOAuthNotFound) {
		writeProblem(w, http.StatusNotFound, "Unknown agent", "")
		return
	}
	if err != nil {
		h.logger.Error("revoking an agent", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot revoke the agent", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
