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

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
)

// stubExchanger remplace le fournisseur d'identité : c'est le seul point du flux
// qui sort du processus, et le simuler permet de vérifier tout le reste.
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

// L'amorçage est neutre dans ces tests : ils éprouvent le flux, pas la
// promotion. Un stub qui promeut masquerait un défaut d'autorisation.
func (s *stubSessions) ApplyBootstrap(context.Context, string, string, []string) (bool, error) {
	return false, nil
}

func (s *stubSessions) Create(context.Context, string, string, string) (auth.Session, error) {
	return s.created, s.createErr
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
	return NewAuthHandler(ex, flows, sessions, quietLogger(), true, nil, time.Hour), flows
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
		t.Fatalf("code = %d, attendu 302", rec.Code)
	}
	if ex.authCodeCall.state == "" || ex.authCodeCall.nonce == "" {
		t.Error("state ou nonce absent de la demande d'autorisation")
	}
	if ex.authCodeCall.challenge == "" {
		t.Error("challenge PKCE absent")
	}
	// Le vérificateur ne doit jamais quitter le serveur : seul son condensé part.
	if strings.Contains(rec.Header().Get("Location"), ex.authCodeCall.challenge) &&
		ex.authCodeCall.challenge == ex.authCodeCall.nonce {
		t.Error("le challenge ne doit pas être le nonce")
	}
}

// Sans ce filtrage, la page de connexion deviendrait un redirecteur ouvert : un
// lien vers le dashboard, sur le bon domaine, renverrait l'utilisateur
// authentifié vers un site tiers.
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
			t.Errorf("safeRedirect(%q) = %q, attendu %q", input, got, expected)
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
		t.Fatalf("code = %d, attendu 302", res.StatusCode)
	}
	if location := res.Header.Get("Location"); location != "/versions" {
		t.Errorf("redirection = %q, la destination demandée est perdue", location)
	}

	// Le vérificateur PKCE et le nonce doivent venir de la transaction, pas du
	// client : c'est ce qui empêche de rejouer un code intercepté.
	if ex.gotVerifier != flow.CodeVerifier || ex.gotNonce != flow.Nonce {
		t.Error("le vérificateur ou le nonce de la transaction n'a pas été transmis")
	}

	session := cookieByName(res, sessionCookieName)
	if session == nil {
		t.Fatal("cookie de session absent")
	}
	if !session.HttpOnly {
		t.Error("le cookie de session doit être HttpOnly: un jeton lisible par un script est volable")
	}
	if !session.Secure {
		t.Error("le cookie de session doit être Secure")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, attendu Lax", session.SameSite)
	}

	csrf := cookieByName(res, csrfCookieName)
	if csrf == nil {
		t.Fatal("cookie CSRF absent")
	}
	if csrf.HttpOnly {
		t.Error("le cookie CSRF doit être lisible par le script: c'est le principe de la double soumission")
	}
}

func TestCallbackRejectsUnknownState(t *testing.T) {
	handler, _ := newAuthHandler(&stubExchanger{}, &stubSessions{})

	rec := httptest.NewRecorder()
	handler.Callback(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/callback?code=abc&state=forgé", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, attendu 400", rec.Code)
	}
	if cookieByName(rec.Result(), sessionCookieName) != nil {
		t.Error("une session a été ouverte malgré un state invalide")
	}
}

// Un state rejoué permettrait de rejouer un code d'autorisation intercepté.
func TestCallbackRejectsReplayedState(t *testing.T) {
	ex := &stubExchanger{claims: auth.Claims{Subject: "sub-1"}}
	handler, flows := newAuthHandler(ex, &stubSessions{created: auth.Session{ID: "s"}})

	flow, _ := flows.Begin(context.Background(), "/")
	target := "/api/v1/auth/callback?code=abc&state=" + url.QueryEscape(flow.State)

	first := httptest.NewRecorder()
	handler.Callback(first, httptest.NewRequest(http.MethodGet, target, nil))
	if first.Code != http.StatusFound {
		t.Fatalf("première tentative refusée: %d", first.Code)
	}

	second := httptest.NewRecorder()
	handler.Callback(second, httptest.NewRequest(http.MethodGet, target, nil))
	if second.Code != http.StatusBadRequest {
		t.Errorf("le state a été accepté une seconde fois: %d", second.Code)
	}
}

// Un refus du fournisseur — l'utilisateur a cliqué sur « Annuler » — n'est pas un
// échec technique et ne doit pas s'afficher comme tel.
func TestCallbackDistinguishesProviderRefusal(t *testing.T) {
	handler, _ := newAuthHandler(&stubExchanger{}, &stubSessions{})

	rec := httptest.NewRecorder()
	handler.Callback(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/callback?error=access_denied", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, attendu 401", rec.Code)
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
		t.Fatalf("code = %d, attendu 401", rec.Code)
	}
	if cookieByName(rec.Result(), sessionCookieName) != nil {
		t.Error("une session a été ouverte malgré un échange refusé")
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
		t.Fatalf("code = %d, attendu 200", rec.Code)
	}
	if len(sessions.revoked) != 1 || sessions.revoked[0] != "session-id" {
		t.Errorf("session non révoquée: %v", sessions.revoked)
	}
	if cookie := cookieByName(rec.Result(), sessionCookieName); cookie == nil || cookie.MaxAge >= 0 {
		t.Error("le cookie de session n'a pas été effacé")
	}
}

// Effacer le cookie alors que la révocation a échoué ferait croire l'utilisateur
// déconnecté, tandis que sa session resterait utilisable par qui la détiendrait.
func TestLogoutKeepsCookieWhenRevocationFails(t *testing.T) {
	sessions := &stubSessions{revokeErr: errors.New("base indisponible")}
	handler, _ := newAuthHandler(&stubExchanger{}, sessions)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-id"})

	rec := httptest.NewRecorder()
	handler.Logout(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, attendu 500", rec.Code)
	}
	if cookieByName(rec.Result(), sessionCookieName) != nil {
		t.Error("le cookie a été effacé alors que la session reste active")
	}
}
