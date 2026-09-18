package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeAdminStore struct {
	roles   []store.UserRole
	entries []store.AuditEntry
	granted []string
	revoked []string
	audit   []string
	err     error
}

func (f *fakeAdminStore) ListUserRoles(context.Context) ([]store.UserRole, error) {
	return f.roles, nil
}

func (f *fakeAdminStore) SetRoleIn(
	_ context.Context, subject, _, role string, projectID *int64, _ string,
) error {
	if f.err != nil {
		return f.err
	}
	// The scope is part of the record: a global grant and a project grant must not look alike
	// in the assertions.
	scope := ""
	if projectID != nil {
		scope = fmt.Sprintf("@%d", *projectID)
	}
	f.granted = append(f.granted, subject+"="+role+scope)
	return nil
}

// ProjectBySlug resolves the scope named in the request.
func (f *fakeAdminStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "test" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug}, nil
}

func (f *fakeAdminStore) RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error {
	if projectID != nil {
		// The scope is part of the record: removing a project role and removing the global role
		// must not look alike in the assertions.
		if f.err != nil {
			return f.err
		}
		f.revoked = append(f.revoked, fmt.Sprintf("%s@%d", subject, *projectID))
		return nil
	}
	return f.RevokeRole(ctx, subject)
}

func (f *fakeAdminStore) RevokeRole(_ context.Context, subject string) error {
	if f.err != nil {
		return f.err
	}
	f.revoked = append(f.revoked, subject)
	return nil
}

func (f *fakeAdminStore) ListAudit(_ context.Context, target string, _ int) ([]store.AuditEntry, error) {
	if target == "" {
		return f.entries, nil
	}
	var scoped []store.AuditEntry
	for _, entry := range f.entries {
		if entry.Target == target {
			scoped = append(scoped, entry)
		}
	}
	return scoped, nil
}

func (f *fakeAdminStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

func adminRequest(method, path, body string) *http.Request {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	return asRole(request, auth.RoleAdmin)
}

// The catalogue comes with the list: the interface offers the possible roles without
// hard-coding them, and what a role allows stays decided by internal/auth.
func TestRolesCarryTheCatalogue(t *testing.T) {
	fake := &fakeAdminStore{roles: []store.UserRole{
		{Subject: "alice", Username: "alice", Role: "admin", GrantedBy: "bootstrap"},
	}}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Roles(recorder, adminRequest(http.MethodGet, "/admin/roles", ""))

	var payload struct {
		Roles     []userRolePayload `json:"roles"`
		Catalogue []struct {
			Role        string   `json:"role"`
			Permissions []string `json:"permissions"`
		} `json:"catalogue"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Catalogue) != len(auth.AllRoles) {
		t.Errorf("catalogue = %d entries", len(payload.Catalogue))
	}

	// Permissions are cumulative: a releaser can launch a pipeline.
	for _, entry := range payload.Catalogue {
		if entry.Role != "releaser" {
			continue
		}
		var canRun bool
		for _, permission := range entry.Permissions {
			if permission == string(auth.PermPipelineRun) {
				canRun = true
			}
		}
		if !canRun {
			t.Errorf("releaser permissions = %v, expected cumulative", entry.Permissions)
		}
	}
	// `bootstrap` surfaces: removing such a role without touching the configuration would make
	// it reappear at the next restart.
	if payload.Roles[0].GrantedBy != "bootstrap" {
		t.Errorf("provenance = %q", payload.Roles[0].GrantedBy)
	}
}

// Authorization is about the identity provider's subject. A username changes, a subject
// does not.
func TestGrantRequiresASubject(t *testing.T) {
	fake := &fakeAdminStore{}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Grant(recorder, adminRequest(http.MethodPost, "/admin/roles",
		`{"username":"bob","role":"operator"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, expected 400", recorder.Code)
	}
	if len(fake.granted) != 0 {
		t.Error("no role must be granted without a subject")
	}
}

func TestGrantRejectsAnUnknownRole(t *testing.T) {
	fake := &fakeAdminStore{}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Grant(recorder, adminRequest(http.MethodPost, "/admin/roles",
		`{"subject":"bob","role":"superadmin"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, expected 400", recorder.Code)
	}
}

func TestGrantIsAudited(t *testing.T) {
	fake := &fakeAdminStore{}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Grant(recorder, adminRequest(http.MethodPost, "/admin/roles",
		`{"subject":"bob","username":"bob","role":"releaser"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.granted) != 1 || fake.granted[0] != "bob=releaser" {
		t.Errorf("attributions = %v", fake.granted)
	}
	// Granting a role decides who can ship: the record is not optional.
	if len(fake.audit) != 1 || fake.audit[0] != "role.grant:bob" {
		t.Errorf("journal = %v", fake.audit)
	}

	// The interface must know the sessions were closed, otherwise an administrator would
	// believe the old role still active.
	var payload map[string]any
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	if payload["sessions_closed"] != true {
		t.Errorf("response = %v", payload)
	}
}

// 409 and not 403: the request is legitimate and the caller has the right, it is the
// system's state that forbids it.
func TestLastAdminIsAConflictNotAForbidden(t *testing.T) {
	fake := &fakeAdminStore{err: store.ErrLastAdmin}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Revoke(recorder, withSubject(adminRequest(http.MethodDelete, "/admin/roles/alice", ""), "alice"))

	if recorder.Code != http.StatusConflict {
		t.Errorf("code = %d, expected 409", recorder.Code)
	}
	// The reason is returned: "impossible" with no explanation leaves people searching.
	if !strings.Contains(recorder.Body.String(), "administrateur") {
		t.Errorf("corps = %s", recorder.Body.String())
	}
}

func TestRevokeAnUnknownSubjectIsNotFound(t *testing.T) {
	fake := &fakeAdminStore{err: store.ErrRoleNotFound}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Revoke(recorder, withSubject(adminRequest(http.MethodDelete, "/admin/roles/x", ""), "x"))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code = %d, expected 404", recorder.Code)
	}
}

// "What happened to this release" is the read actually performed, and the index exists for
// it.
func TestAuditCanBeScopedToATarget(t *testing.T) {
	fake := &fakeAdminStore{entries: []store.AuditEntry{
		{ID: 1, Actor: "alice", Action: "release.promote", Target: "1.8.0"},
		{ID: 2, Actor: "bob", Action: "job.enqueue", Target: "autre"},
	}}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Audit(recorder, adminRequest(http.MethodGet, "/admin/audit?target=1.8.0", ""))

	var payload struct {
		Entries []auditPayload `json:"entries"`
	}
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	if len(payload.Entries) != 1 || payload.Entries[0].Target != "1.8.0" {
		t.Errorf("entries = %+v", payload.Entries)
	}
}

func withSubject(request *http.Request, subject string) *http.Request {
	request.SetPathValue("subject", subject)
	return request
}

// The directory of actor names. Empty: these tests are about behaviour, not display, and
// an empty directory falls back to the subject — exactly what an unknown actor does.
func (f *fakeAdminStore) Directory(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
