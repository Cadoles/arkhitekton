package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/native"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

/*
 * The routes an installed plugin's program serves.
 *
 * Two routes are mounted once, whatever is installed: a project's, under
 * /projects/{project}/plugins/{plugin}/, and the instance's, under /plugins/{plugin}/. What lies
 * below is looked up at each request among the routes the active version declares, so that
 * installing, updating or removing a plugin takes effect without a restart.
 *
 * Everything that is about security is done here, before the program sees the request: the
 * caller is authenticated, an unsafe method carries its CSRF token, the project switches the
 * plugin on, and the caller holds the permission the route declares. The program receives the
 * caller and the project, never the session.
 */

const (
	// maxPluginRequest bounds a request's body handed to a program.
	maxPluginRequest = 4 << 20
	// pluginTimeout bounds a program's answer.
	pluginTimeout = 30 * time.Second
)

// PluginPrograms are the installed plugins' programs, as the API reaches them.
type PluginPrograms interface {
	// Routes returns the routes the active version of a plugin declares; false when no active
	// version has a program.
	Routes(pluginID string) ([]manifest.Route, bool)
	// Serve hands a request to the plugin's program.
	Serve(ctx context.Context, pluginID string, request *pluginv1.HTTPRequest) (*pluginv1.HTTPResponse, error)
}

// PluginGate says whether a plugin is switched on, for a project or, with no project, for the
// instance.
type PluginGate interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
}

// PluginRoutesHandler serves the routes installed plugins declare.
type PluginRoutesHandler struct {
	programs   PluginPrograms
	projects   projectResolver
	gate       PluginGate
	authorizer *auth.Authorizer
}

// NewPluginRoutesHandler returns the handler of installed plugins' routes.
func NewPluginRoutesHandler(programs PluginPrograms, projects projectResolver, gate PluginGate, authorizer *auth.Authorizer) *PluginRoutesHandler {
	return &PluginRoutesHandler{programs: programs, projects: projects, gate: gate, authorizer: authorizer}
}

// Routes mounts the two prefixes.
func (h *PluginRoutesHandler) Routes(mux *http.ServeMux, middleware *Middleware) {
	mux.Handle("/projects/{project}/plugins/{plugin}/{path...}",
		Chain(http.HandlerFunc(h.serveProject), middleware.Authenticate, middleware.CSRF))
	mux.Handle("/plugins/{plugin}/{path...}",
		Chain(http.HandlerFunc(h.serveInstance), middleware.Authenticate, middleware.CSRF))
}

func (h *PluginRoutesHandler) serveProject(w http.ResponseWriter, r *http.Request) {
	project, err := requestProjectRecord(w, r, h.projects)
	if err != nil {
		return
	}
	h.serve(w, r, manifest.ScopeProject, &project)
}

func (h *PluginRoutesHandler) serveInstance(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, manifest.ScopeInstance, nil)
}

func (h *PluginRoutesHandler) serve(w http.ResponseWriter, r *http.Request, scope string, project *store.Project) {
	pluginID, path := r.PathValue("plugin"), r.PathValue("path")
	identity, _ := IdentityFrom(r.Context())

	routes, found := h.programs.Routes(pluginID)
	if !found {
		writeProblem(w, http.StatusNotFound, "No such plugin route", "")
		return
	}
	route, parameters, found := matchRoute(routes, scope, r.Method, path)
	if !found {
		writeProblem(w, http.StatusNotFound, "No such plugin route", "")
		return
	}
	// A plugin switched off answers as if it were not installed: its routes are not a way around
	// the switch.
	var projectID int64
	if project != nil {
		projectID = project.ID
	}
	if settings, err := h.gate.ResolvedPlugin(r.Context(), projectID, pluginID); errors.Is(err, store.ErrPluginDisabled) ||
		(err == nil && !settings.Enabled) {
		writeProblem(w, http.StatusNotFound, "No such plugin route", "")
		return
	} else if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plugin's settings", "")
		return
	}
	if !h.allowed(identity, route, parameters, project) {
		writeProblem(w, http.StatusForbidden, "Permission required", route.Permission)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxPluginRequest+1))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	if len(body) > maxPluginRequest {
		writeProblem(w, http.StatusRequestEntityTooLarge, "Request too large", "4 MiB at most")
		return
	}
	request := &pluginv1.HTTPRequest{
		Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
		Headers: forwarded(r.Header, "Content-Type", "Accept", "Accept-Language"), Identity: native.IdentityTo(identity),
	}
	if project != nil {
		request.Project = native.ProjectTo(*project)
	}

	ctx, cancel := context.WithTimeout(r.Context(), pluginTimeout)
	defer cancel()
	response, err := h.programs.Serve(ctx, pluginID, request)
	if err != nil {
		writePluginError(w, err)
		return
	}
	for name, value := range forwardedMap(response.GetHeaders(), "Content-Type", "Cache-Control", "Content-Disposition", "Location") {
		w.Header().Set(name, value)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	code := int(response.GetStatus())
	if code < 100 || code > 599 {
		code = http.StatusBadGateway
	}
	w.WriteHeader(code)
	_, _ = w.Write(response.GetBody())
}

// allowed checks the permission the route declares. A deploy permission names the environment
// from the path, and is judged as a promotion is.
func (h *PluginRoutesHandler) allowed(identity auth.Identity, route manifest.Route, parameters map[string]string, project *store.Project) bool {
	if parameter, deploy := route.DeployParameter(); deploy {
		return project != nil && h.authorizer.CanDeploy(identity, project.ID, project.Slug, parameters[parameter])
	}
	if project != nil {
		return h.authorizer.CanIn(identity, project.ID, auth.Permission(route.Permission))
	}
	return h.authorizer.Can(identity, auth.Permission(route.Permission))
}

// matchRoute finds the declared route a request's method and path match, and its parameters.
func matchRoute(routes []manifest.Route, scope, method, path string) (manifest.Route, map[string]string, bool) {
	segments := strings.Split(path, "/")
	for _, route := range routes {
		if route.EffectiveScope() != scope || route.Method != method {
			continue
		}
		if parameters, ok := matchPath(strings.Split(route.Path, "/"), segments); ok {
			return route, parameters, true
		}
	}
	return manifest.Route{}, nil, false
}

func matchPath(pattern, segments []string) (map[string]string, bool) {
	parameters := map[string]string{}
	for i, part := range pattern {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "...}") {
			parameters[strings.TrimSuffix(strings.TrimPrefix(part, "{"), "...}")] = strings.Join(segments[min(i, len(segments)):], "/")
			return parameters, true
		}
		if i >= len(segments) || segments[i] == "" {
			return nil, false
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			parameters[strings.Trim(part, "{}")] = segments[i]
			continue
		}
		if part != segments[i] {
			return nil, false
		}
	}
	return parameters, len(pattern) == len(segments)
}

func forwarded(header http.Header, names ...string) map[string]string {
	kept := map[string]string{}
	for _, name := range names {
		if value := header.Get(name); value != "" {
			kept[name] = value
		}
	}
	return kept
}

func forwardedMap(headers map[string]string, names ...string) map[string]string {
	kept := map[string]string{}
	for name, value := range headers {
		for _, allowed := range names {
			if strings.EqualFold(name, allowed) {
				kept[allowed] = value
			}
		}
	}
	return kept
}

// writePluginError says what became of the program, without its message: it may name a path or a
// host of the pod.
func writePluginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, native.ErrRestarting):
		writeProblem(w, http.StatusServiceUnavailable, "The plugin is restarting", "")
	case status.Code(err) == codes.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded):
		writeProblem(w, http.StatusGatewayTimeout, "The plugin did not answer in time", "")
	case status.Code(err) == codes.Unimplemented:
		writeProblem(w, http.StatusNotFound, "No such plugin route", "")
	default:
		writeProblem(w, http.StatusBadGateway, "The plugin failed", "")
	}
}
