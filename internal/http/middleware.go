package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
)

type contextKey string

const identityKey contextKey = "identity"

// IdentityFrom rend l'identité posée par le middleware d'authentification.
func IdentityFrom(ctx context.Context) (auth.Identity, bool) {
	identity, ok := ctx.Value(identityKey).(auth.Identity)
	return identity, ok
}

// Middleware assemble authentification, CSRF et autorisation.
type Middleware struct {
	sessions   Sessions
	authorizer *auth.Authorizer
	// projects traduit le nom porté par une URL en projet, pour les contrôles
	// rattachés à un projet. Nil tant qu'il n'est pas branché : [Middleware.RequireIn]
	// refuse alors tout, ce qui est le bon sens du doute — sans projet résolu, il
	// n'y a pas de rôle à vérifier.
	projects projectResolver
	logger   *slog.Logger
}

func NewMiddleware(sessions Sessions, authorizer *auth.Authorizer, logger *slog.Logger) *Middleware {
	return &Middleware{sessions: sessions, authorizer: authorizer, logger: logger}
}

// WithProjects branche la résolution des projets pour les contrôles rattachés.
func (m *Middleware) WithProjects(projects projectResolver) *Middleware {
	m.projects = projects
	return m
}

// Authenticate résout la session et pose l'identité dans le contexte.
//
// Un utilisateur authentifié sans rôle reçoit un 403 explicite plutôt qu'un 401 :
// se réauthentifier n'y changerait rien, et le message doit dire quoi faire —
// demander une attribution — plutôt que de renvoyer vers la page de connexion en
// boucle.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
			return
		}

		session, role, projects, err := m.sessions.LookupRoles(r.Context(), cookie.Value)
		switch {
		case errors.Is(err, auth.ErrNoRole):
			m.logger.Info("utilisateur sans rôle", "subject", session.Subject, "username", session.Username)
			writeProblem(w, http.StatusForbidden, "Aucun rôle attribué",
				"Votre compte est reconnu mais aucun rôle ne lui est attribué. Demandez une attribution à un administrateur.")
			return
		case errors.Is(err, auth.ErrSessionNotFound):
			writeProblem(w, http.StatusUnauthorized, "Session expirée", "")
			return
		case err != nil:
			m.logger.Error("résolution de session", "error", err)
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

// safeMethods ne modifient rien et ne sont pas soumises au contrôle CSRF.
var safeMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions}

// CSRF exige la double soumission sur les méthodes non sûres.
//
// SameSite=Lax couvre la navigation intersite mais pas une requête émise par une
// page tierce déjà chargée. Le frontend lit le cookie CSRF et le renvoie en
// en-tête ; une page tierce peut déclencher la requête, jamais lire le cookie.
func (m *Middleware) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(safeMethods, r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(csrfCookieName)
		header := r.Header.Get(csrfHeaderName)

		if err != nil || cookie.Value == "" || header == "" || !auth.ConstantTimeEqual(cookie.Value, header) {
			m.logger.Warn("requête rejetée par le contrôle CSRF", "method", r.Method, "path", r.URL.Path)
			writeProblem(w, http.StatusForbidden, "Jeton CSRF absent ou invalide", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Require exige une permission. L'autorisation est décidée ici, jamais dans un
// handler : un contrôle dispersé finit par manquer sur une route ajoutée plus
// tard.
func (m *Middleware) Require(permission auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
				return
			}
			if !m.authorizer.Can(identity, permission) {
				m.logger.Info("permission refusée",
					"subject", identity.Subject, "role", identity.Role, "permission", permission)
				writeProblem(w, http.StatusForbidden, "Permission insuffisante", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireIn exige une permission **dans le projet que le chemin désigne**.
//
// Distincte de [Require] et non un paramètre : une route rattachée à un projet
// qui vérifierait globalement accorderait sur tous les projets ce qui n'a été
// accordé que sur un. L'écart se verrait mal en relecture ; deux noms se
// distinguent.
//
// Un projet inconnu est refusé ici plutôt que par le handler : sans identifiant,
// il n'y a pas de rôle à vérifier, et laisser passer reviendrait à décider que
// c'est permis.
func (m *Middleware) RequireIn(permission auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
				return
			}

			if m.projects == nil {
				m.logger.Error("contrôle par projet sans résolution branchée",
					"permission", permission, "path", r.URL.Path)
				writeProblem(w, http.StatusInternalServerError, "Autorisation indisponible", "")
				return
			}

			projectID, ok := requestProject(w, r, m.projects)
			if !ok {
				return
			}

			if !m.authorizer.CanIn(identity, projectID, permission) {
				m.logger.Info("permission refusée",
					"subject", identity.Subject, "role", identity.RoleIn(projectID),
					"projet", projectID, "permission", permission)
				writeProblem(w, http.StatusForbidden, "Permission insuffisante", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireDeploy exige la permission de promotion d'un environnement, lu dans le
// segment de chemin indiqué, **dans le projet que le chemin désigne**.
//
// Promouvoir est le geste le plus coûteux du dashboard : il pousse une image vers
// un environnement réel. Le vérifier globalement accorderait sur tous les projets
// ce qui n'a été accordé que sur un — précisément ce que cette tranche existe pour
// empêcher.
func (m *Middleware) RequireDeploy(pathValue string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
				return
			}
			if m.projects == nil {
				m.logger.Error("contrôle de promotion sans résolution de projet branchée",
					"path", r.URL.Path)
				writeProblem(w, http.StatusInternalServerError, "Autorisation indisponible", "")
				return
			}

			projectID, ok := requestProject(w, r, m.projects)
			if !ok {
				return
			}

			environment := r.PathValue(pathValue)
			if !m.authorizer.CanIn(identity, projectID, auth.PermDeploy(environment)) {
				m.logger.Info("promotion refusée",
					"subject", identity.Subject, "role", identity.RoleIn(projectID),
					"projet", projectID, "environment", environment)
				writeProblem(w, http.StatusForbidden, "Permission insuffisante", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Chain applique les middlewares dans l'ordre de lecture : Chain(h, a, b) exécute
// a, puis b, puis h.
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

// writeProblem rend une erreur exploitable par le frontend.
//
// Le détail est destiné à l'utilisateur : il ne doit jamais porter d'information
// d'infrastructure. Les causes techniques vont au log.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Title: title, Status: status, Detail: detail})
}
