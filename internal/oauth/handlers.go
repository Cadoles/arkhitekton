package oauth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// Routes mounts the metadata and the endpoints, at the root: the `.well-known` paths are fixed.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.Handle("/.well-known/oauth-authorization-server", cors(http.HandlerFunc(s.serverMetadata)))
	mux.Handle("/.well-known/oauth-protected-resource", cors(http.HandlerFunc(s.resourceMetadata)))
	mux.Handle("/.well-known/oauth-protected-resource/mcp", cors(http.HandlerFunc(s.resourceMetadata)))
	mux.Handle("/oauth/register", cors(http.HandlerFunc(s.register)))
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.Handle("/oauth/token", cors(http.HandlerFunc(s.token)))
	mux.Handle("/oauth/revoke", cors(http.HandlerFunc(s.revoke)))
}

// cors opens an endpoint to a client running in a browser. Nothing here reads a cookie: a request
// from another origin obtains nothing it could not obtain from anywhere else.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serverMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	issuer := s.Issuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"registration_endpoint":                 issuer + "/oauth/register",
		"revocation_endpoint":                   issuer + "/oauth/revoke",
		"scopes_supported":                      Scopes,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (s *Server) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.Resource(r),
		"authorization_servers":    []string{s.Issuer(r)},
		"scopes_supported":         Scopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Arkhitekton",
	})
}

// Registration limits: a registration is open to anyone, it must stay small.
const (
	maxNameLength    = 100
	maxRedirectURIs  = 5
	maxRegistrations = 16 << 10
)

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		GrantTypes   []string `json:"grant_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegistrations)).Decode(&request); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unreadable registration")
		return
	}
	if request.AuthMethod != "" && request.AuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only public clients are accepted: token_endpoint_auth_method must be none")
		return
	}
	if len(request.RedirectURIs) == 0 || len(request.RedirectURIs) > maxRedirectURIs {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "one to five redirect URIs are expected")
		return
	}
	for _, uri := range request.RedirectURIs {
		if !acceptedRedirect(uri) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"a redirect URI is https, http on a loopback address, or a native application's scheme: "+uri)
			return
		}
	}
	name := strings.TrimSpace(request.ClientName)
	if name == "" {
		name = "Agent"
	}
	if len([]rune(name)) > maxNameLength {
		name = string([]rune(name)[:maxNameLength])
	}

	id, err := token("")
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	client := store.OAuthClient{ID: id, Name: name, RedirectURIs: request.RedirectURIs}
	if err := s.store.CreateOAuthClient(r.Context(), client); err != nil {
		s.logger.Error("registering an oauth client", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  id,
		"client_name":                name,
		"redirect_uris":              request.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"client_id_issued_at":        s.now().Unix(),
	})
}

// authorize checks the request, keeps it for the consent screen and sends the browser there. A
// client or a redirect URI it cannot trust is answered here: redirecting to an unchecked address
// would make the dashboard an open redirector.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	client, err := s.store.OAuthClient(r.Context(), query.Get("client_id"))
	if errors.Is(err, store.ErrOAuthNotFound) {
		http.Error(w, "Unknown client: register it again.", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Error("reading an oauth client", "error", err)
		http.Error(w, "Authorisation unavailable.", http.StatusInternalServerError)
		return
	}
	redirect, ok := registeredRedirect(client.RedirectURIs, query.Get("redirect_uri"))
	if !ok {
		http.Error(w, "This redirect URI is not the client's.", http.StatusBadRequest)
		return
	}

	state := query.Get("state")
	fail := func(code, description string) {
		http.Redirect(w, r, withQuery(redirect, "error", code, "error_description", description,
			"state", state), http.StatusFound)
	}
	if query.Get("response_type") != "code" {
		fail("unsupported_response_type", "only the code flow is supported")
		return
	}
	challenge := query.Get("code_challenge")
	if challenge == "" || query.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with S256 is required")
		return
	}
	if resource := query.Get("resource"); resource != "" && strings.TrimRight(resource, "/") != s.Resource(r) {
		fail("invalid_target", "the only resource is "+s.Resource(r))
		return
	}
	scopes := requestedScopes(query.Get("scope"))

	id, err := token("")
	if err != nil {
		fail("server_error", "")
		return
	}
	if err := s.store.CreateOAuthRequest(r.Context(), store.OAuthRequest{
		ID: id, ClientID: client.ID, RedirectURI: redirect, State: state, CodeChallenge: challenge,
		Scopes: scopes, ExpiresAt: s.now().UTC().Add(requestTTL),
	}); err != nil {
		s.logger.Error("recording an oauth request", "error", err)
		fail("server_error", "")
		return
	}
	http.Redirect(w, r, "/connect/"+id, http.StatusFound)
}

// requestedScopes keeps the scopes known among those asked; none asked is both.
func requestedScopes(raw string) []string {
	asked := strings.Fields(raw)
	if len(asked) == 0 {
		return slices.Clone(Scopes)
	}
	scopes := []string{ScopeRead}
	if slices.Contains(asked, ScopeRun) {
		scopes = append(scopes, ScopeRun)
	}
	return scopes
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "unreadable form")
		return
	}
	if resource := r.PostForm.Get("resource"); resource != "" && strings.TrimRight(resource, "/") != s.Resource(r) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "the only resource is "+s.Resource(r))
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	form := r.PostForm
	code, clientID, err := s.store.TakeOAuthCode(r.Context(), hash(form.Get("code")), s.now().UTC())
	if errors.Is(err, store.ErrOAuthNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or used code")
		return
	}
	if err != nil {
		s.logger.Error("reading an oauth code", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}

	// The code is spent whatever happens next: a grant whose code failed is withdrawn rather than
	// left waiting for tokens it will never get.
	refuse := func(description string) {
		if _, err := s.store.DeleteOAuthGrant(r.Context(), code.GrantID, ""); err != nil {
			s.logger.Error("withdrawing an oauth grant", "error", err)
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", description)
	}
	if !equal(form.Get("client_id"), clientID) {
		refuse("the code was issued to another client")
		return
	}
	if !equal(form.Get("redirect_uri"), code.RedirectURI) {
		refuse("the redirect URI differs from the authorisation request's")
		return
	}
	verifier := form.Get("code_verifier")
	if verifier == "" || !equal(auth.CodeChallenge(verifier), code.CodeChallenge) {
		refuse("PKCE verification failed")
		return
	}

	access, refresh, tokens, err := s.newTokens()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if err := s.store.IssueOAuthTokens(r.Context(), code.GrantID, tokens); err != nil {
		s.logger.Error("issuing oauth tokens", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	grant, err := s.store.OAuthGrantByAccess(r.Context(), tokens.Access, s.now().UTC())
	if err != nil {
		s.logger.Error("reading an oauth grant", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeTokens(w, access, refresh, grant.Scopes)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	form := r.PostForm
	previous := form.Get("refresh_token")
	if !strings.HasPrefix(previous, refreshPrefix) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
		return
	}
	access, refresh, tokens, err := s.newTokens()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	grant, err := s.store.RefreshOAuthGrant(r.Context(), form.Get("client_id"), hash(previous), tokens, s.now().UTC())
	switch {
	case errors.Is(err, store.ErrOAuthReplay):
		s.logger.Warn("a replaced refresh token was presented: the agent's grant is revoked",
			"client", form.Get("client_id"))
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token already used; authorise again")
		return
	case errors.Is(err, store.ErrOAuthNotFound):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired refresh token")
		return
	case err != nil:
		s.logger.Error("refreshing an oauth grant", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeTokens(w, access, refresh, grant.Scopes)
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "unreadable form")
		return
	}
	if err := s.store.RevokeOAuthToken(r.Context(), r.PostForm.Get("client_id"),
		hash(r.PostForm.Get("token"))); err != nil {
		s.logger.Error("revoking an oauth token", "error", err)
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) newTokens() (string, string, store.OAuthTokens, error) {
	access, err := token(accessPrefix)
	if err != nil {
		return "", "", store.OAuthTokens{}, err
	}
	refresh, err := token(refreshPrefix)
	if err != nil {
		return "", "", store.OAuthTokens{}, err
	}
	now := s.now().UTC()
	return access, refresh, store.OAuthTokens{
		Access: hash(access), AccessExpires: now.Add(accessTTL),
		Refresh: hash(refresh), RefreshExpires: now.Add(refreshTTL),
	}, nil
}

func writeTokens(w http.ResponseWriter, access, refresh string, scopes []string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         strings.Join(scopes, " "),
	})
}

// acceptedRedirect says whether a client may register a redirect URI: https anywhere, http on a
// loopback address, where a native client such as Claude Code listens, or a native application's
// own scheme (RFC 8252 §7.1), such as Cursor's `cursor://`. A scheme a browser would run or read
// itself is refused.
func acceptedRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		return u.Host != "" && loopback(u.Hostname())
	}
	return privateScheme(u.Scheme) && (u.Host != "" || u.Path != "")
}

// refusedSchemes are the schemes a browser handles itself: redirecting a code there would run it,
// show it, or send it in clear.
var refusedSchemes = []string{
	"javascript", "data", "vbscript", "file", "about", "blob", "filesystem", "ftp", "ws", "wss", "view-source",
}

func privateScheme(scheme string) bool {
	if scheme == "" || slices.Contains(refusedSchemes, strings.ToLower(scheme)) {
		return false
	}
	for _, r := range scheme {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// registeredRedirect returns the redirect URI to use: the one asked when the client registered it,
// any port on a registered loopback URI (RFC 8252 §7.3: a native client picks its port when it
// starts), or the only one registered when none is asked.
func registeredRedirect(registered []string, asked string) (string, bool) {
	if asked == "" {
		if len(registered) == 1 {
			return registered[0], true
		}
		return "", false
	}
	if slices.Contains(registered, asked) {
		return asked, true
	}
	u, err := url.Parse(asked)
	if err != nil || u.Scheme != "http" || !loopback(u.Hostname()) {
		return "", false
	}
	for _, candidate := range registered {
		c, err := url.Parse(candidate)
		if err == nil && c.Scheme == "http" && c.Hostname() == u.Hostname() && c.Path == u.Path &&
			c.RawQuery == u.RawQuery {
			return asked, true
		}
	}
	return "", false
}

// redirectHost is what the consent screen shows of where the browser will go: the host, or for a
// native application's scheme, the scheme too, which names the application.
func redirectHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return u.Scheme + "://" + u.Host
	}
	return u.Host
}

// withQuery adds parameters to a URL, those with an empty value left out.
func withQuery(raw string, pairs ...string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			query.Set(pairs[i], pairs[i+1])
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	payload := map[string]string{"error": code}
	if description != "" {
		payload["error_description"] = description
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
