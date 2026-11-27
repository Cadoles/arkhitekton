package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Cadoles/arkhitekton/internal/auth"
	dashhttp "github.com/Cadoles/arkhitekton/internal/http"
	"github.com/Cadoles/arkhitekton/internal/oauth"
	"github.com/Cadoles/arkhitekton/internal/store"
)

// tokens maps a bearer token to its agent.
type tokens map[string]oauth.Agent

func (t tokens) Verify(_ context.Context, token string) (oauth.Agent, error) {
	agent, ok := t[token]
	if !ok {
		return oauth.Agent{}, oauth.ErrInvalidToken
	}
	agent.Expires = time.Now().Add(time.Hour)
	return agent, nil
}

func (tokens) ResourceMetadataURL(*http.Request) string {
	return "https://dashboard.example/.well-known/oauth-protected-resource/mcp"
}

// noSessions: an agent never goes through a session.
type noSessions struct{ dashhttp.Sessions }

// launched is what the fake queue received.
type launched struct {
	who, agent, kind string
}

// api is a slice of the API router: the same middleware, a queue that records what it launches.
func api(t *testing.T, got *[]launched) http.Handler {
	middleware := dashhttp.NewMiddleware(noSessions{}, auth.NewAuthorizer(nil),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	mux.Handle("GET /me", dashhttp.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := dashhttp.IdentityFrom(r.Context())
		dashhttp.WriteJSON(w, http.StatusOK, map[string]any{"username": identity.Username})
	}), middleware.Authenticate))
	mux.Handle("POST /queue", dashhttp.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := dashhttp.IdentityFrom(r.Context())
		agent, _ := store.AgentFrom(r.Context())
		var body struct {
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*got = append(*got, launched{identity.Username, agent, body.Kind})
		dashhttp.WriteJSON(w, http.StatusCreated, map[string]any{"id": 7})
	}), middleware.Authenticate, middleware.CSRF, middleware.Require(auth.PermPipelineRun)))
	return mux
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func text(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func TestAnAgentActsAsItsPersonWithinItsScopes(t *testing.T) {
	var got []launched
	agents := tokens{
		"operator": {Identity: auth.Identity{Subject: "a", Username: "alice", Role: auth.RoleOperator},
			Name: "Claude Code", Scopes: []string{oauth.ScopeRead, oauth.ScopeRun}},
		"viewer": {Identity: auth.Identity{Subject: "b", Username: "bob", Role: auth.RoleViewer},
			Name: "Claude Code", Scopes: []string{oauth.ScopeRead, oauth.ScopeRun}},
		"reader": {Identity: auth.Identity{Subject: "a", Username: "alice", Role: auth.RoleOperator},
			Name: "Claude Code", Scopes: []string{oauth.ScopeRead}},
	}
	server := httptest.NewServer(Handler(api(t, &got), agents, "test", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	ctx := context.Background()

	operator := connect(t, server.URL, "operator")
	if names := toolNames(t, operator); !slices.Contains(names, "launch_pipeline") || !slices.Contains(names, "whoami") {
		t.Fatalf("tools %v", names)
	}
	result, err := operator.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil || result.IsError || !strings.Contains(text(result), "alice") {
		t.Fatalf("whoami: %v %s", err, text(result))
	}
	result, err = operator.CallTool(ctx, &mcp.CallToolParams{
		Name: "launch_pipeline", Arguments: map[string]any{"kind": "docker/build"},
	})
	if err != nil || result.IsError || len(got) != 1 || got[0] != (launched{"alice", "Claude Code", "docker/build"}) {
		t.Fatalf("launch: %v %s, %+v", err, text(result), got)
	}

	// The tool is there, the role refuses: the interface's answer, word for word.
	viewer := connect(t, server.URL, "viewer")
	result, err = viewer.CallTool(ctx, &mcp.CallToolParams{
		Name: "launch_pipeline", Arguments: map[string]any{"kind": "docker/build"},
	})
	if err != nil || !result.IsError || !strings.Contains(text(result), "403 Insufficient permission") || len(got) != 1 {
		t.Fatalf("a viewer launching: %v %s", err, text(result))
	}

	// Reading only: the tools that act are not offered.
	reader := connect(t, server.URL, "reader")
	if names := toolNames(t, reader); slices.Contains(names, "launch_pipeline") || !slices.Contains(names, "get_queue") {
		t.Errorf("a reader's tools %v", names)
	}
}

// Without a token, the answer says where to obtain one.
func TestAnUnknownTokenIsSentToTheAuthorisationServer(t *testing.T) {
	var got []launched
	server := httptest.NewServer(Handler(api(t, &got), tokens{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	request, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer nope")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized ||
		!strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="https://dashboard.example/`) {
		t.Errorf("%d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}
