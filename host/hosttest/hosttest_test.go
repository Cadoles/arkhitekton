package hosttest

import (
	"os"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// A catalogue binding an installed plugin's capability loads with the plugin.
func TestACatalogueLoadsWithItsPlugins(t *testing.T) {
	path := t.TempDir() + "/catalog.yaml"
	if err := writeFile(path, "capabilities:\n  - ref: widgets/check\n"); err != nil {
		t.Fatal(err)
	}
	widgets := capability.Plugin{
		ID: "widgets", Version: "1", APIVersion: sdk.APIVersion, Label: "Widgets",
		Pipelines: []capability.Pipeline{{Name: "check", Label: "Check", Spec: capability.PipelineSpec{
			PipelineRef: "widgets-check", Produces: []capability.Product{capability.ProductNone},
		}}},
	}
	if _, found := Catalogue(t, path, widgets).Find("widgets/check"); !found {
		t.Error("the plugin's capability is not in the catalogue")
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }

func TestAPluginsInterfaceComposesWithTheCores(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"arkhitekton-plugin.yaml": "apiVersion: arkhitekton.plugin/v2\nid: widgets\nversion: 1.0.0\nsdk: \"1\"\nlabel: Widgets\n" +
			"binary: bin/plugin\nprotocol: 1\nui: { resources: [ui/*.yaml] }\n",
		"ui/menu.yaml": "apiVersion: ui.arkhitekton/v1\nkind: Navigation\nmetadata: { name: main }\n" +
			"spec: { items: [{ id: widgets.home, label: Widgets, to: /widgets }] }\n",
	} {
		if err := os.MkdirAll(dir+"/ui", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(dir+"/"+name, content); err != nil {
			t.Fatal(err)
		}
	}
	items := Interface(t, dir).Navigations["main"].Items
	if last := items[len(items)-1]; last.ID != "widgets.home" {
		t.Errorf("items = %+v", items)
	}
}
