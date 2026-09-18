package plugin

import (
	"errors"
	"strings"
	"testing"
)

// The registry covers every kind, and nothing else. The test is about kinds, not
// about how many plugins exist: adding a plugin is routine, inventing a kind takes
// code and must be visible.
func TestLeRegistreCouvreChaqueGenre(t *testing.T) {
	seen := map[Kind]bool{}
	ids := map[string]bool{}

	for _, descriptor := range Registry() {
		if ids[descriptor.ID] {
			t.Errorf("identifiant %q déclaré deux fois", descriptor.ID)
		}
		ids[descriptor.ID] = true

		if _, known := KindLabel[descriptor.Kind]; !known {
			t.Errorf("plugin %q: genre %q sans libellé", descriptor.ID, descriptor.Kind)
		}
		if descriptor.Label == "" || descriptor.Summary == "" {
			t.Errorf("plugin %q: sans libellé ou sans résumé — l'écran ne saurait "+
				"pas quoi en dire", descriptor.ID)
		}
		seen[descriptor.Kind] = true
	}

	for _, kind := range []Kind{KindTracker, KindForge, KindExporter, KindModule} {
		if !seen[kind] {
			t.Errorf("aucune réalisation pour le genre %q", kind)
		}
	}
}

// An unknown plugin names the ones that exist: reading the source is not an answer
// to give someone looking at an API message.
func TestUnPluginInconnuNommeCeuxQuiExistent(t *testing.T) {
	if _, err := Get("redmine"); err != nil {
		t.Fatalf("redmine introuvable: %v", err)
	}

	_, err := Get("jira")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("erreur = %v, attendu ErrUnknown", err)
	}
	if !strings.Contains(err.Error(), "redmine") {
		t.Errorf("message %q — il doit nommer les plugins embarqués", err)
	}
}

/*
 * Validation, and the two mistakes it catches.
 */

// A field the descriptor does not declare is refused, not ignored. An ignored
// field means a screen saying "saved" over a value nothing reads back, and the typo
// surfaces weeks later as a synchronisation that never did anything.
func TestUnReglageNonDeclareEstRefuse(t *testing.T) {
	redmine, _ := Get("redmine")

	err := redmine.Validate(ScopeProject, Values{"project": "mse", "projet": "mse"}, nil)
	if err == nil {
		t.Fatal("réglage inconnu accepté")
	}
	if !strings.Contains(err.Error(), "projet") {
		t.Errorf("message %q — il doit nommer le réglage fautif", err)
	}
}

// A required field is checked against the resolved value, not the typed one. That
// is what separates "this project has nothing particular to say" from "the value is
// missing everywhere".
func TestLObligationPorteSurLaValeurResolue(t *testing.T) {
	redmine, _ := Get("redmine")

	// `project` is required at project scope, and nothing provides it.
	if err := redmine.Validate(ScopeProject, Values{}, nil); err == nil {
		t.Error("identifiant de projet manquant accepté")
	}

	// Provided by the wider scope: accepted, although nothing was typed.
	err := redmine.Validate(ScopeProject, Values{}, Values{"project": "mse"})
	if err != nil {
		t.Errorf("valeur héritée refusée: %v", err)
	}
}

// Shapes are checked on input. A URL without a scheme is the real case: it saves,
// then every call fails with a message that does not point at it.
func TestLesFormesSeVerifientALaSaisie(t *testing.T) {
	redmine, _ := Get("redmine")

	for _, value := range []string{"redmine.example.fr", "/redmine", "n'importe quoi"} {
		err := redmine.Validate(ScopeProject, Values{"project": "mse", "url": value}, nil)
		if err == nil {
			t.Errorf("URL %q acceptée", value)
		}
	}

	if err := redmine.Validate(ScopeProject,
		Values{"project": "mse", "url": "https://redmine.example.fr"}, nil); err != nil {
		t.Errorf("URL valide refusée: %v", err)
	}
}

// A plugin with no settings validates without demanding anything: it either
// applies or it does not.
func TestUnPluginSansReglageSeValideSansRienExiger(t *testing.T) {
	exporter, err := Get("bon-de-livraison")
	if err != nil {
		t.Fatalf("plugin introuvable: %v", err)
	}
	if !exporter.PerProject {
		t.Error("le bon de livraison doit se décider par projet")
	}
	if err := exporter.Validate(ScopeProject, Values{}, nil); err != nil {
		t.Errorf("validation: %v", err)
	}
}

/*
 * Layering the two scopes.
 */

// An empty project field does not erase the instance value. The trap it avoids: a
// form that does not display the API key — because it is secret — would send it back
// empty, and saving would wipe the instance key for every project.
func TestUnChampVideNEffacePasCeluiDeLInstance(t *testing.T) {
	resolved := Resolve(
		Values{"url": "https://redmine.example.fr", "api_key": "globale"},
		Values{"url": "", "project": "mse"},
	)

	if resolved["url"] != "https://redmine.example.fr" {
		t.Errorf("URL résolue = %q — le vide n'efface pas", resolved["url"])
	}
	if resolved["api_key"] != "globale" {
		t.Errorf("clé résolue = %q", resolved["api_key"])
	}
	if resolved["project"] != "mse" {
		t.Errorf("projet résolu = %q", resolved["project"])
	}
}

// What the project declares wins: this is the second tenant, with its own server.
func TestLeProjetLEmporteSurLInstance(t *testing.T) {
	resolved := Resolve(
		Values{"url": "https://redmine.example.fr", "api_key": "globale"},
		Values{"url": "https://redmine.autre-client.fr", "api_key": "propre"},
	)

	if resolved["url"] != "https://redmine.autre-client.fr" {
		t.Errorf("URL résolue = %q", resolved["url"])
	}
	if resolved["api_key"] != "propre" {
		t.Errorf("clé résolue = %q", resolved["api_key"])
	}
}

// Secret fields are named per scope: that is what tells the store what to seal, and
// the API what to withhold.
func TestLesChampsSensiblesSeNommentParPortee(t *testing.T) {
	redmine, _ := Get("redmine")

	for _, scope := range []Scope{ScopeGlobal, ScopeProject} {
		secrets := redmine.SecretFields(scope)
		if len(secrets) != 1 || secrets[0] != "api_key" {
			t.Errorf("portée %q: champs sensibles = %v", scope, secrets)
		}
		if !redmine.IsSecret(scope, "api_key") {
			t.Errorf("portée %q: la clé d'API n'est pas marquée sensible", scope)
		}
		if redmine.IsSecret(scope, "url") {
			t.Errorf("portée %q: l'URL est marquée sensible", scope)
		}
	}
}

// List splitting lives in one place: two divergent splits would read the same
// configured status two ways.
func TestLeDecoupageDUneListeIgnoreLesEspacesEtLeVide(t *testing.T) {
	got := List(" En revue , Validé ,, ")
	if len(got) != 2 || got[0] != "En revue" || got[1] != "Validé" {
		t.Errorf("découpage = %q", got)
	}
	if items := List(""); len(items) != 0 {
		t.Errorf("liste vide = %q", items)
	}
}
