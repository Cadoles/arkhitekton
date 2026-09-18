package catalog

import "testing"

// The catalogue actually deployed must load.
//
// It is edited by hand, in a long file, and a misspelled `suggest` has no visible
// consequence before the binary starts in the cluster. Reading it here fails in
// the right place.
func TestTheDeployedCatalogueLoads(t *testing.T) {
	catalog, err := Load("../../deploy/base/config/catalog.yaml")
	if err != nil {
		t.Fatalf("deployed catalogue: %v", err)
	}

	for _, pipeline := range catalog.Pipelines {
		for _, input := range pipeline.Inputs() {
			// A typed parameter with no declared nature is not a mistake, but it is one more
			// empty field in the form. Reporting it keeps the question open.
			if input.Suggest == "" {
				t.Logf("%s/%s: no suggestion declared", pipeline.Kind, input.Name)
			}
		}
	}
}
