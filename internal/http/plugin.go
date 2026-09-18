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

// Mount mounts a plugin's routes. Each route sets its own access control, including
// [Middleware.RequireModule].
type Mount func(mux *http.ServeMux, middleware *Middleware)

// ProjectResolver translates the name a URL carries into a project.
type ProjectResolver = projectResolver

// WriteJSON returns a JSON response.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	writeJSON(w, status, payload)
}

// WriteProblem returns an error in RFC 9457 form.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	writeProblem(w, status, title, detail)
}

// RequestProject résout le projet désigné par le chemin, ou écrit la réponse.
func RequestProject(w http.ResponseWriter, r *http.Request, source ProjectResolver) (int64, bool) {
	return requestProject(w, r, source)
}

// ContextWithIdentity sets an identity as authentication would. It serves the plugin
// handlers' tests.
func ContextWithIdentity(ctx context.Context, identity auth.Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}
