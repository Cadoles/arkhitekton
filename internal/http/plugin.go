package http

import (
	"context"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

/*
 * Ce qu'un plugin qui sert ses propres routes emprunte au noyau.
 *
 * Le plugin MSE importe ce paquet ; l'inverse est interdit (doc 16 §16.6). Il
 * reçoit donc le multiplexeur et le middleware par [Mount], et répond dans le
 * même format que le reste de l'API.
 */

// Mount monte les routes d'un plugin. Chaque route pose elle-même son contrôle
// d'accès, [Middleware.RequireModule] compris.
type Mount func(mux *http.ServeMux, middleware *Middleware)

// ProjectResolver traduit le nom porté par une URL en projet.
type ProjectResolver = projectResolver

// WriteJSON rend une réponse JSON.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	writeJSON(w, status, payload)
}

// WriteProblem rend une erreur au format RFC 9457.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	writeProblem(w, status, title, detail)
}

// RequestProject résout le projet désigné par le chemin, ou écrit la réponse.
func RequestProject(w http.ResponseWriter, r *http.Request, source ProjectResolver) (int64, bool) {
	return requestProject(w, r, source)
}

// ContextWithIdentity pose une identité comme le ferait l'authentification. Sert
// aux tests des handlers de plugin.
func ContextWithIdentity(ctx context.Context, identity auth.Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}
