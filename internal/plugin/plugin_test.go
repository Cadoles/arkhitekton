package plugin

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// The registry covers every kind but pipelines, and nothing else. The test is about kinds, not
// about how many plugins exist: adding a plugin is routine, inventing a kind takes code and must
// be visible. Pipelines are described by capabilities the binary assembles, not by this registry.
func TestTheRegistryCoversEveryKind(t *testing.T) {
	seen := map[capability.Kind]bool{}
	ids := map[string]bool{}

	for _, descriptor := range Registry() {
		if ids[descriptor.ID] {
			t.Errorf("identifier %q declared twice", descriptor.ID)
		}
		ids[descriptor.ID] = true

		if len(descriptor.Capabilities) == 0 {
			t.Errorf("plugin %q brings no capability", descriptor.ID)
		}
		for _, header := range descriptor.Capabilities {
			if _, known := KindLabel[header.Kind]; !known {
				t.Errorf("plugin %q: kind %q with no label", descriptor.ID, header.Kind)
			}
			seen[header.Kind] = true
		}
		if descriptor.Label == "" || descriptor.Summary == "" {
			t.Errorf("plugin %q: with no label or no summary: the screen would not "+
				"know what to say about it", descriptor.ID)
		}
	}

	for _, kind := range []capability.Kind{
		capability.KindTracker, capability.KindForge, capability.KindExporter, capability.KindModule,
	} {
		if !seen[kind] {
			t.Errorf("no implementation for kind %q", kind)
		}
	}
}

// An unknown plugin names the ones that exist: reading the source is not an answer
// to give someone looking at an API message.
func TestAnUnknownPluginNamesThoseThatExist(t *testing.T) {
	if _, err := Get("redmine"); err != nil {
		t.Fatalf("redmine not found: %v", err)
	}

	_, err := Get("jira")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("error = %v, expected ErrUnknown", err)
	}
	if !strings.Contains(err.Error(), "redmine") {
		t.Errorf("message %q — it must name the embedded plugins", err)
	}
}

/*
 * Validation, and the two mistakes it catches.
 */

// A field the descriptor does not declare is refused, not ignored. An ignored
// field means a screen saying "saved" over a value nothing reads back, and the typo
// surfaces weeks later as a synchronisation that never did anything.
func TestAnUndeclaredSettingIsRefused(t *testing.T) {
	redmine, _ := Get("redmine")

	err := redmine.Validate(ScopeProject, Values{"project": "mse", "project_id": "mse"}, nil)
	if err == nil {
		t.Fatal("unknown setting accepted")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("message %q — it must name the offending setting", err)
	}
}

// A required field is checked against the resolved value, not the typed one. That
// is what separates "this project has nothing particular to say" from "the value is
// missing everywhere".
func TestTheRequirementBearsOnTheResolvedValue(t *testing.T) {
	redmine, _ := Get("redmine")

	// `project` is required at project scope, and nothing provides it.
	if err := redmine.Validate(ScopeProject, Values{}, nil); err == nil {
		t.Error("missing project identifier accepted")
	}

	// Provided by the wider scope: accepted, although nothing was typed.
	err := redmine.Validate(ScopeProject, Values{}, Values{"project": "mse"})
	if err != nil {
		t.Errorf("inherited value refused: %v", err)
	}
}

// Shapes are checked on input. A URL without a scheme is the real case: it saves,
// then every call fails with a message that does not point at it.
func TestFormatsAreCheckedOnInput(t *testing.T) {
	redmine, _ := Get("redmine")

	for _, value := range []string{"redmine.example.fr", "/redmine", "n'importe quoi"} {
		err := redmine.Validate(ScopeProject, Values{"project": "mse", "url": value}, nil)
		if err == nil {
			t.Errorf("URL %q accepted", value)
		}
	}

	if err := redmine.Validate(ScopeProject,
		Values{"project": "mse", "url": "https://redmine.example.fr"}, nil); err != nil {
		t.Errorf("valid URL refused: %v", err)
	}
}

// A plugin with no settings validates without demanding anything: it either
// applies or it does not.
func TestAPluginWithoutSettingsValidatesRequiringNothing(t *testing.T) {
	exporter, err := Get("bon-de-livraison")
	if err != nil {
		t.Fatalf("plugin not found: %v", err)
	}
	if !exporter.PerProject {
		t.Error("the delivery note must be decided per project")
	}
	if err := exporter.Validate(ScopeProject, Values{}, nil); err != nil {
		t.Errorf("sign-off: %v", err)
	}
}

/*
 * Layering the two scopes.
 */

// An empty project field does not erase the instance value. The trap it avoids: a
// form that does not display the API key — because it is secret — would send it back
// empty, and saving would wipe the instance key for every project.
func TestAnEmptyFieldDoesNotEraseTheInstances(t *testing.T) {
	resolved := Resolve(
		Values{"url": "https://redmine.example.fr", "api_key": "globale"},
		Values{"url": "", "project": "mse"},
	)

	if resolved["url"] != "https://redmine.example.fr" {
		t.Errorf("resolved URL = %q — emptiness does not erase", resolved["url"])
	}
	if resolved["api_key"] != "globale" {
		t.Errorf("resolved key = %q", resolved["api_key"])
	}
	if resolved["project"] != "mse" {
		t.Errorf("resolved project = %q", resolved["project"])
	}
}

// What the project declares wins: this is the second tenant, with its own server.
func TestTheProjectPrevailsOverTheInstance(t *testing.T) {
	resolved := Resolve(
		Values{"url": "https://redmine.example.fr", "api_key": "globale"},
		Values{"url": "https://redmine.autre-client.fr", "api_key": "own"},
	)

	if resolved["url"] != "https://redmine.autre-client.fr" {
		t.Errorf("resolved URL = %q", resolved["url"])
	}
	if resolved["api_key"] != "own" {
		t.Errorf("resolved key = %q", resolved["api_key"])
	}
}

// Secret fields are named per scope: that is what tells the store what to seal, and
// the API what to withhold.
func TestSensitiveFieldsAreNamedPerScope(t *testing.T) {
	redmine, _ := Get("redmine")

	for _, scope := range []Scope{ScopeGlobal, ScopeProject} {
		secrets := redmine.SecretFields(scope)
		if len(secrets) != 1 || secrets[0] != "api_key" {
			t.Errorf("scope %q: sensitive fields = %v", scope, secrets)
		}
		if !redmine.IsSecret(scope, "api_key") {
			t.Errorf("scope %q: the API key is not marked sensitive", scope)
		}
		if redmine.IsSecret(scope, "url") {
			t.Errorf("scope %q: the URL is marked sensitive", scope)
		}
	}
}

// List splitting lives in one place: two divergent splits would read the same
// configured status two ways.
func TestSplittingAListIgnoresSpacesAndEmptyItems(t *testing.T) {
	got := List(" En revue , Validé ,, ")
	if len(got) != 2 || got[0] != "En revue" || got[1] != "Validé" {
		t.Errorf("split = %q", got)
	}
	if items := List(""); len(items) != 0 {
		t.Errorf("empty list = %q", items)
	}
}
