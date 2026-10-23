package hosttest

import (
	"os"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// A catalogue binding an extension's capability loads with the extension, and only with it.
func TestACatalogueLoadsWithItsExtensions(t *testing.T) {
	path := t.TempDir() + "/catalog.yaml"
	if err := writeFile(path, "capabilities:\n  - ref: widgets/check\n"); err != nil {
		t.Fatal(err)
	}
	widgets := host.Extension{ID: "widgets", Plugin: capability.Plugin{
		ID: "widgets", Version: "1", APIVersion: sdk.APIVersion, Label: "Widgets",
		Pipelines: []capability.Pipeline{{Name: "check", Label: "Check", Spec: capability.PipelineSpec{
			PipelineRef: "widgets-check", Produces: []capability.Product{capability.ProductNone},
		}}},
	}}
	if _, found := Catalogue(t, path, widgets).Find("widgets/check"); !found {
		t.Error("the extension's capability is not in the catalogue")
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }
