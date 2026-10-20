package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

const (
	sessionCookieName = "mse_session"
	csrfCookieName    = "mse_csrf"
	csrfHeaderName    = "X-CSRF-Token"
)

// Sessions describes what the handlers need from the session store.
type Sessions interface {
	Create(ctx context.Context, subject, username, email string) (auth.Session, error)
	// ApplyBootstrap promotes an account declared in configuration to administrator. Called
	// at sign-in: the identity provider's subject is not knowable before.
	ApplyBootstrap(ctx context.Context, subject, username string) (string, error)
	// LookupRoles returns the global role and the per-project roles. Both scopes coexist: a
	// project role makes no sense outside it, and a global role cannot be derived from them.
	LookupRoles(ctx context.Context, id string) (auth.Session, auth.Role, map[int64]auth.Role, error)
	Revoke(ctx context.Context, id string) error
}

// AuthHandler carries the authentication flow.
type AuthHandler struct {
	exchanger auth.Exchanger
	flows     Flows
	sessions  Sessions
	logger    *slog.Logger
	// secureCookies must be true everywhere except in development over http.
	secureCookies bool
	sessionTTL    time.Duration
	// projects lists the declared projects, returned with the identity. Nil when it is not
	// wired: `/me` then answers without a list, and the interface says so rather than
	// composing hollow URLs.
	projects projectLister
	// modules returns the active modules with the identity. Nil: `/me` says nothing about
	// them.
	modules *Modules
}

// WithModules returns the active modules on `/me`.
//
// There because it is the only answer anyone signed in can read: listing the plugins
// requires `admin`, and navigation depends on it from the first screen.
func (h *AuthHandler) WithModules(modules *Modules) *AuthHandler {
	h.modules = modules
	return h
}

// WithProjects wires the resolution of the current project on `/me`.
func (h *AuthHandler) WithProjects(projects projectLister) *AuthHandler {
	h.projects = projects
	return h
}

func NewAuthHandler(
	exchanger auth.Exchanger,
	flows Flows,
	sessions Sessions,
	logger *slog.Logger,
	secureCookies bool,
	sessionTTL time.Duration,
) *AuthHandler {
	return &AuthHandler{
		exchanger:     exchanger,
		flows:         flows,
		sessions:      sessions,
		logger:        logger,
		secureCookies: secureCookies,
		sessionTTL:    sessionTTL,
	}
}

// Flows is what the handler needs from the transaction store.
//
// An interface, and not the concrete type: the in-memory store is enough for tests, while
// production requires shared storage — two api replicas with no session affinity serve
// /auth/login and /auth/callback from different instances, and the state would be missing
// one time in two.
type Flows interface {
	Begin(ctx context.Context, redirectAfter string) (auth.FlowState, error)
	Consume(ctx context.Context, state string) (auth.FlowState, error)
}

// Login opens a transaction and redirects to the provider.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	flow, err := h.flows.Begin(r.Context(), safeRedirect(r.URL.Query().Get("redirect")))
	if err != nil {
		h.logger.Error("opening an authentication transaction", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Authentication unavailable", "")
		return
	}

	http.Redirect(w, r,
		h.exchanger.AuthCodeURL(flow.State, flow.Nonce, auth.CodeChallenge(flow.CodeVerifier)),
		http.StatusFound)
}

// Callback handles the provider's return.
func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// The provider reports a refusal through `error`: telling it from a technical failure
	// avoids showing "internal error" to someone who simply clicked Cancel.
	if providerError := query.Get("error"); providerError != "" {
		h.logger.Info("authentication refused by the provider", "error", providerError)
		writeProblem(w, http.StatusUnauthorized, "Authentication refused",
			"The identity provider refused the request.")
		return
	}

	flow, err := h.flows.Consume(r.Context(), query.Get("state"))
	if err != nil {
		// Replay, expiry or a forged state: all treated alike, and without detail —
		// distinguishing the cases would give an oracle.
		h.logger.Warn("authentication state rejected")
		writeProblem(w, http.StatusBadRequest, "Authentication session expired",
			"Start the sign-in again.")
		return
	}

	code := query.Get("code")
	if code == "" {
		writeProblem(w, http.StatusBadRequest, "Missing authorization code", "")
		return
	}

	claims, err := h.exchanger.Exchange(r.Context(), code, flow.CodeVerifier, flow.Nonce)
	if err != nil {
		h.logger.Error("exchanging the authorization code", "error", err)
		writeProblem(w, http.StatusUnauthorized, "Authentication refused", "")
		return
	}

	// Before the session is created: this is where the provider's subject is finally known,
	// and therefore the only moment a role declared in advance can be attached to it.
	if role, err := h.sessions.ApplyBootstrap(
		r.Context(), claims.Subject, claims.Username); err != nil {
		// The bootstrap is not blocking: failing here would deny sign-in to a user who may
		// already have a role.
		h.logger.Error("bootstrapping a role", "subject", claims.Subject, "error", err)
	} else if role != "" {
		h.logger.Info("bootstrap role granted",
			"subject", claims.Subject, "username", claims.Username, "role", role)
	}

	session, err := h.sessions.Create(r.Context(), claims.Subject, claims.Username, claims.Email)
	if err != nil {
		h.logger.Error("creating a session", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Session unavailable", "")
		return
	}

	h.setSessionCookie(w, session.ID)
	if err := h.setCSRFCookie(w); err != nil {
		h.logger.Error("generating the CSRF token", "error", err)
		writeProblem(w, http.StatusInternalServerError, "Session unavailable", "")
		return
	}

	h.logger.Info("session ouverte", "subject", claims.Subject, "username", claims.Username)
	http.Redirect(w, r, flow.RedirectAfter, http.StatusFound)
}

// Logout revokes the session and clears the cookies.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := h.sessions.Revoke(r.Context(), cookie.Value); err != nil {
			// Revoking in the database failed: clearing the cookie anyway would be misleading, the
			// user would believe they were signed out while their session stays usable by whoever
			// holds it.
			h.logger.Error("revoking a session", "error", err)
			writeProblem(w, http.StatusInternalServerError, "Incomplete sign-out",
				"The session could not be revoked, try again.")
			return
		}
	}

	h.clearCookie(w, sessionCookieName)
	h.clearCookie(w, csrfCookieName)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged-out"})
}

// Me returns the identity and the effective permissions.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	// The current project comes with the identity, and it is the only place anyone signed in
	// can read it: listing the projects requires `admin`, while the interface needs to compose
	// its URLs from the first screen.
	payload := map[string]any{
		"subject":  identity.Subject,
		"username": identity.Username,
		"email":    identity.Email,
		"role":     identity.Role,
	}
	if h.projects != nil {
		// The list, and not a single project. `/me` used to return "the buildable project", which
		// assumed there was only one: declaring a second made the resolution fail, and the
		// interface was left with no composable URL — the whole releases screen with it.
		//
		// It is also what multi-project means on the interface side: the choice belongs to whoever
		// is looking, not to the dashboard.
		projects, err := h.projects.ListProjects(r.Context())
		if err != nil && h.logger != nil {
			h.logger.Warn("project list unavailable", "error", err)
		}
		visible := make([]map[string]any, 0, len(projects))
		for _, project := range projects {
			entry := map[string]any{"slug": project.Slug, "label": project.Label}
			// The project's modules: the interface follows the current project, and a module switched
			// on for the instance may be off for it.
			if h.modules != nil {
				entry["modules"] = h.modules.ActiveIn(r.Context(), project.ID)
			}
			visible = append(visible, entry)
		}
		payload["projects"] = visible
	}
	if h.modules != nil {
		payload["modules"] = h.modules.Active(r.Context())
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: id,
		Path:  "/",
		// HttpOnly: a token readable by a script on the page is a token stealable by any
		// injection.
		HttpOnly: true,
		Secure:   h.secureCookies,
		// Lax and not Strict: Strict would stop the cookie accompanying the provider's return
		// redirect, and the session would look lost just after a successful sign-in.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})
}

// setCSRFCookie sets a token readable by the front end, to be returned in a header on
// unsafe methods. SameSite=Lax covers cross-site navigation, not requests issued by a
// third-party page already loaded.
func (h *AuthHandler) setCSRFCookie(w http.ResponseWriter) error {
	token, err := auth.NewCSRFToken()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:  csrfCookieName,
		Value: token,
		Path:  "/",
		// Deliberately readable by the script: that is the principle of double submission. Its
		// value gives nothing without the session cookie.
		HttpOnly: false,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.sessionTTL.Seconds()),
	})
	return nil
}

func (h *AuthHandler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: name == sessionCookieName,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// safeRedirect accepts only an internal path.
//
// Without that filtering, `?redirect=https://example.invalid` would turn the sign-in page
// into an open redirector: a link to the dashboard, hosted on the right domain, would send
// the authenticated user to a third-party site. Protocol-relative paths count as external.
func safeRedirect(target string) string {
	const fallback = "/"

	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return fallback
	}
	// `/\host` is read as an absolute URL by some browsers.
	if strings.HasPrefix(target, "/\\") {
		return fallback
	}
	return target
}
