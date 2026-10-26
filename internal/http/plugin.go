package http

import (
	"context"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

/*
 * What a set of routes mounted beside the core's borrows from it: the multiplexer and the
 * middleware through [Mount], and the API's answer format.
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

// RequestProject resolves the project the path names, or writes the response.
func RequestProject(w http.ResponseWriter, r *http.Request, source ProjectResolver) (int64, bool) {
	return requestProject(w, r, source)
}

// ContextWithIdentity sets an identity as authentication would. It serves the plugin
// handlers' tests.
func ContextWithIdentity(ctx context.Context, identity auth.Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}
