package http

import (
	"net/http"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
)

// NewAPIRouter monte les routes applicatives.
//
// L'autorisation est décidée ici, route par route, et jamais dans un handler :
// un contrôle dispersé finit toujours par manquer sur une route ajoutée plus
// tard. Le tableau ci-dessous est donc la référence de ce qui est protégé.
func NewAPIRouter(handler *AuthHandler, runs *RunsHandler, logs *LogsHandler, middleware *Middleware) http.Handler {
	mux := http.NewServeMux()

	// Routes publiques : elles servent précisément à obtenir une session.
	mux.HandleFunc("GET /auth/login", handler.Login)
	mux.HandleFunc("GET /auth/callback", handler.Callback)

	// La déconnexion modifie l'état, donc passe par le contrôle CSRF, mais
	// n'exige pas de rôle : un utilisateur sans rôle doit pouvoir se déconnecter.
	mux.Handle("POST /auth/logout", Chain(http.HandlerFunc(handler.Logout), middleware.CSRF))

	mux.Handle("GET /me", Chain(http.HandlerFunc(handler.Me), middleware.Authenticate))

	// Les exécutions sont en lecture pour tous les rôles : constater ce que les
	// pipelines ont fait ne demande aucun privilège, et c'est le premier usage
	// réellement utile du dashboard.
	mux.Handle("GET /runs", Chain(http.HandlerFunc(runs.List),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /runs/events", Chain(http.HandlerFunc(runs.Stream),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	// Les journaux se lisent par plage, et se suivent par flux. Les deux passent
	// par le même lecteur : un client peut suivre un run puis remonter dans son
	// historique sans changer de mécanisme.
	mux.Handle("GET /runs/{run}/tasks/{task}/log", Chain(http.HandlerFunc(logs.Read),
		middleware.Authenticate, middleware.Require(auth.PermRead)))
	mux.Handle("GET /runs/{run}/tasks/{task}/log/stream", Chain(http.HandlerFunc(logs.Stream),
		middleware.Authenticate, middleware.Require(auth.PermRead)))

	return mux
}
