package extension_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/records"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// fakeHost knows one project and nothing else.
type fakeHost struct {
	pluginv1.UnimplementedHostServer
	audited []string
}

func (h *fakeHost) ProjectBySlug(_ context.Context, request *pluginv1.ProjectSlug) (*pluginv1.Project, error) {
	if request.GetSlug() != "app" {
		return nil, status.Error(codes.NotFound, extension.ErrProjectNotFound.Error())
	}
	return &pluginv1.Project{Id: 7, Slug: "app", Label: "Application"}, nil
}

func (h *fakeHost) Audit(_ context.Context, entry *pluginv1.AuditEntry) (*pluginv1.Empty, error) {
	h.audited = append(h.audited, entry.GetAction()+" "+entry.GetTarget())
	return &pluginv1.Empty{}, nil
}

type counter struct{}

func (counter) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	if run.Status == "boom" {
		panic("boom")
	}
	return []facts.Set{{Tool: records[0].Tool, Summary: string(records[0].Payload.(parse.Bytes))}}, nil
}

func start(started *extension.Runtime) extension.Start {
	return func(ctx context.Context, runtime extension.Runtime) (extension.Hooks, error) {
		*started = runtime
		if _, err := runtime.Host.ProjectBySlug(ctx, "missing"); !errors.Is(err, extension.ErrProjectNotFound) {
			return extension.Hooks{}, errors.New("the host's error does not come back as itself")
		}
		mux := http.NewServeMux()
		mux.HandleFunc("POST /things/{name}", func(w http.ResponseWriter, r *http.Request) {
			identity, _ := extension.IdentityFrom(r.Context())
			project, _ := extension.ProjectFrom(r.Context())
			if err := runtime.Host.Audit(r.Context(), identity.Username, "thing.created", r.PathValue("name"), nil); err != nil {
				extension.WriteProblem(w, http.StatusBadGateway, "Audit", err.Error())
				return
			}
			extension.WriteJSON(w, http.StatusCreated, map[string]string{
				"name": r.PathValue("name"), "project": project.Slug, "by": identity.Username, "q": r.URL.Query().Get("q"),
			})
		})
		return extension.Hooks{
			Interpreters: map[string]capability.Interpreter{"unit-tests": counter{}},
			Routes:       mux,
			EnvironmentSuggestions: func(_ context.Context, slug string) []extension.Suggestion {
				if slug != "app" {
					return nil
				}
				return []extension.Suggestion{{Value: "staging", Label: "Staging"}}
			},
			Module: func(ctx context.Context, state extension.ModuleState) error {
				<-ctx.Done()
				return ctx.Err()
			},
		}, nil
	}
}

func connect(t *testing.T, host pluginv1.HostServer) (*rpc.Connection, *extension.Runtime) {
	t.Helper()
	started := &extension.Runtime{}
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		rpc.Name: &rpc.GRPCPlugin{Implementation: func(broker *goplugin.GRPCBroker) pluginv1.PluginServer {
			return extension.Server(start(started), broker)
		}},
	})
	t.Cleanup(func() { _ = client.Close() })
	raw, err := client.Dispense(rpc.Name)
	if err != nil {
		t.Fatal(err)
	}
	connection := raw.(*rpc.Connection)
	if err := connection.Init(context.Background(), host, rpc.RoleAPI, "widgets"); err != nil {
		t.Fatal(err)
	}
	return connection, started
}

func TestAPluginIsInitialisedWithTheHost(t *testing.T) {
	_, started := connect(t, &fakeHost{})
	if started.Role != rpc.RoleAPI || started.PluginID != "widgets" {
		t.Errorf("runtime = %+v", started)
	}
}

func TestARouteIsServedWithItsCallerAndProject(t *testing.T) {
	host := &fakeHost{}
	connection, _ := connect(t, host)

	response, err := connection.Plugin.Serve(context.Background(), &pluginv1.HTTPRequest{
		Method: http.MethodPost, Path: "things/gear", Query: "q=1",
		Identity: &pluginv1.Identity{Username: "alice", Role: "operator"},
		Project:  &pluginv1.Project{Id: 7, Slug: "app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetStatus() != http.StatusCreated || !strings.HasPrefix(response.GetHeaders()["Content-Type"], "application/json") {
		t.Fatalf("response = %d %v", response.GetStatus(), response.GetHeaders())
	}
	var body map[string]string
	if err := json.Unmarshal(response.GetBody(), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "gear" || body["project"] != "app" || body["by"] != "alice" || body["q"] != "1" {
		t.Errorf("body = %v", body)
	}
	if len(host.audited) != 1 || host.audited[0] != "thing.created gear" {
		t.Errorf("the plugin did not reach the host: %v", host.audited)
	}
}

func TestARunIsReadAndAPanicIsAnError(t *testing.T) {
	connection, _ := connect(t, &fakeHost{})
	wire, err := records.Encode(parse.Record{Parser: parse.Raw, Tool: "go", Payload: parse.Bytes("ok")})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]records.Wire{wire})

	read := func(state string) (*pluginv1.InterpretResponse, error) {
		run, _ := json.Marshal(capability.RunState{Status: state})
		return connection.Plugin.Interpret(context.Background(), &pluginv1.InterpretRequest{
			Pipeline: "unit-tests", RunState: run, Records: encoded,
		})
	}
	response, err := read("succeeded")
	if err != nil {
		t.Fatal(err)
	}
	var sets []facts.Set
	if err := json.Unmarshal(response.GetFacts(), &sets); err != nil || len(sets) != 1 || sets[0].Summary != "ok" {
		t.Errorf("facts = %s (%v)", response.GetFacts(), err)
	}
	if _, err := read("boom"); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("a panic answered %v", err)
	}
}

func TestAHookThePluginLacksIsUnimplemented(t *testing.T) {
	connection, _ := connect(t, &fakeHost{})
	_, err := connection.Plugin.Deployments(context.Background(), &pluginv1.ProjectRef{ProjectId: 7})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("err = %v", err)
	}
}

// Offering nothing and offering an empty list are two answers: the first leaves the field free.
func TestSuggestionsSayWhetherTheyAreOffered(t *testing.T) {
	connection, _ := connect(t, &fakeHost{})
	for slug, offered := range map[string]bool{"app": true, "other": false} {
		answer, err := connection.Plugin.EnvironmentSuggestions(context.Background(), &pluginv1.ProjectSlug{Slug: slug})
		if err != nil || answer.GetOffered() != offered {
			t.Errorf("%s: %v, %v", slug, answer, err)
		}
	}
}

func TestAModuleRunsUntilItIsCancelled(t *testing.T) {
	connection, _ := connect(t, &fakeHost{})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := connection.Plugin.RunModule(ctx, &pluginv1.ModuleState{})
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { _, err := stream.Recv(); ended <- err }()
	select {
	case err := <-ended:
		t.Fatalf("the module ended on its own: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the module did not stop")
	}
}
