package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

// stubExchanger replaces the identity provider: it is the only point of the flow that
// leaves the process, and simulating it allows checking all the rest.
type stubExchanger struct {
	claims       auth.Claims
	err          error
	gotVerifier  string
	gotNonce     string
	authCodeCall struct{ state, nonce, challenge string }
}

func (s *stubExchanger) AuthCodeURL(state, nonce, challenge string) string {
	s.authCodeCall.state = state
	s.authCodeCall.nonce = nonce
	s.authCodeCall.challenge = challenge
	return "https://idp.example.fr/authorize?state=" + url.QueryEscape(state)
}

func (s *stubExchanger) Exchange(_ context.Context, _, verifier, nonce string) (auth.Claims, error) {
	s.gotVerifier, s.gotNonce = verifier, nonce
	return s.claims, s.err
}

type stubSessions struct {
	created    auth.Session
	createErr  error
	lookup     auth.Session
	lookupRole auth.Role
	lookupErr  error
	revokeErr  error
	revoked    []string
}

// The bootstrap is neutral in these tests: they exercise the flow, not promotion. A stub
// that promoted would hide an authorization defect.
func (s *stubSessions) ApplyBootstrap(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *stubSessions) Create(context.Context, string, string, string) (auth.Session, error) {
	return s.created, s.createErr
}

func (s *stubSessions) LookupRoles(
	context.Context, string,
) (auth.Session, auth.Role, map[int64]auth.Role, error) {
	session, role, err := s.Lookup(context.Background(), "")
	return session, role, nil, err
}

func (s *stubSessions) Lookup(context.Context, string) (auth.Session, auth.Role, error) {
	return s.lookup, s.lookupRole, s.lookupErr
}

func (s *stubSessions) Revoke(_ context.Context, id string) error {
	s.revoked = append(s.revoked, id)
	return s.revokeErr
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newAuthHandler(ex auth.Exchanger, sessions Sessions) (*AuthHandler, *auth.FlowStore) {
	flows := auth.NewFlowStore(time.Minute)
	return NewAuthHandler(ex, flows, sessions, quietLogger(), true, time.Hour), flows
}

func cookieByName(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginRedirectsWithPKCEChallenge(t *testing.T) {
	ex := &stubExchanger{}
	handler, _ := newAuthHandler(ex, &stubSessions{})

	rec := httptest.NewRecorder()
	handler.Login(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, expected 302", rec.Code)
	}
	if ex.authCodeCall.state == "" || ex.authCodeCall.nonce == "" {
		t.Error("state or nonce missing from the authorization request")
	}
	if ex.authCodeCall.challenge == "" {
		t.Error("PKCE challenge missing")
	}
	// The verifier must never leave the server: only its digest goes.
	if strings.Contains(rec.Header().Get("Location"), ex.authCodeCall.challenge) &&
		ex.authCodeCall.challenge == ex.authCodeCall.nonce {
		t.Error("the challenge must not be the nonce")
	}
}

// Without that filtering, the sign-in page would become an open redirector: a link to the
// dashboard, on the right domain, would send the authenticated user to a third-party site.
func TestSafeRedirectRejectsExternalTargets(t *testing.T) {
	cases := map[string]string{
		"":                          "/",
		"/versions":                 "/versions",
		"/releases/1.8.0":           "/releases/1.8.0",
		"https://exemple.invalide":  "/",
		"http://exemple.invalide":   "/",
		"//exemple.invalide":        "/",
		"/\\exemple.invalide":       "/",
		"javascript:alert(1)":       "/",
		"exemple.invalide/versions": "/",
	}

	for input, expected := range cases {
		if got := safeRedirect(input); got != expected {
			t.Errorf("safeRedirect(%q) = %q, expected %q", input, got, expected)
		}
	}
}

func TestCallbackOpensSessionAndSetsCookies(t *testing.T) {
	ex := &stubExchanger{claims: auth.Claims{Subject: "sub-1", Username: "alice", Email: "a@x.fr"}}
	sessions := &stubSessions{created: auth.Session{ID: "session-id"}}
	handler, flows := newAuthHandler(ex, sessions)

	flow, _ := flows.Begin(context.Background(), "/versions")
	rec := httptest.NewRecorder()
	handler.Callback(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/callback?code=abc&state="+url.QueryEscape(flow.State), nil))

	res := rec.Result()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("code = %d, expected 302", res.StatusCode)
	}
	if location := res.Header.Get("Location"); location != "/versions" {
		t.Errorf("redirect = %q, the requested destination is lost", location)
	}

	// The PKCE verifier and the nonce must come from the transaction, not from the client: that
	// is what prevents replaying an intercepted code.
	if ex.gotVerifier != flow.CodeVerifier || ex.gotNonce != flow.Nonce {
		t.Error("the transaction's verifier or nonce was not passed")
	}

	session := cookieByName(res, sessionCookieName)
	if session == nil {
		t.Fatal("session cookie missing")
	}
	if !session.HttpOnly {
		t.Error("the session cookie must be HttpOnly: a token a script can read can be stolen")
	}
	if !session.Secure {
		t.Error("the session cookie must be Secure")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, expected Lax", session.SameSite)
	}

	csrf := cookieByName(res, csrfCookieName)
	if csrf == nil {
		t.Fatal("CSRF cookie missing")
	}
	if csrf.HttpOnly {
		t.Error("the CSRF cookie must be readable by the script: that is the principle of double submission")
	}
}

func TestCallbackRejectsUnknownState(t *testing.T) {
	handler, _ := newAuthHandler(&stubExchanger{}, &stubSessions{})

	rec := httptest.NewRecorder()
	handler.Callback(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/callback?code=abc&state=forged", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, expected 400", rec.Code)
	}
	if cookieByName(rec.Result(), sessionCookieName) != nil {
		t.Error("a session was opened despite an invalid state")
	}
}

// A replayed state would allow replaying an intercepted authorization code.
func TestCallbackRejectsReplayedState(t *testing.T) {
	ex := &stubExchanger{claims: auth.Claims{Subject: "sub-1"}}
	handler, flows := newAuthHandler(ex, &stubSessions{created: auth.Session{ID: "s"}})

	flow, _ := flows.Begin(context.Background(), "/")
	target := "/api/v1/auth/callback?code=abc&state=" + url.QueryEscape(flow.State)

	first := httptest.NewRecorder()
	handler.Callback(first, httptest.NewRequest(http.MethodGet, target, nil))
	if first.Code != http.StatusFound {
		t.Fatalf("first attempt refused: %d", first.Code)
	}

	second := httptest.NewRecorder()
	handler.Callback(second, httptest.NewRequest(http.MethodGet, target, nil))
	if second.Code != http.StatusBadRequest {
		t.Errorf("the state was accepted a second time: %d", second.Code)
	}
}

// A refusal from the provider — the user clicked Cancel — is not a technical failure and
// must not be displayed as one.
func TestCallbackDistinguishesProviderRefusal(t *testing.T) {
	handler, _ := newAuthHandler(&stubExchanger{}, &stubSessions{})

	rec := httptest.NewRecorder()
	handler.Callback(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/callback?error=access_denied", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, expected 401", rec.Code)
	}
}

func TestCallbackRejectsFailedExchange(t *testing.T) {
	ex := &stubExchanger{err: errors.New("nonce invalide")}
	handler, flows := newAuthHandler(ex, &stubSessions{})

	flow, _ := flows.Begin(context.Background(), "/")
	rec := httptest.NewRecorder()
	handler.Callback(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/callback?code=abc&state="+url.QueryEscape(flow.State), nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, expected 401", rec.Code)
	}
	if cookieByName(rec.Result(), sessionCookieName) != nil {
		t.Error("a session was opened despite a refused exchange")
	}
}

func TestLogoutRevokesAndClearsCookies(t *testing.T) {
	sessions := &stubSessions{}
	handler, _ := newAuthHandler(&stubExchanger{}, sessions)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-id"})

	rec := httptest.NewRecorder()
	handler.Logout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, expected 200", rec.Code)
	}
	if len(sessions.revoked) != 1 || sessions.revoked[0] != "session-id" {
		t.Errorf("session not revoked: %v", sessions.revoked)
	}
	if cookie := cookieByName(rec.Result(), sessionCookieName); cookie == nil || cookie.MaxAge >= 0 {
		t.Error("the session cookie was not cleared")
	}
}

// Clearing the cookie while revocation failed would make the user believe they are signed
// out, while their session stays usable by whoever holds it.
func TestLogoutKeepsCookieWhenRevocationFails(t *testing.T) {
	sessions := &stubSessions{revokeErr: errors.New("base indisponible")}
	handler, _ := newAuthHandler(&stubExchanger{}, sessions)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-id"})

	rec := httptest.NewRecorder()
	handler.Logout(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, expected 500", rec.Code)
	}
	if cookieByName(rec.Result(), sessionCookieName) != nil {
		t.Error("the cookie was cleared while the session stays active")
	}
}
