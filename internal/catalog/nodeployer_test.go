package catalog

import (
	"strings"
	"testing"
)

// A catalogue with no deployer settings at all must be valid.
//
// `promotionSuffix` used to be required, and its absence failed validation: the
// catalogue imposed one deployment tool on whoever had none. Run through Parse,
// which is the real path, strict decoding included.
func TestACatalogueWithoutDeployerSettingsIsValid(t *testing.T) {
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
		t.Fatalf("a project with no deployer must be able to declare its environments: %v", err)
	}
	for _, environment := range parsed.EnvironmentsOf("recette-project") {
		if len(environment.Deployer) != 0 {
			t.Errorf("environment %q: invented settings %v", environment.ID, environment.Deployer)
		}
	}
}

// Ranks and identifiers are unique within a project: two projects may each have a
// `qualif` of rank 2.
func TestEnvironmentsAreValidatedPerProject(t *testing.T) {
	const sharedItems = `
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
	if _, err := Parse([]byte(sharedItems)); err != nil {
		t.Errorf("two projects with the same environment: %v", err)
	}

	const duplicates = `
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
	_, err := Parse([]byte(duplicates))
	if err == nil {
		t.Fatal("a duplicated identifier and rank within a project must be refused")
	}
	for _, expected := range []string{`"qualif" declared twice`, "share rank 2"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("error %q — it must say %q", err, expected)
		}
	}
}
