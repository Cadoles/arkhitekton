// Package oauth is the authorisation server that lets an AI agent act on a person's behalf through
// the MCP server (`/mcp`), as the MCP authorisation specification describes it: OAuth 2.1 with
// PKCE, dynamic client registration (RFC 7591), authorisation server metadata (RFC 8414),
// protected resource metadata (RFC 9728) and token revocation (RFC 7009).
//
// It authenticates no one itself: the person signs in to the dashboard as usual, then consents on
// the dashboard's own screen. See docs/09-security.md.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Cadoles/arkhitekton/internal/auth"
	"github.com/Cadoles/arkhitekton/internal/store"
)

// Scopes an agent may be granted.
const (
	// ScopeRead reads what a role reads.
	ScopeRead = "dashboard:read"
	// ScopeRun launches and cancels pipelines and creates, builds, launches and approves releases,
	// as far as the role allows.
	ScopeRun = "dashboard:run"
)

// Scopes lists the scopes, in the order they are offered.
var Scopes = []string{ScopeRead, ScopeRun}

// Lifetimes.
const (
	requestTTL = 10 * time.Minute
	codeTTL    = 10 * time.Minute
	accessTTL  = time.Hour
	// refreshTTL runs from the last refresh: an agent in use stays connected, one left aside for a
	// month must be authorised again.
	refreshTTL = 30 * 24 * time.Hour
)

// Token prefixes, so that a token pasted where it should not be is recognisable.
const (
	accessPrefix  = "ark_at_"
	refreshPrefix = "ark_rt_"
)

// Store is what the server keeps.
type Store interface {
	CreateOAuthClient(ctx context.Context, client store.OAuthClient) error
	OAuthClient(ctx context.Context, id string) (store.OAuthClient, error)
	CreateOAuthRequest(ctx context.Context, request store.OAuthRequest) error
	OAuthRequest(ctx context.Context, id string, now time.Time) (store.OAuthRequest, error)
	TakeOAuthRequest(ctx context.Context, id string, now time.Time) (store.OAuthRequest, error)
	CreateOAuthGrant(ctx context.Context, grant store.OAuthGrant, code store.OAuthCode) (int64, error)
	TakeOAuthCode(ctx context.Context, hash []byte, now time.Time) (store.OAuthCode, string, error)
	IssueOAuthTokens(ctx context.Context, grantID int64, tokens store.OAuthTokens) error
	RefreshOAuthGrant(ctx context.Context, clientID string, previous []byte, tokens store.OAuthTokens,
		now time.Time) (store.OAuthGrant, error)
	OAuthGrantByAccess(ctx context.Context, access []byte, now time.Time) (store.OAuthGrant, error)
	DeleteOAuthGrant(ctx context.Context, id int64, subject string) (store.OAuthGrant, error)
	RevokeOAuthToken(ctx context.Context, clientID string, hash []byte) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// Roles resolves a subject's roles at each call, so that a role withdrawn applies at once.
type Roles interface {
	ResolveRoles(ctx context.Context, subject string, groups []string) (auth.Role, map[int64]auth.Role, error)
}

// Server is the authorisation server.
type Server struct {
	store  Store
	roles  Roles
	logger *slog.Logger
	// public is the dashboard's public URL.
	public *url.URL
	now    func() time.Time
}

// New builds the server for a dashboard served at publicURL.
func New(st Store, roles Roles, publicURL string, logger *slog.Logger) *Server {
	public, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil {
		public = &url.URL{}
	}
	return &Server{store: st, roles: roles, logger: logger, public: public, now: time.Now}
}

// Issuer is the dashboard's address as a request reached it: the host of PUBLIC_URL, with the
// scheme the request arrived on. A client checks that the resource it is told about is the URL it
// called: an instance served over both http and https must answer each in its own scheme. Another
// host gets PUBLIC_URL as it is.
func (s *Server) Issuer(r *http.Request) string {
	origin := *s.public
	if r != nil && strings.EqualFold(r.Host, s.public.Host) {
		origin.Scheme = requestScheme(r, s.public.Scheme)
	}
	return origin.String()
}

// requestScheme is the scheme a request arrived on: TLS here, or the proxy's word for it.
func requestScheme(r *http.Request, fallback string) string {
	if r.TLS != nil {
		return "https"
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "https":
		return "https"
	case "http":
		return "http"
	}
	return fallback
}

// Resource is the MCP server's URL, the resource tokens are issued for.
func (s *Server) Resource(r *http.Request) string { return s.Issuer(r) + "/mcp" }

// ResourceMetadataURL is where the MCP server's protected resource metadata is served.
func (s *Server) ResourceMetadataURL(r *http.Request) string {
	return s.Issuer(r) + "/.well-known/oauth-protected-resource/mcp"
}

// Agent is who calls the MCP server: a person's identity, through a client they authorised.
type Agent struct {
	Identity auth.Identity
	// Name is the name the client gave itself.
	Name    string
	Scopes  []string
	Expires time.Time
}

// Can says whether the agent was granted a scope.
func (a Agent) Can(scope string) bool { return slices.Contains(a.Scopes, scope) }

// ErrInvalidToken covers an unknown, expired or revoked token, and a person left with no role.
var ErrInvalidToken = errors.New("invalid or expired token")

// Verify resolves an access token into the agent that holds it, with the roles its person holds
// now.
func (s *Server) Verify(ctx context.Context, token string) (Agent, error) {
	if !strings.HasPrefix(token, accessPrefix) {
		return Agent{}, ErrInvalidToken
	}
	grant, err := s.store.OAuthGrantByAccess(ctx, hash(token), s.now().UTC())
	if errors.Is(err, store.ErrOAuthNotFound) {
		return Agent{}, ErrInvalidToken
	}
	if err != nil {
		return Agent{}, err
	}
	role, projects, err := s.roles.ResolveRoles(ctx, grant.Subject, grant.Groups)
	if errors.Is(err, auth.ErrNoRole) {
		return Agent{}, ErrInvalidToken
	}
	if err != nil {
		return Agent{}, err
	}
	agent := Agent{
		Identity: auth.Identity{
			Subject: grant.Subject, Username: grant.Username, Email: grant.Email, Groups: grant.Groups,
			Role: role, Projects: projects,
		},
		Name:   grant.ClientName,
		Scopes: grant.Scopes,
	}
	if grant.AccessExpires != nil {
		agent.Expires = *grant.AccessExpires
	}
	return agent, nil
}

// Pending is an authorisation request as the consent screen shows it.
type Pending struct {
	ID     string   `json:"id"`
	Client string   `json:"client"`
	Host   string   `json:"redirect_host"`
	Scopes []string `json:"scopes"`
}

// ErrUnknownRequest covers an unknown, expired or already answered request.
var ErrUnknownRequest = errors.New("unknown or expired authorisation request")

// Pending reads a request waiting for consent.
func (s *Server) Pending(ctx context.Context, id string) (Pending, error) {
	request, err := s.store.OAuthRequest(ctx, id, s.now().UTC())
	if errors.Is(err, store.ErrOAuthNotFound) {
		return Pending{}, ErrUnknownRequest
	}
	if err != nil {
		return Pending{}, err
	}
	return Pending{
		ID: request.ID, Client: request.ClientName, Host: redirectHost(request.RedirectURI),
		Scopes: request.Scopes,
	}, nil
}

// Approve grants a request, with the scopes the person kept among those asked, and returns where
// to send the browser: back to the client, with the code.
func (s *Server) Approve(ctx context.Context, id string, identity auth.Identity, scopes []string) (string, error) {
	request, err := s.store.TakeOAuthRequest(ctx, id, s.now().UTC())
	if errors.Is(err, store.ErrOAuthNotFound) {
		return "", ErrUnknownRequest
	}
	if err != nil {
		return "", err
	}

	granted := []string{ScopeRead}
	if slices.Contains(scopes, ScopeRun) && slices.Contains(request.Scopes, ScopeRun) {
		granted = append(granted, ScopeRun)
	}

	code, err := token("")
	if err != nil {
		return "", err
	}
	if _, err := s.store.CreateOAuthGrant(ctx, store.OAuthGrant{
		ClientID: request.ClientID, Subject: identity.Subject, Username: identity.Username,
		Email: identity.Email, Groups: identity.Groups, Scopes: granted,
	}, store.OAuthCode{
		Hash: hash(code), RedirectURI: request.RedirectURI, CodeChallenge: request.CodeChallenge,
		ExpiresAt: s.now().UTC().Add(codeTTL),
	}); err != nil {
		return "", err
	}

	if err := s.store.Audit(ctx, identity.Subject, store.AuditAgentAuthorize, request.ClientName,
		map[string]any{"scopes": granted, "redirect_host": redirectHost(request.RedirectURI)}); err != nil {
		s.logger.Error("recording an agent's authorisation", "error", err)
	}
	return withQuery(request.RedirectURI, "code", code, "state", request.State), nil
}

// Deny refuses a request and returns where to send the browser: back to the client, told so.
func (s *Server) Deny(ctx context.Context, id string) (string, error) {
	request, err := s.store.TakeOAuthRequest(ctx, id, s.now().UTC())
	if errors.Is(err, store.ErrOAuthNotFound) {
		return "", ErrUnknownRequest
	}
	if err != nil {
		return "", err
	}
	return withQuery(request.RedirectURI, "error", "access_denied", "state", request.State), nil
}

// Revoke withdraws a grant: a subject's own, or anyone's when subject is empty (an administrator).
func (s *Server) Revoke(ctx context.Context, id int64, actor, subject string) error {
	grant, err := s.store.DeleteOAuthGrant(ctx, id, subject)
	if err != nil {
		return err
	}
	if err := s.store.Audit(ctx, actor, store.AuditAgentRevoke, grant.ClientName,
		map[string]any{"owner": grant.Subject}); err != nil {
		s.logger.Error("recording an agent's revocation", "error", err)
	}
	return nil
}

// token returns an unpredictable value with a prefix.
func token(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// hash is what is kept of a token or a code.
func hash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
