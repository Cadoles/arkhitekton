package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cadoles/arkhitekton/internal/store"
)

func project1() *int64 { id := int64(1); return &id }

// One entry per person: their own roles, their groups', and what applies with where it comes from;
// someone signed in with nothing granted comes first, waiting.
func TestPeopleSayWhatTheyMayDoAndWhy(t *testing.T) {
	seen := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	fake := &fakeAdminStore{
		accounts: []store.Account{
			{Subject: "121", Username: "vcarroy", Groups: []string{"team:dev"}, LastSeen: seen},
			{Subject: "150", Username: "newcomer", LastSeen: seen},
		},
		roles: []store.UserRole{
			{Subject: "121", Username: "vcarroy", Role: "viewer", ProjectID: project1(), ProjectSlug: "test", ProjectLabel: "Test", GrantedBy: "bootstrap"},
			// A role granted to someone who never signed in.
			{Subject: "160", Username: "later", Role: "operator", GrantedBy: "121"},
		},
		groupRoles: []store.GroupRole{{Group: "team:dev", Role: "releaser"}},
	}
	recorder := httptest.NewRecorder()
	NewAdminHandler(fake).People(recorder, adminRequest(http.MethodGet, "/admin/people", ""))

	var payload struct {
		People []personPayload `json:"people"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.People) != 3 || payload.People[0].Subject != "150" || !payload.People[0].Pending {
		t.Fatalf("people = %+v", payload.People)
	}
	me := payload.People[1]
	if me.Global != (effectivePayload{Role: "releaser", Source: "team:dev"}) {
		t.Errorf("global = %+v, expected the group's role and its name", me.Global)
	}
	if len(me.Projects) != 1 || me.Projects[0] != (effectivePayload{Project: "test", ProjectLabel: "Test", Role: "viewer"}) {
		t.Errorf("projects = %+v, expected the person's own restriction", me.Projects)
	}
	if len(me.Roles) != 1 || !me.Roles[0].Bootstrap {
		t.Errorf("roles = %+v", me.Roles)
	}
	if later := payload.People[2]; later.Username != "later" || later.Global.Role != "operator" || later.LastSeen != nil {
		t.Errorf("never signed in = %+v", later)
	}
}

func TestAGroupIsGivenARoleAndLosesIt(t *testing.T) {
	fake := &fakeAdminStore{
		accounts:   []store.Account{{Subject: "121", Groups: []string{"team", "team:dev"}}, {Subject: "130", Groups: []string{"team"}}},
		groupRoles: []store.GroupRole{{Group: "team", Role: "viewer"}},
	}
	handler := NewAdminHandler(fake)

	put := func(group, body string) int {
		request := adminRequest(http.MethodPut, "/admin/groups/"+group+"/roles", body)
		request.SetPathValue("group", group)
		recorder := httptest.NewRecorder()
		handler.SetGroupRole(recorder, request)
		return recorder.Code
	}
	if code := put("team:dev", `{"role":"operator","project":"test"}`); code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if code := put("team:dev", `{"role":"admin","project":"test"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("admin in a project: %d", code)
	}
	if fake.granted[0] != "group:team:dev=operator@1" || !strings.Contains(strings.Join(fake.audit, ","), "group.grant:team:dev") {
		t.Errorf("granted %v, audit %v", fake.granted, fake.audit)
	}

	request := adminRequest(http.MethodDelete, "/admin/groups/team/roles", "")
	request.SetPathValue("group", "team")
	recorder := httptest.NewRecorder()
	handler.ClearGroupRole(recorder, request)
	if recorder.Code != http.StatusOK || fake.revoked[0] != "group:team" {
		t.Errorf("clear: %d %v", recorder.Code, fake.revoked)
	}

	recorder = httptest.NewRecorder()
	handler.Groups(recorder, adminRequest(http.MethodGet, "/admin/groups", ""))
	var payload struct {
		Groups []groupPayload `json:"groups"`
		Known  []groupPayload `json:"known"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if len(payload.Groups) != 1 || payload.Groups[0].Members != 2 || len(payload.Known) != 2 || payload.Known[1].Group != "team:dev" {
		t.Errorf("groups = %+v, known = %+v", payload.Groups, payload.Known)
	}
}
