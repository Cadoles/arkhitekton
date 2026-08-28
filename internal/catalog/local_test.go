package catalog

import (
	"os"
	"testing"
)

// Le catalogue livré doit rester analysable : il est monté en ConfigMap, et une
// faute de frappe ne se verrait qu'au démarrage d'un pod.
func TestShippedCatalogParses(t *testing.T) {
	path := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skip("catalogue livré introuvable")
	}

	catalog, err := Load(path)
	if err != nil {
		t.Fatalf("le catalogue livré est invalide: %v", err)
	}

	if len(catalog.Pipelines) == 0 {
		t.Error("aucun pipeline déclaré")
	}
	if len(catalog.Environments) != 4 {
		t.Errorf("%d environnement(s), attendu 4", len(catalog.Environments))
	}

	// Le piège de B7, vérifié sur le fichier réellement livré : Flux désigne
	// qualif sous le nom `testing`, et s'en tromper promeut vers rien.
	var qualif *Environment
	for i := range catalog.Environments {
		if catalog.Environments[i].ID == "qualif" {
			qualif = &catalog.Environments[i]
		}
	}
	if qualif == nil {
		t.Fatal("environnement qualif absent")
	}
	// Le suffixe vit sous `deployer:` depuis le lot D : le catalogue le
	// transporte sans le lire, et seul le déployeur le nomme.
	if qualif.Deployer["promotionSuffix"] != "testing" {
		t.Errorf("suffixe de qualif = %q, attendu testing",
			qualif.Deployer["promotionSuffix"])
	}

	for _, environment := range catalog.Environments {
		if environment.ID == "prod" {
			if environment.Deployer["acceptsPrerelease"] != "false" {
				t.Error("la production ne doit pas accepter les pré-releases")
			}
			if environment.IsObservable() {
				t.Error("la production tourne dans un cluster indépendant: non observable")
			}
		}
	}
}
