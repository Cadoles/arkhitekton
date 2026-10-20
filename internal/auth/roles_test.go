package auth

import "testing"

func testAuthorizer() *Authorizer {
	return NewAuthorizer(ProjectPolicies{"mse": {
		"unstable": {Sensitive: false},
		"qualif":   {Sensitive: false},
		"preprod":  {Sensitive: true},
		"prod":     {Sensitive: true},
	}})
}

func identity(role Role) Identity {
	return Identity{Subject: "s", Username: "u", Role: role}
}

func TestRoleHierarchyGrantsLowerPermissions(t *testing.T) {
	a := testAuthorizer()

	cases := []struct {
		role    Role
		perm    Permission
		allowed bool
	}{
		{RoleViewer, PermRead, true},
		{RoleViewer, PermReleaseExport, true},
		{RoleViewer, PermPipelineRun, false},
		{RoleViewer, PermReleaseCreate, false},

		{RoleOperator, PermRead, true},
		{RoleOperator, PermPipelineRun, true},
		{RoleOperator, PermReleaseCreate, true},
		{RoleOperator, PermReleaseFinal, false},
		{RoleOperator, PermAdmin, false},

		{RoleReleaser, PermReleaseFinal, true},
		{RoleReleaser, PermPipelineRun, true},
		{RoleReleaser, PermAdmin, false},

		{RoleAdmin, PermAdmin, true},
		{RoleAdmin, PermReleaseFinal, true},
		{RoleAdmin, PermRead, true},
	}

	for _, c := range cases {
		if got := a.Can(identity(c.role), c.perm); got != c.allowed {
			t.Errorf("Can(%s, %s) = %v, expected %v", c.role, c.perm, got, c.allowed)
		}
	}
}

func TestDeployRequiresReleaserOnSensitiveEnvironments(t *testing.T) {
	a := testAuthorizer()

	cases := []struct {
		role    Role
		env     string
		allowed bool
	}{
		{RoleOperator, "unstable", true},
		{RoleOperator, "qualif", true},
		{RoleOperator, "preprod", false},
		{RoleOperator, "prod", false},

		{RoleReleaser, "preprod", true},
		{RoleReleaser, "prod", true},

		{RoleViewer, "unstable", false},
		{RoleAdmin, "prod", true},
	}

	for _, c := range cases {
		if got := a.CanDeploy(identity(c.role), 0, "mse", c.env); got != c.allowed {
			t.Errorf("Can(%s, deploy:%s) = %v, expected %v", c.role, c.env, got, c.allowed)
		}
	}
}

// An environment created and then forgotten in the configuration must be harder to
// reach, never easier: erring this way costs time, erring the other way lets
// someone promote to production.
func TestDeployOnUnknownEnvironmentFailsClosed(t *testing.T) {
	a := testAuthorizer()

	if a.CanDeploy(identity(RoleOperator), 0, "mse", "nouveau-env") {
		t.Error("an unknown environment must not be deployable by an operator")
	}
	if a.CanDeploy(identity(RoleOperator), 0, "other-project", "unstable") {
		t.Error("an environment of an undeclared project must not be deployable by an operator")
	}
	if a.Can(identity(RoleOperator), PermDeploy("unstable")) {
		t.Error("without a project the sensitivity is unknown: an operator does not promote")
	}
	if !a.CanDeploy(identity(RoleReleaser), 0, "mse", "nouveau-env") {
		t.Error("a releaser must be able to deploy an unknown environment")
	}
}

// An empty or invented role must satisfy no requirement — not even read. That is
// the case of a user who is authenticated but has no role.
func TestInvalidRoleGrantsNothing(t *testing.T) {
	a := testAuthorizer()

	for _, role := range []Role{"", "superadmin", "VIEWER", "root"} {
		for _, perm := range []Permission{PermRead, PermAdmin, PermDeploy("unstable")} {
			if a.Can(Identity{Role: role}, perm) {
				t.Errorf("invalid role %q obtained %s", role, perm)
			}
		}
	}
}

func TestParseRole(t *testing.T) {
	for _, valid := range []string{"viewer", " OPERATOR ", "Releaser", "admin"} {
		if _, err := ParseRole(valid); err != nil {
			t.Errorf("ParseRole(%q) refused: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "superuser", "read-only"} {
		if _, err := ParseRole(invalid); err == nil {
			t.Errorf("ParseRole(%q) wrongly accepted", invalid)
		}
	}
}

// With no environment declared, no promotion may pass below the releaser role: an
// empty configuration must not open the gates.
func TestEmptyAuthorizerFailsClosed(t *testing.T) {
	a := NewAuthorizer(nil)

	if a.CanDeploy(identity(RoleOperator), 0, "mse", "unstable") {
		t.Error("an empty configuration must allow an operator no promotion")
	}
	if !a.Can(identity(RoleOperator), PermPipelineRun) {
		t.Error("permissions unrelated to deployment stay unchanged")
	}
}

// A role granted on a project holds only there.
//
// That is what per-project roles are for: granting releaser on an unrelated
// repository because someone holds it on the main one is an authorization nobody
// asked for.
func TestAProjectRoleHoldsOnlyThere(t *testing.T) {
	authorizer := NewAuthorizer(nil)

	// No global role, releaser on project 1 only.
	identity := Identity{Subject: "121", Projects: map[int64]Role{1: RoleReleaser}}

	if !authorizer.CanIn(identity, 1, PermReleaseCreate) {
		t.Error("the role on project 1 must apply there")
	}
	if authorizer.CanIn(identity, 2, PermReleaseCreate) {
		t.Error("the role on project 1 must grant nothing on project 2")
	}
	// And globally, nothing: with no global role, a check outside a project must
	// refuse rather than fall back to the highest role found elsewhere.
	if authorizer.Can(identity, PermRead) {
		t.Error("a project role must grant nothing outside its project")
	}
}

// The most specific wins, including to restrict.
//
// Without that a restriction would be inexpressible: the global role would have to
// be removed to impose one on a single project.
func TestAProjectRoleCanRestrictTheGlobalRole(t *testing.T) {
	authorizer := NewAuthorizer(nil)

	identity := Identity{
		Subject:  "121",
		Role:     RoleOperator,
		Projects: map[int64]Role{2: RoleViewer},
	}

	if !authorizer.CanIn(identity, 1, PermReleaseCreate) {
		t.Error("the global role must apply to projects declaring none")
	}
	if authorizer.CanIn(identity, 2, PermReleaseCreate) {
		t.Error("the role on project 2 must beat the global role")
	}
	if !authorizer.CanIn(identity, 2, PermRead) {
		t.Error("the viewer on project 2 still keeps read access")
	}
}

// Promoting is the costliest gesture: it must be checked inside the project.
func TestPromotionIsCheckedInItsProject(t *testing.T) {
	authorizer := NewAuthorizer(ProjectPolicies{"mse": {"prod": {Sensitive: true}}})

	identity := Identity{Subject: "121", Projects: map[int64]Role{1: RoleReleaser}}

	if !authorizer.CanDeploy(identity, 1, "mse", "prod") {
		t.Error("the releaser on project 1 must be able to promote there")
	}
	if authorizer.CanDeploy(identity, 2, "mse", "prod") {
		t.Error("they must be able to promote nothing on project 2")
	}
}

// The same environment id may be sensitive in one project and not in another: the
// sensitivity is read from the project's declaration.
func TestSensitivityIsReadFromTheProject(t *testing.T) {
	authorizer := NewAuthorizer(ProjectPolicies{
		"mse":    {"qualif": {Sensitive: false}},
		"themes": {"qualif": {Sensitive: true}},
	})
	operator := identity(RoleOperator)

	if !authorizer.CanDeploy(operator, 0, "mse", "qualif") || authorizer.Sensitive("mse", "qualif") {
		t.Error("qualif on project mse is not sensitive")
	}
	if authorizer.CanDeploy(operator, 0, "themes", "qualif") || !authorizer.Sensitive("themes", "qualif") {
		t.Error("qualif on project themes is sensitive")
	}
}
