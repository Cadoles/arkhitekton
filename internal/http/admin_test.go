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
	// La portée fait partie de la trace : une attribution globale et une
	// attribution de projet ne doivent pas se ressembler dans les assertions.
	scope := ""
	if projectID != nil {
		scope = fmt.Sprintf("@%d", *projectID)
	}
	f.granted = append(f.granted, subject+"="+role+scope)
	return nil
}

// ProjectBySlug résout la portée nommée dans la requête.
func (f *fakeAdminStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "test" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug}, nil
}

func (f *fakeAdminStore) RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error {
	if projectID != nil {
		// La portée fait partie de la trace : retirer un rôle de projet et
		// retirer le rôle global ne doivent pas se ressembler dans les
		// assertions.
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

// Le catalogue accompagne la liste : l'interface propose les rôles possibles
// sans les coder en dur, et ce qu'un rôle permet reste décidé par internal/auth.
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
		t.Fatalf("décodage: %v", err)
	}
	if len(payload.Catalogue) != len(auth.AllRoles) {
		t.Errorf("catalogue = %d entrées", len(payload.Catalogue))
	}

	// Les permissions sont cumulées : un releaser peut lancer un pipeline.
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
			t.Errorf("permissions de releaser = %v, attendues cumulées", entry.Permissions)
		}
	}
	// `bootstrap` remonte : retirer un tel rôle sans toucher à la configuration
	// le ferait réapparaître au redémarrage.
	if payload.Roles[0].GrantedBy != "bootstrap" {
		t.Errorf("provenance = %q", payload.Roles[0].GrantedBy)
	}
}

// L'autorisation porte sur le sujet du fournisseur d'identité. Un nom
// d'utilisateur change, un sujet non.
func TestGrantRequiresASubject(t *testing.T) {
	fake := &fakeAdminStore{}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Grant(recorder, adminRequest(http.MethodPost, "/admin/roles",
		`{"username":"bob","role":"operator"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, attendu 400", recorder.Code)
	}
	if len(fake.granted) != 0 {
		t.Error("aucun rôle ne doit être attribué sans sujet")
	}
}

func TestGrantRejectsAnUnknownRole(t *testing.T) {
	fake := &fakeAdminStore{}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Grant(recorder, adminRequest(http.MethodPost, "/admin/roles",
		`{"subject":"bob","role":"superadmin"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, attendu 400", recorder.Code)
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
	// Attribuer un rôle décide de qui peut livrer : la trace n'est pas
	// optionnelle.
	if len(fake.audit) != 1 || fake.audit[0] != "role.grant:bob" {
		t.Errorf("journal = %v", fake.audit)
	}

	// L'interface doit savoir que les sessions sont tombées, sans quoi un
	// administrateur croirait l'ancien rôle encore actif.
	var payload map[string]any
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	if payload["sessions_closed"] != true {
		t.Errorf("réponse = %v", payload)
	}
}

// 409 et non 403 : la requête est légitime et l'appelant a le droit, c'est
// l'état du système qui l'interdit.
func TestLastAdminIsAConflictNotAForbidden(t *testing.T) {
	fake := &fakeAdminStore{err: store.ErrLastAdmin}
	handler := NewAdminHandler(fake)

	recorder := httptest.NewRecorder()
	handler.Revoke(recorder, withSubject(adminRequest(http.MethodDelete, "/admin/roles/alice", ""), "alice"))

	if recorder.Code != http.StatusConflict {
		t.Errorf("code = %d, attendu 409", recorder.Code)
	}
	// Le motif est rendu : « impossible » sans explication laisse chercher.
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
		t.Errorf("code = %d, attendu 404", recorder.Code)
	}
}

// « Qu'est-il arrivé à cette release » est la lecture réellement faite, et
// l'index existe pour elle.
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
		t.Errorf("entrées = %+v", payload.Entries)
	}
}

func withSubject(request *http.Request, subject string) *http.Request {
	request.SetPathValue("subject", subject)
	return request
}

// L'annuaire des noms d'acteurs. Vide : ces tests portent sur des comportements,
// pas sur l'affichage, et un annuaire vide fait retomber sur le sujet — le
// comportement exact d'un acteur inconnu.
func (f *fakeAdminStore) Directory(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
