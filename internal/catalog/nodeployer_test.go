package catalog

import (
	"strings"
	"testing"
)

// Un catalogue sans le moindre réglage de déployeur doit être valide.
//
// `promotionSuffix` était requis, et son absence faisait échouer la validation :
// le catalogue imposait Flux à qui n'en avait pas. Passé par `Parse` : c'est le
// chemin réel, décodage strict compris.
func TestUnCatalogueSansReglageDeDeployeurEstValide(t *testing.T) {
	const catalogue = `
maxConcurrentRuns: 1
projects:
  recette-project:
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
	for _, environment := range parsed.EnvironmentsOf("recette-project") {
		if len(environment.Deployer) != 0 {
			t.Errorf("environnement %q: réglages inventés %v", environment.ID, environment.Deployer)
		}
	}
}

// Les rangs et les identifiants sont uniques **dans un projet** : deux projets
// peuvent chacun avoir un `qualif` de rang 2.
func TestLesEnvironnementsSeValidentParProjet(t *testing.T) {
	const partages = `
maxConcurrentRuns: 1
projects:
  mse:
    environments:
      - {id: qualif, label: Qualification, rank: 2, namespace: mse-dev}
  themes:
    environments:
      - {id: qualif, label: Qualification, rank: 2, namespace: themes-dev}
pipelines:
  - {kind: build, pipelineRef: build-image, label: Construction, produces: [image]}
`
	if _, err := Parse([]byte(partages)); err != nil {
		t.Errorf("deux projets avec le même environnement: %v", err)
	}

	const doublons = `
maxConcurrentRuns: 1
projects:
  mse:
    environments:
      - {id: qualif, label: Qualification, rank: 2, namespace: mse-dev}
      - {id: qualif, label: Encore, rank: 3, namespace: mse-dev}
      - {id: prod, label: Production, rank: 2, namespace: mse-prod}
pipelines:
  - {kind: build, pipelineRef: build-image, label: Construction, produces: [image]}
`
	_, err := Parse([]byte(doublons))
	if err == nil {
		t.Fatal("un identifiant et un rang dupliqués dans un projet doivent être refusés")
	}
	for _, attendu := range []string{`"qualif" déclaré deux fois`, "partagent le rang 2"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("erreur %q — elle doit dire %q", err, attendu)
		}
	}
}
