// Package agent is the MCP server through which an AI agent reads and drives the dashboard, at
// `/mcp`, authorised by the OAuth server of internal/oauth.
//
// A tool is a route of the API called in memory, with the agent's identity in the request's
// context: the permissions, the checks of the handlers and the journal are those of the
// interface, and nothing is decided twice. The tools are the boundary of what an agent can do:
// its token opens `/mcp`, never `/api/v1`.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	dashhttp "github.com/Cadoles/arkhitekton/internal/http"
	"github.com/Cadoles/arkhitekton/internal/oauth"
)

// Verifier resolves a bearer token into the agent holding it.
type Verifier interface {
	Verify(ctx context.Context, token string) (oauth.Agent, error)
	ResourceMetadataURL(r *http.Request) string
}

const instructions = `Arkhitekton drives the continuous integration and the releases of software projects: ` +
	`pipelines run on Tekton, releases are built, verified and approved. Every tool acts as the ` +
	`person who authorised you, with their roles: a refusal (403) means their role does not allow it. ` +
	`Start with whoami to learn the projects (by slug) and the roles. get_queue lists the pipeline ` +
	`kinds with their inputs, which launch_pipeline, run_release_pipeline and ` +
	`launch_pull_request_pipeline expect.`

// Handler serves the MCP server for the API router api.
func Handler(api http.Handler, verifier Verifier, version string, logger *slog.Logger) http.Handler {
	read := newServer(api, false, version, logger)
	full := newServer(api, true, version, logger)

	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if agent, ok := agentFrom(mcpauth.TokenInfoFromContext(r.Context())); ok && agent.Can(oauth.ScopeRun) {
			return full
		}
		return read
	}, &mcp.StreamableHTTPOptions{
		// The API runs in several replicas: no session is kept between two requests.
		Stateless:    true,
		JSONResponse: true,
		Logger:       logger,
	})

	verify := func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		agent, err := verifier.Verify(ctx, token)
		if errors.Is(err, oauth.ErrInvalidToken) {
			return nil, fmt.Errorf("%w: %v", mcpauth.ErrInvalidToken, err)
		}
		if err != nil {
			logger.Error("verifying an agent's token", "error", err)
			return nil, err
		}
		return &mcpauth.TokenInfo{
			Scopes: agent.Scopes, Expiration: agent.Expires, UserID: agent.Identity.Subject,
			Extra: map[string]any{agentExtra: agent},
		}, nil
	}
	// The metadata's address follows the scheme the request arrived on, as the metadata do.
	bearer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpauth.RequireBearerToken(verify, &mcpauth.RequireBearerTokenOptions{
			ResourceMetadataURL: verifier.ResourceMetadataURL(r),
			Scopes:              []string{oauth.ScopeRead},
		})(streamable).ServeHTTP(w, r)
	})
	return cors(bearer)
}

// cors opens the server to a client running in a browser: it reads a bearer token, never a cookie.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Authorization, Content-Type, Accept, Mcp-Protocol-Version, Mcp-Session-Id, Last-Event-ID")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const agentExtra = "agent"

func agentFrom(info *mcpauth.TokenInfo) (oauth.Agent, bool) {
	if info == nil {
		return oauth.Agent{}, false
	}
	agent, ok := info.Extra[agentExtra].(oauth.Agent)
	return agent, ok
}

func newServer(api http.Handler, run bool, version string, logger *slog.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "arkhitekton", Title: "Arkhitekton", Version: version},
		&mcp.ServerOptions{Instructions: instructions, Logger: logger})
	caller := &caller{api: api}
	addReadTools(server, caller)
	if run {
		addRunTools(server, caller)
	}
	return server
}

// request is a call of the API: a method, a path under `/api/v1`, a query and a JSON body.
type request struct {
	method string
	path   string
	query  url.Values
	body   any
}

func get(path string, query url.Values) request {
	return request{method: http.MethodGet, path: path, query: query}
}

func post(path string, body any) request {
	return request{method: http.MethodPost, path: path, body: body}
}

// p builds a path from segments, each escaped.
func p(segments ...string) string {
	var b strings.Builder
	for _, segment := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(segment))
	}
	return b.String()
}

// maxAnswer bounds what a tool returns: an agent's context is not a log archive.
const maxAnswer = 256 << 10

type caller struct {
	api http.Handler
}

// call runs a request on the API as the agent of the tool call, and turns the answer into the
// tool's result.
func (c *caller) call(ctx context.Context, call *mcp.CallToolRequest, r request) (*mcp.CallToolResult, any, error) {
	agent, ok := agentFrom(call.Extra.TokenInfo)
	if !ok {
		return nil, nil, errors.New("no agent behind this call")
	}

	var body io.Reader = http.NoBody
	if r.body != nil {
		encoded, err := json.Marshal(r.body)
		if err != nil {
			return nil, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	target := r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	req, err := http.NewRequestWithContext(dashhttp.WithAgent(ctx, agent.Identity, agent.Name),
		r.method, "http://agent"+target, body)
	if err != nil {
		return nil, nil, err
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	recorder := newRecorder()
	c.api.ServeHTTP(recorder, req)
	return result(recorder), nil, nil
}

func result(recorder *recorder) *mcp.CallToolResult {
	text := recorder.body.String()
	if recorder.status >= 400 {
		var problem struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		message := fmt.Sprintf("%d %s", recorder.status, http.StatusText(recorder.status))
		if json.Unmarshal(recorder.body.Bytes(), &problem) == nil && problem.Title != "" {
			message = fmt.Sprintf("%d %s", recorder.status, problem.Title)
			if problem.Detail != "" {
				message += ": " + problem.Detail
			}
		}
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
	}
	if strings.TrimSpace(text) == "" {
		text = fmt.Sprintf("%d %s", recorder.status, http.StatusText(recorder.status))
	}
	if recorder.truncated {
		text += "\n[answer truncated: narrow the request]"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// recorder keeps a route's answer, up to maxAnswer.
type recorder struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	truncated bool
}

func newRecorder() *recorder { return &recorder{header: http.Header{}, status: http.StatusOK} }

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) { r.status = status }

func (r *recorder) Write(data []byte) (int, error) {
	room := maxAnswer - r.body.Len()
	if room <= 0 {
		r.truncated = true
		return len(data), nil
	}
	if len(data) > room {
		r.truncated = true
		r.body.Write(data[:room])
		return len(data), nil
	}
	return r.body.Write(data)
}
