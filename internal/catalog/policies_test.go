package catalog

import (
	"testing"

	"github.com/Cadoles/arkhitekton/internal/auth"
)

// Sensitivity comes from the project's section, and an environment that does not declare it is
// sensitive: forgetting must make promotion harder, never easier.
func TestSensitivityComesFromTheCatalogueAndForgettingIsSensitive(t *testing.T) {
	non, oui := false, true
	authorizer := auth.NewAuthorizer((&Catalog{Projects: map[string]ProjectDeclaration{
		"shop": {Environments: []Environment{
			{ID: "qualif", Sensitive: &non},
			{ID: "prod", Sensitive: &oui},
			{ID: "oublie"},
		}},
	}}).EnvironmentPolicies())

	operator := auth.Identity{Subject: "s", Role: auth.RoleOperator}
	for environment, expected := range map[string]bool{"qualif": true, "prod": false, "oublie": false, "unknown": false} {
		if got := authorizer.CanDeploy(operator, 0, "shop", environment); got != expected {
			t.Errorf("operator to %s: %v, expected %v", environment, got, expected)
		}
	}
	if authorizer.CanDeploy(operator, 0, "other", "qualif") {
		t.Error("a project with no section returns no environment an operator can promote to")
	}
	if authorizer.Sensitive("shop", "qualif") || !authorizer.Sensitive("shop", "oublie") {
		t.Error("sensitivity wrongly returned to the interface")
	}
}
