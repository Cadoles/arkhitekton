package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

type contextKey string

const identityKey contextKey = "identity"

// IdentityFrom returns the identity the authentication middleware set.
func IdentityFrom(ctx context.Context) (auth.Identity, bool) {
	identity, ok := ctx.Value(identityKey).(auth.Identity)
	return identity, ok
}

// Middleware assemble authentification, CSRF et autorisation.
type Middleware struct {
	sessions   Sessions
	authorizer *auth.Authorizer
	// projects translates the name a URL carries into a project, for the project-scoped
	// checks. Nil while it is not wired: [Middleware.RequireIn] then refuses everything, which
	// is the right benefit of the doubt — with no resolved project, there is no role to check.
	projects projectResolver
	// modules says which modules are switched on. Nil: all of them are.
	modules *Modules
	logger  *slog.Logger
}

func NewMiddleware(sessions Sessions, authorizer *auth.Authorizer, logger *slog.Logger) *Middleware {
	return &Middleware{sessions: sessions, authorizer: authorizer, logger: logger}
}

// WithProjects wires project resolution for the scoped checks.
func (m *Middleware) WithProjects(projects projectResolver) *Middleware {
	m.projects = projects
	return m
}

// Authenticate resolves the session and puts the identity into the context.
//
// An authenticated user with no role receives an explicit 403 rather than a 401:
// re-authenticating would change nothing, and the message must say what to do — ask for a
// grant — rather than send them back to the sign-in page in a loop.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
			return
		}

		session, role, projects, err := m.sessions.LookupRoles(r.Context(), cookie.Value)
		switch {
		case errors.Is(err, auth.ErrNoRole):
			m.logger.Info("user with no role", "subject", session.Subject, "username", session.Username)
			writeProblem(w, http.StatusForbidden, "No role granted",
				"Your account is recognised but no role is granted to it. Ask an administrator to grant one.")
			return
		case errors.Is(err, auth.ErrSessionNotFound):
			writeProblem(w, http.StatusUnauthorized, "Session expired", "")
			return
		case err != nil:
			m.logger.Error("resolving the session", "error", err)
			writeProblem(w, http.StatusInternalServerError, "Session indisponible", "")
			return
		}

		identity := auth.Identity{
			Subject:  session.Subject,
			Username: session.Username,
			Email:    session.Email,
			Role:     role,
			Projects: projects,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, identity)))
	})
}

// safeMethods modify nothing and are not subject to the CSRF check.
var safeMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions}

// CSRF requires double submission on unsafe methods.
//
// SameSite=Lax covers cross-site navigation but not a request issued by a third-party page
// already loaded. The front end reads the CSRF cookie and returns it in a header; a
// third-party page can trigger the request, never read the cookie.
func (m *Middleware) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(safeMethods, r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(csrfCookieName)
		header := r.Header.Get(csrfHeaderName)

		if err != nil || cookie.Value == "" || header == "" || !auth.ConstantTimeEqual(cookie.Value, header) {
			m.logger.Warn("request rejected by the CSRF check", "method", r.Method, "path", r.URL.Path)
			writeProblem(w, http.StatusForbidden, "CSRF token missing or invalid", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Require demands a permission. Authorization is decided here, never in a handler: a
// scattered check ends up missing on a route added later.
func (m *Middleware) Require(permission auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
				return
			}
			if !m.authorizer.Can(identity, permission) {
				m.logger.Info("permission refused",
					"subject", identity.Subject, "role", identity.Role, "permission", permission)
				writeProblem(w, http.StatusForbidden, "Permission insuffisante", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireIn demands a permission in the project the path names.
//
// Distinct from [Require] and not a parameter: a project-scoped route checking globally
// would grant on every project what was granted on one. The discrepancy would be hard to
// see on re-reading; two names are distinguishable.
//
// An unknown project is refused here rather than by the handler: with no identifier there
// is no role to check, and letting it through would amount to deciding it is allowed.
func (m *Middleware) RequireIn(permission auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
				return
			}

			if m.projects == nil {
				m.logger.Error("per-project check with no resolution wired",
					"permission", permission, "path", r.URL.Path)
				writeProblem(w, http.StatusInternalServerError, "Autorisation indisponible", "")
				return
			}

			projectID, ok := requestProject(w, r, m.projects)
			if !ok {
				return
			}

			if !m.authorizer.CanIn(identity, projectID, permission) {
				m.logger.Info("permission refused",
					"subject", identity.Subject, "role", identity.RoleIn(projectID),
					"projet", projectID, "permission", permission)
				writeProblem(w, http.StatusForbidden, "Permission insuffisante", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireDeploy demands the promotion permission of an environment, read from the given
// path segment, in the project the path names.
//
// Promoting is the dashboard's most committing gesture: it pushes an image to a real
// environment. Checking it globally would grant on every project what was granted on one.
func (m *Middleware) RequireDeploy(pathValue string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
				return
			}
			if m.projects == nil {
				m.logger.Error("promotion check with no project resolution wired",
					"path", r.URL.Path)
				writeProblem(w, http.StatusInternalServerError, "Autorisation indisponible", "")
				return
			}

			projectID, ok := requestProject(w, r, m.projects)
			if !ok {
				return
			}

			environment := r.PathValue(pathValue)
			if !m.authorizer.CanDeploy(identity, projectID, r.PathValue("project"), environment) {
				m.logger.Info("promotion refused",
					"subject", identity.Subject, "role", identity.RoleIn(projectID),
					"projet", projectID, "environment", environment)
				writeProblem(w, http.StatusForbidden, "Permission insuffisante", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Chain applies the middlewares in reading order: Chain(h, a, b) runs a, then b, then h.
func Chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// problem suit le format RFC 9457.
type problem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// writeProblem returns an error the front end can use.
//
// The detail is meant for the user: it must never carry infrastructure information.
// Technical causes go to the log.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Title: title, Status: status, Detail: detail})
}
