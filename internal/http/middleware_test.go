package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

func testMiddleware(sessions Sessions) *Middleware {
	authorizer := auth.NewAuthorizer(auth.ProjectPolicies{"test": {
		"unstable": {Sensitive: false},
		"prod":     {Sensitive: true},
	}})
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
		t.Fatalf("code = %d, expected 401", rec.Code)
	}
}

func TestAuthenticateRejectsExpiredSession(t *testing.T) {
	m := testMiddleware(&stubSessions{lookupErr: auth.ErrSessionNotFound})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/versions", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expired"})

	rec := httptest.NewRecorder()
	m.Authenticate(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, expected 401", rec.Code)
	}
}

// Re-authenticating would change nothing: returning 401 would trap the user in a sign-in
// loop. The 403 says what to do.
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
		t.Fatalf("code = %d, expected 403", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "role") {
		t.Errorf("the message does not explain the situation: %s", body)
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
		t.Errorf("wrong identity: %+v", seen)
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
			t.Errorf("role=%s perm=%s: code = %d, expected %d", c.role, c.perm, rec.Code, c.expected)
		}
	}
}

func TestRequireRejectsRequestWithoutIdentity(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	rec := httptest.NewRecorder()
	m.Require(auth.PermRead)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, expected 401", rec.Code)
	}
}

// A project to resolve for the scoped checks. One, recognised by its slug: these tests are
// about the permission, not about resolution.
type stubProjects struct{}

func (stubProjects) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "test" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug}, nil
}

func TestRequireDeployReadsEnvironmentFromPath(t *testing.T) {
	// Promotion is now checked within a project: the path carries it, and the middleware
	// refuses if it cannot resolve it.
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
		{auth.RoleOperator, "unknown", http.StatusForbidden},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/projects/test/deploy/"+c.env, nil)
		req = req.WithContext(withIdentity(req, auth.Identity{Subject: "s", Role: c.role}))

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != c.expected {
			t.Errorf("role=%s env=%s: code = %d, expected %d", c.role, c.env, rec.Code, c.expected)
		}
	}
}

func TestCSRFAllowsSafeMethods(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		rec := httptest.NewRecorder()
		m.CSRF(okHandler()).ServeHTTP(rec, httptest.NewRequest(method, "/x", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s refused with no CSRF token: %d", method, rec.Code)
		}
	}
}

// SameSite=Lax does not cover a request issued by a third-party page already loaded: such a
// page can trigger it, never read the cookie to copy its value into the header.
func TestCSRFRejectsUnsafeMethodWithoutMatchingToken(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	cases := []struct {
		name   string
		cookie string
		header string
	}{
		{"no token", "", ""},
		{"cookie seul", "jeton-abc", ""},
		{"header only", "", "jeton-abc"},
		{"different values", "jeton-a", "jeton-b"},
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
			t.Errorf("%s: code = %d, expected 403", c.name, rec.Code)
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
		t.Fatalf("code = %d, expected 200", rec.Code)
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
		t.Errorf("unexpected execution order: %v", order)
	}
}

// withIdentity simulates what the authentication middleware sets, to test authorization in
// isolation.
func withIdentity(r *http.Request, identity auth.Identity) context.Context {
	return context.WithValue(r.Context(), identityKey, identity)
}

// A permission granted on one project does not cross into the next.
//
// That is the check RequireIn exists to make: checking globally would grant on every project
// what was granted on one, and the gap would show only by noticing someone acting where they
// should not.
func TestRequireInGrantsOnlyWithinItsProject(t *testing.T) {
	m := testMiddleware(&stubSessions{}).WithProjects(stubProjects{})

	mux := http.NewServeMux()
	mux.Handle("POST /projects/{project}/build",
		m.RequireIn(auth.PermReleaseCreate)(okHandler()))

	// `operator` on project 1 only, no global role.
	identity := auth.Identity{Subject: "s", Projects: map[int64]auth.Role{1: auth.RoleOperator}}

	req := httptest.NewRequest(http.MethodPost, "/projects/test/build", nil)
	req = req.WithContext(withIdentity(req, identity))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("the project's role must apply there: code = %d", rec.Code)
	}

	// A project the resolver does not know: 404, and certainly not a pass.
	req = httptest.NewRequest(http.MethodPost, "/projects/absent/build", nil)
	req = req.WithContext(withIdentity(req, identity))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unknown project must be refused: code = %d", rec.Code)
	}
}

// With no resolution wired, the check refuses instead of letting through.
//
// It is the direction of the doubt that counts: a badly wired middleware that granted would
// be a silent hole, whereas a refusal shows on the first attempt.
func TestRequireInRefusesWithoutProjectResolution(t *testing.T) {
	m := testMiddleware(&stubSessions{})

	mux := http.NewServeMux()
	mux.Handle("POST /projects/{project}/build",
		m.RequireIn(auth.PermReleaseCreate)(okHandler()))

	req := httptest.NewRequest(http.MethodPost, "/projects/test/build", nil)
	req = req.WithContext(withIdentity(req, auth.Identity{Subject: "s", Role: auth.RoleAdmin}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("a check with no project resolution must never grant")
	}
}
