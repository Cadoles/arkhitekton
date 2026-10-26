// Package extension is what a native plugin written in Go imports: it serves the plugin's hooks to
// the dashboard that launched it, and lends it the dashboard's Host service.
//
//	func main() {
//		extension.Serve(func(ctx context.Context, runtime extension.Runtime) (extension.Hooks, error) {
//			return extension.Hooks{Routes: routes(runtime.Host)}, nil
//		})
//	}
//
// The program is launched by the dashboard, never by hand: it refuses to start without the
// handshake the dashboard provides. A plugin logs on its standard error, which the dashboard
// collects.
package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/records"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// Runtime is what the plugin is started with.
type Runtime struct {
	Host Host
	// Role is rpc.RoleAPI, rpc.RoleWorker or rpc.RoleReprocess. A module runs in the worker only.
	Role     string
	PluginID string
	// DatabaseURL reaches the dashboard's database, where the plugin keeps its own tables.
	DatabaseURL string
}

// Hooks are what the plugin adds to the dashboard. Every field is optional; a hook the manifest
// does not declare is never called.
type Hooks struct {
	// Interpreters read the runs of the plugin's pipelines, by pipeline name.
	Interpreters map[string]capability.Interpreter
	// Routes serve the routes the manifest declares. A request's path is the route's, below the
	// plugin's prefix; its context carries the caller ([IdentityFrom]) and, for a project's route,
	// the project ([ProjectFrom]).
	Routes http.Handler
	// Deployments returns, per release tag, the environments running it.
	Deployments func(ctx context.Context, projectID int64) (map[string][]string, error)
	// Promotions returns a release's promotions.
	Promotions func(ctx context.Context, projectID, releaseID int64) ([]Promotion, error)
	// EnvironmentSuggestions returns the environments offered when a run asks for one; nil offers
	// none.
	EnvironmentSuggestions func(ctx context.Context, projectSlug string) []Suggestion
	// CheckScheme refuses a version scheme the plugin cannot deploy.
	CheckScheme func(ctx context.Context, scheme string) error
	// Promoted states the fact of a concluded promotion; false states none.
	Promoted func(at time.Time, linked JobReleaseContext) (Event, bool)
	// ProjectSettings returns the configuration values a project adds to its pipelines.
	ProjectSettings func(ctx context.Context, projectID int64) (map[string]string, error)
	// Module runs until its context ends, for the projects that switch the plugin on. It is
	// started again with the new state when settings or projects change.
	Module func(ctx context.Context, state ModuleState) error
}

// Start builds the plugin's hooks, once, when the dashboard initialises it.
type Start func(ctx context.Context, runtime Runtime) (Hooks, error)

// Serve runs the plugin until the dashboard stops it.
func Serve(start Start) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: rpc.Handshake,
		Plugins: rpc.PluginSet(&rpc.GRPCPlugin{Implementation: func(broker *goplugin.GRPCBroker) pluginv1.PluginServer {
			return &server{start: start, broker: broker}
		}}),
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}

// Server returns the gRPC implementation of a plugin, for tests that connect to it in memory.
func Server(start Start, broker *goplugin.GRPCBroker) pluginv1.PluginServer {
	return &server{start: start, broker: broker}
}

type server struct {
	pluginv1.UnimplementedPluginServer
	start  Start
	broker *goplugin.GRPCBroker

	mu    sync.RWMutex
	hooks *Hooks
}

func (s *server) Init(ctx context.Context, request *pluginv1.InitRequest) (*pluginv1.InitResponse, error) {
	conn, err := s.broker.Dial(request.GetHostBrokerId())
	if err != nil {
		return nil, fmt.Errorf("reaching the dashboard: %w", err)
	}
	hooks, err := s.start(ctx, Runtime{
		Host: hostClient{client: pluginv1.NewHostClient(conn)}, Role: request.GetRole(),
		PluginID: request.GetPluginId(), DatabaseURL: os.Getenv("DATABASE_URL"),
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.hooks = &hooks
	s.mu.Unlock()
	return &pluginv1.InitResponse{}, nil
}

func (s *server) started() (*Hooks, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hooks == nil {
		return nil, status.Error(codes.FailedPrecondition, "the plugin is not initialised")
	}
	return s.hooks, nil
}

func unimplemented(hook string) error {
	return status.Errorf(codes.Unimplemented, "the plugin has no %s", hook)
}

func (s *server) Interpret(ctx context.Context, request *pluginv1.InterpretRequest) (*pluginv1.InterpretResponse, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	interpreter := hooks.Interpreters[request.GetPipeline()]
	if interpreter == nil {
		return nil, unimplemented("interpreter for " + request.GetPipeline())
	}
	var run capability.RunState
	if err := json.Unmarshal(request.GetRunState(), &run); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "run state: %v", err)
	}
	var wires []records.Wire
	if err := json.Unmarshal(request.GetRecords(), &wires); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "records: %v", err)
	}
	decoded := make([]parse.Record, 0, len(wires))
	for _, wire := range wires {
		record, err := records.Decode(wire)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		decoded = append(decoded, record)
	}
	sets, err := interpret(ctx, interpreter, run, decoded)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(sets)
	if err != nil {
		return nil, fmt.Errorf("encoding the facts: %w", err)
	}
	return &pluginv1.InterpretResponse{Facts: encoded}, nil
}

// interpret keeps a panicking interpreter from bringing the plugin down: the run is read as
// failed, the other runs are still read.
func interpret(ctx context.Context, interpreter capability.Interpreter, run capability.RunState, records []parse.Record) (sets []facts.Set, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("the interpreter panicked: %v", recovered)
		}
	}()
	return interpreter.Interpret(ctx, run, records)
}

// maxResponse bounds what a route answers: the dashboard refuses more.
const maxResponse = 16 << 20

func (s *server) Serve(ctx context.Context, request *pluginv1.HTTPRequest) (*pluginv1.HTTPResponse, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Routes == nil {
		return nil, unimplemented("routes")
	}
	ctx = WithIdentity(ctx, identityFrom(request.GetIdentity()))
	if project := request.GetProject(); project != nil {
		ctx = WithProject(ctx, projectFrom(project))
	}
	target := "/" + strings.TrimPrefix(request.GetPath(), "/")
	if query := request.GetQuery(); query != "" {
		target += "?" + query
	}
	httpRequest := httptest.NewRequestWithContext(ctx, request.GetMethod(), target,
		strings.NewReader(string(request.GetBody())))
	for name, value := range request.GetHeaders() {
		httpRequest.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	hooks.Routes.ServeHTTP(recorder, httpRequest)

	result := recorder.Result()
	defer func() { _ = result.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(result.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponse {
		return nil, status.Error(codes.ResourceExhausted, "the response exceeds 16 MiB")
	}
	headers := make(map[string]string, len(result.Header))
	for name := range result.Header {
		headers[name] = result.Header.Get(name)
	}
	return &pluginv1.HTTPResponse{Status: int32(result.StatusCode), Headers: headers, Body: body}, nil
}

func (s *server) Deployments(ctx context.Context, request *pluginv1.ProjectRef) (*pluginv1.DeploymentMap, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Deployments == nil {
		return nil, unimplemented("deployments")
	}
	deployed, err := hooks.Deployments(ctx, request.GetProjectId())
	if err != nil {
		return nil, err
	}
	byTag := make(map[string]*pluginv1.EnvironmentIDs, len(deployed))
	for tag, environments := range deployed {
		byTag[tag] = &pluginv1.EnvironmentIDs{Ids: environments}
	}
	return &pluginv1.DeploymentMap{ByTag: byTag}, nil
}

func (s *server) Promotions(ctx context.Context, request *pluginv1.ReleaseRef) (*pluginv1.PromotionList, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Promotions == nil {
		return nil, unimplemented("promotions")
	}
	promotions, err := hooks.Promotions(ctx, request.GetProjectId(), request.GetReleaseId())
	if err != nil {
		return nil, err
	}
	list := &pluginv1.PromotionList{}
	for _, promotion := range promotions {
		list.Promotions = append(list.Promotions, &pluginv1.Promotion{
			Environment: promotion.Environment, Image: promotion.Image, Tag: promotion.Tag,
			Digest: promotion.Digest, JobId: promotion.JobID, PushedAt: formatTime(promotion.PushedAt),
		})
	}
	return list, nil
}

func (s *server) EnvironmentSuggestions(ctx context.Context, request *pluginv1.ProjectSlug) (*pluginv1.Suggestions, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.EnvironmentSuggestions == nil {
		return nil, unimplemented("environment suggestions")
	}
	suggestions := hooks.EnvironmentSuggestions(ctx, request.GetSlug())
	if suggestions == nil {
		return &pluginv1.Suggestions{}, nil
	}
	answer := &pluginv1.Suggestions{Offered: true}
	for _, suggestion := range suggestions {
		answer.Suggestions = append(answer.Suggestions, &pluginv1.Suggestion{Value: suggestion.Value, Label: suggestion.Label})
	}
	return answer, nil
}

func (s *server) CheckScheme(ctx context.Context, request *pluginv1.Scheme) (*pluginv1.Refusal, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.CheckScheme == nil {
		return nil, unimplemented("scheme check")
	}
	if err := hooks.CheckScheme(ctx, request.GetId()); err != nil {
		return &pluginv1.Refusal{Reason: err.Error()}, nil
	}
	return &pluginv1.Refusal{}, nil
}

func (s *server) Promoted(_ context.Context, request *pluginv1.PromotedRequest) (*pluginv1.Event, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.Promoted == nil {
		return nil, unimplemented("promotion facts")
	}
	event, stated := hooks.Promoted(parseTime(request.GetOccurredAt()), JobReleaseContext{
		ReleaseID: request.GetReleaseId(), JobID: request.GetJobId(), Kind: request.GetKind(),
		Tag: request.GetTag(), State: request.GetState(), Environment: request.GetEnvironment(),
		GitRef: request.GetGitRef(), GitCommit: request.GetGitCommit(), ProjectID: request.GetProjectId(),
	})
	if !stated {
		return &pluginv1.Event{}, nil
	}
	return &pluginv1.Event{
		Stated: true, Type: event.Type, Subject: event.Subject, OccurredAt: formatTime(event.OccurredAt),
		Payload: event.Payload, DedupKey: event.DedupKey,
	}, nil
}

func (s *server) ProjectSettings(ctx context.Context, request *pluginv1.ProjectRef) (*pluginv1.Settings, error) {
	hooks, err := s.started()
	if err != nil {
		return nil, err
	}
	if hooks.ProjectSettings == nil {
		return nil, unimplemented("project settings")
	}
	values, err := hooks.ProjectSettings(ctx, request.GetProjectId())
	if err != nil {
		return nil, err
	}
	return &pluginv1.Settings{Values: values}, nil
}

func (s *server) RunModule(request *pluginv1.ModuleState, stream pluginv1.Plugin_RunModuleServer) error {
	hooks, err := s.started()
	if err != nil {
		return err
	}
	if hooks.Module == nil {
		return unimplemented("module")
	}
	state := ModuleState{Global: request.GetGlobal()}
	for _, project := range request.GetProjects() {
		state.Projects = append(state.Projects, ModuleProject{
			Project: projectFrom(project.GetProject()), Settings: project.GetSettings(),
		})
	}
	err = hooks.Module(stream.Context(), state)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type (
	identityKey struct{}
	projectKey  struct{}
)

// WithIdentity sets the caller as the dashboard does before a route: for a plugin's tests.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// WithProject sets the project as the dashboard does before a project's route: for a plugin's
// tests.
func WithProject(ctx context.Context, project Project) context.Context {
	return context.WithValue(ctx, projectKey{}, project)
}

// IdentityFrom returns the caller of a route, authenticated by the dashboard.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}

// ProjectFrom returns the project of a project's route, resolved by the dashboard.
func ProjectFrom(ctx context.Context) (Project, bool) {
	project, ok := ctx.Value(projectKey{}).(Project)
	return project, ok
}

// WriteJSON answers a route with JSON, as the rest of the API does.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteProblem answers a route with an error in RFC 9457 form.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}
