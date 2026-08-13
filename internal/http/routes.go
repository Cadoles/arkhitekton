package http

import "net/http"

// NewAPIRouter monte les routes applicatives.
//
// L'autorisation est décidée ici, route par route, et jamais dans un handler :
// un contrôle dispersé finit toujours par manquer sur une route ajoutée plus
// tard. Le tableau ci-dessous est donc la référence de ce qui est protégé.
func NewAPIRouter(handler *AuthHandler, middleware *Middleware) http.Handler {
	mux := http.NewServeMux()

	// Routes publiques : elles servent précisément à obtenir une session.
	mux.HandleFunc("GET /auth/login", handler.Login)
	mux.HandleFunc("GET /auth/callback", handler.Callback)

	// La déconnexion modifie l'état, donc passe par le contrôle CSRF, mais
	// n'exige pas de rôle : un utilisateur sans rôle doit pouvoir se déconnecter.
	mux.Handle("POST /auth/logout", Chain(http.HandlerFunc(handler.Logout), middleware.CSRF))

	mux.Handle("GET /me", Chain(http.HandlerFunc(handler.Me), middleware.Authenticate))

	return mux
}
