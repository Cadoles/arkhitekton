package catalog

import (
	"os"
	"testing"
)

// The shipped catalogue must stay parseable: it is mounted as a ConfigMap, and a
// typo would only show when a pod starts.
func TestShippedCatalogParses(t *testing.T) {
	path := "../../deploy/base/config/catalog.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skip("shipped catalogue not found")
	}

	catalog, err := Load(path)
	if err != nil {
		t.Fatalf("the shipped catalogue is invalid: %v", err)
	}

	if len(catalog.Pipelines) == 0 {
		t.Error("no pipeline declared")
	}
	environments := catalog.EnvironmentsOf("mse-test-project")
	if len(environments) != 4 {
		t.Errorf("%d environment(s) for mse-test-project, expected 4", len(environments))
	}

	// Checked against the file actually shipped: a GitOps controller may know an
	// environment under another name, and getting it wrong promotes to nothing.
	var qualif *Environment
	for i := range environments {
		if environments[i].ID == "qualif" {
			qualif = &environments[i]
		}
	}
	if qualif == nil {
		t.Fatal("environment qualif is missing")
	}
	// The promotion suffix lives under `deployer:`: the catalogue carries it without
	// reading it, and only the deployer names it.
	if qualif.Deployer["promotionSuffix"] != "testing" {
		t.Errorf("qualif suffix = %q, expected testing",
			qualif.Deployer["promotionSuffix"])
	}

	for _, environment := range environments {
		if environment.ID == "prod" {
			if environment.Deployer["acceptsPrerelease"] != "false" {
				t.Error("production must not accept pre-releases")
			}
			if environment.IsObservable() {
				t.Error("production runs in a separate cluster: not observable")
			}
		}
	}
}
