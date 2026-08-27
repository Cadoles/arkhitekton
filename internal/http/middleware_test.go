package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

func testMiddleware(sessions Sessions) *Middleware {
	authorizer := auth.NewAuthorizer(map[string]auth.EnvironmentPolicy{
		"unstable": {Sensitive: false},
		"prod":     {Sensitive: true},
	})
	return NewMiddleware(sessions, authorizer, quietLogger())
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthenticateRejectsRequestWithoutCookie(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	rec := httptest.NewRecorder()
	m.Authenticate(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/versions", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, attendu 401", rec.Code)
	}
}

func TestAuthenticateRejectsExpiredSession(t *testing.T) {
	m := testMiddleware(&stubSessions{lookupErr: auth.ErrSessionNotFound})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/versions", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expirée"})

	rec := httptest.NewRecorder()
	m.Authenticate(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, attendu 401", rec.Code)
	}
}

// Se réauthentifier ne changerait rien : renvoyer 401 boucherait l'utilisateur
// dans un cycle de connexion. Le 403 dit ce qu'il faut faire.
func TestAuthenticateReturns403ForUserWithoutRole(t *testing.T) {
	m := testMiddleware(&stubSessions{
		lookup:    auth.Session{Subject: "sub", Username: "nouveau"},
		lookupErr: auth.ErrNoRole,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/versions", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valide"})

	rec := httptest.NewRecorder()
	m.Authenticate(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, attendu 403", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "rôle") {
		t.Errorf("le message n'explique pas la situation: %s", body)
	}
}

func TestAuthenticatePlacesIdentityInContext(t *testing.T) {
	m := testMiddleware(&stubSessions{
		lookup:     auth.Session{Subject: "sub-1", Username: "alice", Email: "a@x.fr"},
		lookupRole: auth.RoleOperator,
	})

	var seen auth.Identity
	handler := m.Authenticate(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = IdentityFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/versions", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valide"})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen.Subject != "sub-1" || seen.Username != "alice" || seen.Role != auth.RoleOperator {
		t.Errorf("identité incorrecte: %+v", seen)
	}
}

func TestRequireEnforcesPermission(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	cases := []struct {
		role     auth.Role
		perm     auth.Permission
		expected int
	}{
		{auth.RoleViewer, auth.PermRead, http.StatusOK},
		{auth.RoleViewer, auth.PermPipelineRun, http.StatusForbidden},
		{auth.RoleOperator, auth.PermPipelineRun, http.StatusOK},
		{auth.RoleOperator, auth.PermReleaseFinal, http.StatusForbidden},
		{auth.RoleReleaser, auth.PermReleaseFinal, http.StatusOK},
		{auth.RoleOperator, auth.PermAdmin, http.StatusForbidden},
		{auth.RoleAdmin, auth.PermAdmin, http.StatusOK},
	}

	for _, c := range cases {
		handler := m.Require(c.perm)(okHandler())
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(withIdentity(req, auth.Identity{Subject: "s", Role: c.role}))

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != c.expected {
			t.Errorf("role=%s perm=%s: code = %d, attendu %d", c.role, c.perm, rec.Code, c.expected)
		}
	}
}

func TestRequireRejectsRequestWithoutIdentity(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	rec := httptest.NewRecorder()
	m.Require(auth.PermRead)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, attendu 401", rec.Code)
	}
}

// Un projet à résoudre pour les contrôles rattachés. Un seul, reconnu par son
// slug : ces tests portent sur la permission, pas sur la résolution.
type stubProjects struct{}

func (stubProjects) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "test" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug}, nil
}

func TestRequireDeployReadsEnvironmentFromPath(t *testing.T) {
	// La promotion se vérifie désormais **dans un projet** : le chemin le porte,
	// et le middleware refuse s'il ne peut pas le résoudre.
	m := testMiddleware(&stubSessions{}).WithProjects(stubProjects{})

	mux := http.NewServeMux()
	mux.Handle("POST /projects/{project}/deploy/{env}", m.RequireDeploy("env")(okHandler()))

	cases := []struct {
		role     auth.Role
		env      string
		expected int
	}{
		{auth.RoleOperator, "unstable", http.StatusOK},
		{auth.RoleOperator, "prod", http.StatusForbidden},
		{auth.RoleReleaser, "prod", http.StatusOK},
		// Environnement non déclaré : échec fermé.
		{auth.RoleOperator, "inconnu", http.StatusForbidden},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/projects/test/deploy/"+c.env, nil)
		req = req.WithContext(withIdentity(req, auth.Identity{Subject: "s", Role: c.role}))

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != c.expected {
			t.Errorf("role=%s env=%s: code = %d, attendu %d", c.role, c.env, rec.Code, c.expected)
		}
	}
}

func TestCSRFAllowsSafeMethods(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		rec := httptest.NewRecorder()
		m.CSRF(okHandler()).ServeHTTP(rec, httptest.NewRequest(method, "/x", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s refusé sans jeton CSRF: %d", method, rec.Code)
		}
	}
}

// SameSite=Lax ne couvre pas une requête émise par une page tierce déjà chargée :
// une page tierce peut la déclencher, jamais lire le cookie pour en recopier la
// valeur dans l'en-tête.
func TestCSRFRejectsUnsafeMethodWithoutMatchingToken(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	cases := []struct {
		name   string
		cookie string
		header string
	}{
		{"aucun jeton", "", ""},
		{"cookie seul", "jeton-abc", ""},
		{"en-tête seul", "", "jeton-abc"},
		{"valeurs différentes", "jeton-a", "jeton-b"},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: c.cookie})
		}
		if c.header != "" {
			req.Header.Set(csrfHeaderName, c.header)
		}

		rec := httptest.NewRecorder()
		m.CSRF(okHandler()).ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: code = %d, attendu 403", c.name, rec.Code)
		}
	}
}

func TestCSRFAcceptsMatchingToken(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "jeton-partage-abc123"})
	req.Header.Set(csrfHeaderName, "jeton-partage-abc123")

	rec := httptest.NewRecorder()
	m.CSRF(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu 200", rec.Code)
	}
}

func TestChainAppliesMiddlewaresInReadingOrder(t *testing.T) {
	var order []string
	record := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	handler := Chain(okHandler(), record("premier"), record("second"))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if len(order) != 2 || order[0] != "premier" || order[1] != "second" {
		t.Errorf("ordre d'exécution inattendu: %v", order)
	}
}

// withIdentity simule ce que pose le middleware d'authentification, pour tester
// l'autorisation isolément.
func withIdentity(r *http.Request, identity auth.Identity) context.Context {
	return context.WithValue(r.Context(), identityKey, identity)
}

// Une permission accordée sur un projet ne franchit pas la frontière du suivant.
//
// C'est le contrôle que `RequireIn` existe pour poser : vérifier globalement
// accorderait sur tous les projets ce qui n'a été accordé que sur un, et l'écart
// ne se verrait qu'en constatant qu'une personne agit là où elle ne devrait pas.
func TestRequireInNaccordeQueDansSonProjet(t *testing.T) {
	m := testMiddleware(&stubSessions{}).WithProjects(stubProjects{})

	mux := http.NewServeMux()
	mux.Handle("POST /projects/{project}/build",
		m.RequireIn(auth.PermReleaseCreate)(okHandler()))

	// `operator` sur le projet 1 seulement, aucun rôle global.
	identity := auth.Identity{Subject: "s", Projects: map[int64]auth.Role{1: auth.RoleOperator}}

	req := httptest.NewRequest(http.MethodPost, "/projects/test/build", nil)
	req = req.WithContext(withIdentity(req, identity))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("le rôle du projet doit s'y appliquer: code = %d", rec.Code)
	}

	// Un projet que le résolveur ne connaît pas : 404, et surtout pas un passage.
	req = httptest.NewRequest(http.MethodPost, "/projects/absent/build", nil)
	req = req.WithContext(withIdentity(req, identity))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("un projet inconnu doit être refusé: code = %d", rec.Code)
	}
}

// Sans résolution branchée, le contrôle refuse au lieu de laisser passer.
//
// C'est le sens du doute qui compte : un middleware mal câblé qui accorderait
// serait une faille silencieuse, alors qu'un refus se voit au premier essai.
func TestRequireInRefuseSansResolutionDeProjet(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	mux := http.NewServeMux()
	mux.Handle("POST /projects/{project}/build",
		m.RequireIn(auth.PermReleaseCreate)(okHandler()))

	req := httptest.NewRequest(http.MethodPost, "/projects/test/build", nil)
	req = req.WithContext(withIdentity(req, auth.Identity{Subject: "s", Role: auth.RoleAdmin}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("un contrôle sans résolution de projet ne doit jamais accorder")
	}
}
