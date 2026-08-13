package auth

import "testing"

func testAuthorizer() *Authorizer {
	return NewAuthorizer(map[string]EnvironmentPolicy{
		"unstable": {Sensitive: false},
		"qualif":   {Sensitive: false},
		"preprod":  {Sensitive: true},
		"prod":     {Sensitive: true},
	})
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
			t.Errorf("Can(%s, %s) = %v, attendu %v", c.role, c.perm, got, c.allowed)
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
		if got := a.Can(identity(c.role), PermDeploy(c.env)); got != c.allowed {
			t.Errorf("Can(%s, deploy:%s) = %v, attendu %v", c.role, c.env, got, c.allowed)
		}
	}
}

// Un environnement créé puis oublié dans la configuration doit être plus
// difficile à atteindre, jamais plus facile : se tromper dans ce sens fait
// perdre du temps, se tromper dans l'autre laisse promouvoir en production.
func TestDeployOnUnknownEnvironmentFailsClosed(t *testing.T) {
	a := testAuthorizer()

	if a.Can(identity(RoleOperator), PermDeploy("nouveau-env")) {
		t.Error("un environnement inconnu ne doit pas être déployable par un operator")
	}
	if !a.Can(identity(RoleReleaser), PermDeploy("nouveau-env")) {
		t.Error("un releaser doit pouvoir déployer un environnement inconnu")
	}
}

// Un rôle vide ou fantaisiste ne doit satisfaire aucune exigence — pas même la
// lecture. C'est le cas d'un utilisateur authentifié mais sans rôle attribué.
func TestInvalidRoleGrantsNothing(t *testing.T) {
	a := testAuthorizer()

	for _, role := range []Role{"", "superadmin", "VIEWER", "root"} {
		for _, perm := range []Permission{PermRead, PermAdmin, PermDeploy("unstable")} {
			if a.Can(Identity{Role: role}, perm) {
				t.Errorf("le rôle invalide %q a obtenu %s", role, perm)
			}
		}
	}
}

func TestParseRole(t *testing.T) {
	for _, valid := range []string{"viewer", " OPERATOR ", "Releaser", "admin"} {
		if _, err := ParseRole(valid); err != nil {
			t.Errorf("ParseRole(%q) refusé: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "superuser", "read-only"} {
		if _, err := ParseRole(invalid); err == nil {
			t.Errorf("ParseRole(%q) accepté à tort", invalid)
		}
	}
}

// Sans environnement déclaré, aucune promotion ne doit passer sous le rôle
// releaser : une configuration vide ne doit pas ouvrir les vannes.
func TestEmptyAuthorizerFailsClosed(t *testing.T) {
	a := NewAuthorizer(nil)

	if a.Can(identity(RoleOperator), PermDeploy("unstable")) {
		t.Error("une configuration vide ne doit autoriser aucune promotion à un operator")
	}
	if !a.Can(identity(RoleOperator), PermPipelineRun) {
		t.Error("les permissions non liées au déploiement restent inchangées")
	}
}
