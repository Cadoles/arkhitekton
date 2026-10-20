package mse

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// Sensitivity comes from the project's section, and an environment that does not declare it is
// sensitive: forgetting must make promotion harder, never easier.
func TestSensitivityComesFromTheCatalogueAndForgettingIsSensitive(t *testing.T) {
	non, oui := false, true
	authorizer := auth.NewAuthorizer(Policies(&catalog.Catalog{Projects: map[string]catalog.ProjectDeclaration{
		"mse": {Environments: []catalog.Environment{
			{ID: "qualif", Sensitive: &non},
			{ID: "prod", Sensitive: &oui},
			{ID: "oublie"},
		}},
	}}))

	operator := auth.Identity{Subject: "s", Role: auth.RoleOperator}
	for environment, expected := range map[string]bool{"qualif": true, "prod": false, "oublie": false, "unknown": false} {
		if got := authorizer.CanDeploy(operator, 0, "mse", environment); got != expected {
			t.Errorf("operator to %s: %v, expected %v", environment, got, expected)
		}
	}
	if authorizer.CanDeploy(operator, 0, "other", "qualif") {
		t.Error("a project with no section returns no environment an operator can promote to")
	}
	if authorizer.Sensitive("mse", "qualif") || !authorizer.Sensitive("mse", "oublie") {
		t.Error("sensitivity wrongly returned to the interface")
	}
}
