package mse

import (
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
)

// La sensibilité vient du catalogue, et un environnement qui ne la déclare pas est
// sensible : l'oubli doit rendre la promotion plus difficile, jamais plus facile.
func TestLaSensibiliteVientDuCatalogueEtLOubliEstSensible(t *testing.T) {
	non, oui := false, true
	authorizer := auth.NewAuthorizer(Policies([]catalog.Environment{
		{ID: "qualif", Sensitive: &non},
		{ID: "prod", Sensitive: &oui},
		{ID: "oublie"},
	}))

	operator := auth.Identity{Subject: "s", Role: auth.RoleOperator}
	for environment, attendu := range map[string]bool{"qualif": true, "prod": false, "oublie": false, "inconnu": false} {
		if got := authorizer.Can(operator, auth.PermDeploy(environment)); got != attendu {
			t.Errorf("operator vers %s: %v, attendu %v", environment, got, attendu)
		}
	}
	if authorizer.Sensitive("qualif") || !authorizer.Sensitive("oublie") {
		t.Error("sensibilité mal rendue à l'interface")
	}
}

// Le catalogue réellement déployé garde la table qu'écrivait `main.go` : la
// déplacer ne doit rien changer à qui peut promouvoir où.
func TestLeCatalogueDeployeRepriseLaTableDeMain(t *testing.T) {
	deployed, err := catalog.Load("../../deploy/base/config/catalog.yaml")
	if err != nil {
		t.Fatalf("catalogue déployé: %v", err)
	}
	policies := Policies(deployed.Environments)

	for environment, sensitive := range map[string]bool{
		"unstable": false, "qualif": false, "preprod": true, "prod": true,
	} {
		policy, found := policies[environment]
		if !found || policy.Sensitive != sensitive {
			t.Errorf("%s: %+v (trouvé %v), attendu sensible=%v", environment, policy, found, sensitive)
		}
	}
}
