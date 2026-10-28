package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

func pluginWithInterface(t *testing.T, resources string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"arkhitekton-plugin.yaml": "apiVersion: arkhitekton.plugin/v2\nid: widgets\nversion: 1.0.0\nsdk: \"1\"\nlabel: Widgets\n" +
			"binary: bin/plugin\nprotocol: 1\nui: { resources: [ui/*.yaml] }\n",
		"ui/screens.yaml": resources,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckUISaysWhetherAnInterfaceComposes(t *testing.T) {
	composes := pluginWithInterface(t, "apiVersion: ui.arkhitekton/v1\nkind: Navigation\nmetadata: { name: main }\n"+
		"spec: { items: [{ id: widgets.home, after: core.releases, label: Widgets, to: /widgets }] }\n")
	if err := runCheckUI([]string{"--plugin", composes}); err != nil {
		t.Errorf("a composing interface: %v", err)
	}
	refused := pluginWithInterface(t, "apiVersion: ui.arkhitekton/v1\nkind: Navigation\nmetadata: { name: main }\n"+
		"spec: { items: [{ id: core.versions, label: Mine, to: /mine }] }\n")
	if err := runCheckUI([]string{"--plugin", refused}); err == nil || !strings.Contains(err.Error(), "core.versions") {
		t.Errorf("a conflicting interface: %v", err)
	}
}

// A plugin of the retired module contract brings a script and no resource: installing it would show
// none of its screens, so it is refused with the reason.
func TestAPluginOfTheRetiredModuleContractIsRefused(t *testing.T) {
	plugin := installed.Plugin{Native: &installed.Native{Manifest: manifest.Manifest{UI: &manifest.UI{Entry: "ui/index.js"}}}}
	err := (&capabilitySources{}).checkInterface(context.Background(), plugin)
	if err == nil || !strings.Contains(err.Error(), "ui.resources") {
		t.Errorf("err = %v", err)
	}
}
