package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// memStore keeps the server's records in memory, as the database does.
type memStore struct {
	mu       sync.Mutex
	clients  map[string]store.OAuthClient
	requests map[string]store.OAuthRequest
	grants   map[int64]*memGrant
	codes    map[string]store.OAuthCode
	nextID   int64
	audits   []string
}

type memGrant struct {
	grant    store.OAuthGrant
	access   []byte
	accessAt time.Time
	refresh  []byte
	refreshT time.Time
	previous []byte
}

func newMemStore() *memStore {
	return &memStore{
		clients: map[string]store.OAuthClient{}, requests: map[string]store.OAuthRequest{},
		grants: map[int64]*memGrant{}, codes: map[string]store.OAuthCode{},
	}
}

func (m *memStore) CreateOAuthClient(_ context.Context, c store.OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[c.ID] = c
	return nil
}

func (m *memStore) OAuthClient(_ context.Context, id string) (store.OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	if !ok {
		return c, store.ErrOAuthNotFound
	}
	return c, nil
}

func (m *memStore) CreateOAuthRequest(_ context.Context, r store.OAuthRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ClientName = m.clients[r.ClientID].Name
	m.requests[r.ID] = r
	return nil
}

func (m *memStore) OAuthRequest(_ context.Context, id string, now time.Time) (store.OAuthRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok || !r.ExpiresAt.After(now) {
		return r, store.ErrOAuthNotFound
	}
	return r, nil
}

func (m *memStore) TakeOAuthRequest(ctx context.Context, id string, now time.Time) (store.OAuthRequest, error) {
	r, err := m.OAuthRequest(ctx, id, now)
	m.mu.Lock()
	delete(m.requests, id)
	m.mu.Unlock()
	return r, err
}

func (m *memStore) CreateOAuthGrant(_ context.Context, g store.OAuthGrant, c store.OAuthCode) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	g.ID = m.nextID
	g.ClientName = m.clients[g.ClientID].Name
	m.grants[g.ID] = &memGrant{grant: g}
	c.GrantID = g.ID
	m.codes[string(c.Hash)] = c
	return g.ID, nil
}

func (m *memStore) TakeOAuthCode(_ context.Context, hash []byte, now time.Time) (store.OAuthCode, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.codes[string(hash)]
	delete(m.codes, string(hash))
	if !ok || !c.ExpiresAt.After(now) {
		return c, "", store.ErrOAuthNotFound
	}
	return c, m.grants[c.GrantID].grant.ClientID, nil
}

func (m *memStore) IssueOAuthTokens(_ context.Context, id int64, t store.OAuthTokens) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return store.ErrOAuthNotFound
	}
	g.access, g.accessAt, g.refresh, g.refreshT = t.Access, t.AccessExpires, t.Refresh, t.RefreshExpires
	return nil
}

func (m *memStore) RefreshOAuthGrant(
	_ context.Context, clientID string, previous []byte, t store.OAuthTokens, now time.Time,
) (store.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, g := range m.grants {
		if g.grant.ClientID == clientID && bytes.Equal(g.refresh, previous) && g.refreshT.After(now) {
			g.previous, g.refresh, g.refreshT, g.access, g.accessAt = g.refresh, t.Refresh, t.RefreshExpires,
				t.Access, t.AccessExpires
			return m.grants[id].grant, nil
		}
	}
	for id, g := range m.grants {
		if bytes.Equal(g.previous, previous) {
			delete(m.grants, id)
			return store.OAuthGrant{}, store.ErrOAuthReplay
		}
	}
	return store.OAuthGrant{}, store.ErrOAuthNotFound
}

func (m *memStore) OAuthGrantByAccess(_ context.Context, access []byte, now time.Time) (store.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.grants {
		if g.access != nil && bytes.Equal(g.access, access) && g.accessAt.After(now) {
			grant := g.grant
			expires := g.accessAt
			grant.AccessExpires = &expires
			return grant, nil
		}
	}
	return store.OAuthGrant{}, store.ErrOAuthNotFound
}

func (m *memStore) DeleteOAuthGrant(_ context.Context, id int64, subject string) (store.OAuthGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok || (subject != "" && g.grant.Subject != subject) {
		return store.OAuthGrant{}, store.ErrOAuthNotFound
	}
	delete(m.grants, id)
	return g.grant, nil
}

func (m *memStore) RevokeOAuthToken(_ context.Context, clientID string, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, g := range m.grants {
		if g.grant.ClientID == clientID && (bytes.Equal(g.access, hash) || bytes.Equal(g.refresh, hash)) {
			delete(m.grants, id)
		}
	}
	return nil
}

func (m *memStore) Audit(_ context.Context, actor, action, target string, _ map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, actor+" "+action+" "+target)
	return nil
}

// roles grants each subject the role the test gives it.
type roles map[string]auth.Role

func (r roles) ResolveRoles(_ context.Context, subject string, _ []string) (auth.Role, map[int64]auth.Role, error) {
	role, ok := r[subject]
	if !ok {
		return "", nil, auth.ErrNoRole
	}
	return role, nil, nil
}

const publicURL = "https://dashboard.example"

type fixture struct {
	t      *testing.T
	server *Server
	store  *memStore
	roles  roles
	http   *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	st := newMemStore()
	rs := roles{"alice": auth.RoleOperator}
	server := New(st, rs, publicURL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	server.Routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &fixture{t: t, server: server, store: st, roles: rs, http: ts}
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func (f *fixture) register(redirects ...string) (int, map[string]any) {
	body, _ := json.Marshal(map[string]any{"client_name": "Claude Code", "redirect_uris": redirects})
	resp, err := http.Post(f.http.URL+"/oauth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	return resp.StatusCode, answer
}

func (f *fixture) authorize(query url.Values) *http.Response {
	resp, err := noRedirect.Get(f.http.URL + "/oauth/authorize?" + query.Encode())
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (f *fixture) tokenCall(form url.Values) (int, map[string]any) {
	resp, err := http.PostForm(f.http.URL+"/oauth/token", form)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	return resp.StatusCode, answer
}

const verifier = "a-code-verifier-long-enough-to-be-unguessable-0123456789"

// connect runs the flow up to the consent and returns the client and the code.
func (f *fixture) connect(scope string, kept []string) (clientID, code string) {
	f.t.Helper()
	_, client := f.register("http://127.0.0.1/callback")
	clientID = client["client_id"].(string)

	resp := f.authorize(url.Values{
		"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:43123/callback"},
		"response_type": {"code"}, "code_challenge": {auth.CodeChallenge(verifier)},
		"code_challenge_method": {"S256"}, "state": {"xyz"}, "scope": {scope},
	})
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(location, "/connect/") {
		f.t.Fatalf("authorize: %d %s", resp.StatusCode, location)
	}
	id := strings.TrimPrefix(location, "/connect/")

	pending, err := f.server.Pending(context.Background(), id)
	if err != nil || pending.Client != "Claude Code" || pending.Host != "127.0.0.1:43123" {
		f.t.Fatalf("pending %+v, %v", pending, err)
	}
	back, err := f.server.Approve(context.Background(), id,
		auth.Identity{Subject: "alice", Username: "alice", Groups: []string{"dev"}}, kept)
	if err != nil {
		f.t.Fatal(err)
	}
	redirect, _ := url.Parse(back)
	if redirect.Host != "127.0.0.1:43123" || redirect.Query().Get("state") != "xyz" {
		f.t.Fatalf("redirect %s", back)
	}
	return clientID, redirect.Query().Get("code")
}

func exchange(clientID, code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {"http://127.0.0.1:43123/callback"}, "code_verifier": {verifier},
	}
}

// The whole flow: registration, request, consent, code, tokens, a verified agent with its
// person's roles, a refresh that rotates the tokens.
func TestAnAgentIsAuthorisedThenRefreshed(t *testing.T) {
	f := newFixture(t)
	clientID, code := f.connect(ScopeRead+" "+ScopeRun, []string{ScopeRead, ScopeRun})

	status, tokens := f.tokenCall(exchange(clientID, code))
	if status != http.StatusOK || tokens["scope"] != ScopeRead+" "+ScopeRun {
		t.Fatalf("token: %d %v", status, tokens)
	}
	access := tokens["access_token"].(string)
	agent, err := f.server.Verify(context.Background(), access)
	if err != nil || agent.Identity.Role != auth.RoleOperator || agent.Name != "Claude Code" ||
		!agent.Can(ScopeRun) || !slices.Equal(agent.Identity.Groups, []string{"dev"}) {
		t.Fatalf("agent %+v, %v", agent, err)
	}
	if !slices.Contains(f.store.audits, "alice agent.authorize Claude Code") {
		t.Errorf("audit %v", f.store.audits)
	}

	// The code serves once.
	if status, answer := f.tokenCall(exchange(clientID, code)); status != http.StatusBadRequest ||
		answer["error"] != "invalid_grant" {
		t.Errorf("code replayed: %d %v", status, answer)
	}

	refresh := tokens["refresh_token"].(string)
	status, renewed := f.tokenCall(url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh},
	})
	if status != http.StatusOK || renewed["refresh_token"] == refresh {
		t.Fatalf("refresh: %d %v", status, renewed)
	}
	if _, err := f.server.Verify(context.Background(), access); err == nil {
		t.Error("the replaced access token still works")
	}
	if _, err := f.server.Verify(context.Background(), renewed["access_token"].(string)); err != nil {
		t.Errorf("the new access token: %v", err)
	}

	// The replaced refresh token comes back: someone else holds it, the grant is withdrawn.
	if status, _ := f.tokenCall(url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh},
	}); status != http.StatusBadRequest {
		t.Errorf("replayed refresh: %d", status)
	}
	if _, err := f.server.Verify(context.Background(), renewed["access_token"].(string)); err == nil {
		t.Error("the grant survived a replayed refresh token")
	}
}

// The person keeps reading only; a role withdrawn stops the agent at once.
func TestAnAgentHasWhatThePersonKeptAndHolds(t *testing.T) {
	f := newFixture(t)
	clientID, code := f.connect("", []string{ScopeRead})
	_, tokens := f.tokenCall(exchange(clientID, code))
	access := tokens["access_token"].(string)

	agent, err := f.server.Verify(context.Background(), access)
	if err != nil || agent.Can(ScopeRun) {
		t.Fatalf("agent %+v, %v", agent, err)
	}
	delete(f.roles, "alice")
	if _, err := f.server.Verify(context.Background(), access); err != ErrInvalidToken {
		t.Errorf("a person with no role: %v", err)
	}
}

func TestAWrongExchangeIsRefusedAndSpendsTheGrant(t *testing.T) {
	cases := map[string]func(url.Values){
		"wrong verifier": func(v url.Values) { v.Set("code_verifier", "another-verifier-0123456789-0123456789-01234") },
		"no verifier":    func(v url.Values) { v.Del("code_verifier") },
		"other redirect": func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1:9/callback") },
		"other client":   func(v url.Values) { v.Set("client_id", "someone-else") },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			clientID, code := f.connect("", []string{ScopeRead})
			form := exchange(clientID, code)
			alter(form)
			if status, answer := f.tokenCall(form); status != http.StatusBadRequest || answer["error"] != "invalid_grant" {
				t.Fatalf("%d %v", status, answer)
			}
			if len(f.store.grants) != 0 {
				t.Errorf("the grant survived a failed exchange")
			}
		})
	}
}

func TestRegistrationAndAuthorisationRefuseWhatCannotBeTrusted(t *testing.T) {
	f := newFixture(t)
	for _, uri := range []string{"http://evil.example/cb", "javascript:alert(1)", "https://x.example/cb#frag"} {
		if status, _ := f.register(uri); status != http.StatusBadRequest {
			t.Errorf("%s registered: %d", uri, status)
		}
	}
	for _, uri := range []string{"data:text/html,x", "file:///etc/passwd", "ws://127.0.0.1/cb"} {
		if status, _ := f.register(uri); status != http.StatusBadRequest {
			t.Errorf("%s registered: %d", uri, status)
		}
	}
	for _, uri := range []string{
		"https://claude.ai/api/mcp/auth_callback",
		"cursor://anysphere.cursor-mcp/oauth/callback",
		"com.example.app:/oauth2redirect",
	} {
		if status, _ := f.register(uri); status != http.StatusCreated {
			t.Errorf("%s refused: %d", uri, status)
		}
	}
	if host := redirectHost("cursor://anysphere.cursor-mcp/oauth/callback"); host != "cursor://anysphere.cursor-mcp" {
		t.Errorf("a native scheme shown as %q", host)
	}

	_, client := f.register("http://localhost/callback")
	clientID := client["client_id"].(string)
	base := url.Values{
		"client_id": {clientID}, "redirect_uri": {"http://localhost:5000/callback"},
		"response_type": {"code"}, "code_challenge": {auth.CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}

	// Not the client's redirect URI: answered here, never redirected to.
	foreign := url.Values{}
	for k, v := range base {
		foreign[k] = v
	}
	foreign.Set("redirect_uri", "https://evil.example/cb")
	if resp := f.authorize(foreign); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("foreign redirect: %d", resp.StatusCode)
	}

	for name, alter := range map[string]func(url.Values){
		"plain PKCE":     func(v url.Values) { v.Set("code_challenge_method", "plain") },
		"no PKCE":        func(v url.Values) { v.Del("code_challenge") },
		"other resource": func(v url.Values) { v.Set("resource", "https://other.example/mcp") },
		"implicit flow":  func(v url.Values) { v.Set("response_type", "token") },
	} {
		query := url.Values{}
		for k, v := range base {
			query[k] = slices.Clone(v)
		}
		alter(query)
		resp := f.authorize(query)
		location, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || location.Host != "localhost:5000" || location.Query().Get("error") == "" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, location)
		}
	}
}

// What a client discovers: where to authorise, for which resource.
func TestTheMetadataNameTheResourceAndTheEndpoints(t *testing.T) {
	f := newFixture(t)
	var resource, server map[string]any
	for path, into := range map[string]*map[string]any{
		"/.well-known/oauth-protected-resource/mcp": &resource,
		"/.well-known/oauth-authorization-server":   &server,
	} {
		resp, err := http.Get(f.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(resp.Body).Decode(into)
		resp.Body.Close()
	}
	if resource["resource"] != publicURL+"/mcp" || resource["authorization_servers"].([]any)[0] != publicURL {
		t.Errorf("resource %v", resource)
	}
	if server["token_endpoint"] != publicURL+"/oauth/token" ||
		!slices.Contains(server["code_challenge_methods_supported"].([]any), any("S256")) {
		t.Errorf("server %v", server)
	}
}

// An instance served over http and https answers each in its own scheme, on PUBLIC_URL's host
// only: a client checks that the resource is the URL it called.
func TestTheIssuerFollowsTheSchemeOnThePublicHost(t *testing.T) {
	server := New(newMemStore(), roles{}, "http://dashboard.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := func(host, proto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		return r
	}
	for _, c := range []struct{ host, proto, want string }{
		{"dashboard.example", "https", "https://dashboard.example/mcp"},
		{"dashboard.example", "", "http://dashboard.example/mcp"},
		{"DASHBOARD.example", "http", "http://dashboard.example/mcp"},
		{"elsewhere.example", "https", "http://dashboard.example/mcp"},
	} {
		if got := server.Resource(request(c.host, c.proto)); got != c.want {
			t.Errorf("%s over %q: %s, expected %s", c.host, c.proto, got, c.want)
		}
	}
}
