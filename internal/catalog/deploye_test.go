package catalog

import "testing"

// Le catalogue réellement déployé doit se charger.
//
// Il est édité à la main, dans un fichier long, et un `suggest` mal orthographié
// n'a aucune conséquence visible avant le démarrage du binaire en cluster. Le
// lire ici le fait échouer au bon endroit.
func TestLeCatalogueDeployeSeCharge(t *testing.T) {
	catalog, err := Load("../../deploy/base/config/catalog.yaml")
	if err != nil {
		t.Fatalf("catalogue déployé: %v", err)
	}

	for _, pipeline := range catalog.Pipelines {
		for _, input := range pipeline.Inputs() {
			// Un paramètre saisi sans nature déclarée n'est pas une faute, mais
			// c'est un champ vide de plus dans le formulaire. Le signaler garde
			// la question ouverte.
			if input.Suggest == "" {
				t.Logf("%s/%s: aucune suggestion déclarée", pipeline.Kind, input.Name)
			}
		}
	}
}
