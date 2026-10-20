package builtin

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// The built-in plugins cover every kind, and each says what it is. The test is about kinds, not
// about how many plugins exist: adding a plugin is routine, inventing a kind takes code and must
// be visible.
func TestTheBuiltInPluginsCoverEveryKind(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatal(err)
	}

	seen := map[capability.Kind]bool{}
	for _, plugin := range registry.Plugins() {
		if len(plugin.Headers()) == 0 {
			t.Errorf("plugin %q brings no capability", plugin.ID)
		}
		for _, header := range plugin.Headers() {
			seen[header.Kind] = true
		}
		if plugin.Label == "" || plugin.Summary == "" {
			t.Errorf("plugin %q: with no label or no summary: the screen would not "+
				"know what to say about it", plugin.ID)
		}
	}
	for _, kind := range []capability.Kind{
		capability.KindPipeline, capability.KindTracker, capability.KindForge,
		capability.KindExporter, capability.KindModule,
	} {
		if !seen[kind] {
			t.Errorf("no plugin brings kind %q", kind)
		}
	}
}

// An unknown plugin names the ones that exist: reading the source is not an answer to give
// someone looking at an API message.
func TestAnUnknownPluginNamesThoseThatExist(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Plugin("redmine"); err != nil {
		t.Fatalf("redmine not found: %v", err)
	}
	_, err = registry.Plugin("jira")
	if !errors.Is(err, capabilities.ErrUnknown) {
		t.Fatalf("error = %v, expected ErrUnknown", err)
	}
	if !strings.Contains(err.Error(), "redmine") {
		t.Errorf("message %q: it must name the embedded plugins", err)
	}
}

// An action is found by its prefixed reference, from the plugin that declares it.
func TestAnActionIsFoundByItsReference(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	plugin, spec, found := registry.FindAction("gitea.status")
	if !found || plugin.ID != "gitea" || spec.Suffix != "status" {
		t.Errorf("gitea.status: %v %q %q", found, plugin.ID, spec.Suffix)
	}
	if _, _, found := registry.FindAction("forge.status"); found {
		t.Error("an unprefixed reference resolved")
	}
}

// The delivery note is decided per project: it used to be offered on every release.
func TestTheDeliveryNoteIsDecidedPerProject(t *testing.T) {
	registry, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := registry.Plugin("bon-de-livraison")
	if err != nil {
		t.Fatal(err)
	}
	if !exporter.PerProject {
		t.Error("the delivery note must be decided per project")
	}
}
