package catalog

import "testing"

// Un catalogue sans le moindre réglage de déployeur doit être valide.
//
// `promotionSuffix` était requis, et son absence faisait échouer la validation :
// le catalogue imposait Flux à qui n'en avait pas. Passé par `Parse` : c'est le
// chemin réel, décodage strict compris.
func TestUnCatalogueSansReglageDeDeployeurEstValide(t *testing.T) {
	const catalogue = `
maxConcurrentRuns: 1
environments:
  - id: recette
    label: Recette
    rank: 1
    namespace: app-recette
  - id: prod
    label: Production
    rank: 2
    namespace: app-prod
pipelines:
  - kind: build
    pipelineRef: build-image
    label: Construction
    produces: [image]
`

	parsed, err := Parse([]byte(catalogue))
	if err != nil {
		t.Fatalf("un projet sans déployeur doit pouvoir déclarer ses environnements: %v", err)
	}
	for _, environment := range parsed.Environments {
		if len(environment.Deployer) != 0 {
			t.Errorf("environnement %q: réglages inventés %v", environment.ID, environment.Deployer)
		}
	}
}
