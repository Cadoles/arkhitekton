package auth

import "testing"

func project(id int64) *int64 { return &id }

func TestAPersonsOwnRoleWinsOverTheirGroups(t *testing.T) {
	access := Resolve(
		[]Grant{{Role: RoleViewer}, {ProjectID: project(2), Role: RoleOperator}},
		[]Grant{
			{Role: RoleReleaser, Group: "team:dev"},
			{ProjectID: project(2), Role: RoleReleaser, Group: "team"},
			{ProjectID: project(3), Role: RoleOperator, Group: "team"},
		})
	if access.Global != (Effective{Role: RoleViewer}) {
		t.Errorf("global = %+v: the person's own restriction must hold", access.Global)
	}
	if access.Projects[2] != (Effective{Role: RoleOperator}) {
		t.Errorf("project 2 = %+v", access.Projects[2])
	}
	if access.Projects[3] != (Effective{Role: RoleOperator, Source: "team"}) {
		t.Errorf("project 3 = %+v: a group's grant applies where the person has none", access.Projects[3])
	}
}

func TestTheHighestGroupRoleApplies(t *testing.T) {
	access := Resolve(nil, []Grant{
		{Role: RoleViewer, Group: "team"},
		{Role: RoleOperator, Group: "team:dev"},
		{Role: RoleOperator, Group: "cadoles"},
		{Role: "unknown", Group: "other"},
	})
	// Two groups of the same rank: the first by name says where the role comes from.
	if access.Global != (Effective{Role: RoleOperator, Source: "cadoles"}) {
		t.Errorf("global = %+v", access.Global)
	}
	if access.None() {
		t.Error("access granted by groups reads as none")
	}
}

func TestNothingGrantedIsNoAccess(t *testing.T) {
	if access := Resolve(nil, nil); !access.None() {
		t.Errorf("access = %+v", access)
	}
}

// A groups claim is a list of names, or a single name; anything else is no group.
func TestGroupsOfReadsAListOrAName(t *testing.T) {
	if groups := GroupsOf([]any{"team", "team:dev", 3, ""}); len(groups) != 2 || groups[1] != "team:dev" {
		t.Errorf("list = %v", groups)
	}
	if groups := GroupsOf("team"); len(groups) != 1 {
		t.Errorf("name = %v", groups)
	}
	if groups := GroupsOf(map[string]any{}); groups != nil {
		t.Errorf("other = %v", groups)
	}
}
