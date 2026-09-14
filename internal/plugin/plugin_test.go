package plugin

import (
	"errors"
	"strings"
	"testing"
)

// Le registre couvre chaque genre de plugin, et rien d'autre.
//
// Le test porte sur les **genres** et non sur le nombre de plugins : ajouter un
// plugin est une évolution normale, inventer un genre demande du code et doit se
// voir.
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

// Un plugin inconnu nomme ceux qui existent : chercher dans le code source n'est
// pas une réponse à donner à qui lit un message d'API.
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
 * La validation, et les deux erreurs qu'elle attrape.
 */

// Un champ que le descripteur ne déclare pas est refusé, et non ignoré.
//
// Un champ ignoré donne un écran qui dit « enregistré » sur une valeur que rien
// ne relira : la faute de frappe se découvre des semaines plus tard, quand la
// synchronisation n'a jamais rien fait.
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

// L'obligation porte sur la valeur **résolue**, pas sur la saisie.
//
// C'est ce qui distingue « le projet n'a rien de particulier à dire » de « la
// valeur manque partout ». Les confondre refuserait un projet parfaitement
// configuré, dont l'instance fournit déjà l'URL.
func TestLObligationPorteSurLaValeurResolue(t *testing.T) {
	redmine, _ := Get("redmine")

	// `project` est obligatoire au niveau du projet, et rien ne le fournit.
	if err := redmine.Validate(ScopeProject, Values{}, nil); err == nil {
		t.Error("identifiant de projet manquant accepté")
	}

	// Fourni par la portée supérieure : accepté, bien que la saisie soit vide.
	err := redmine.Validate(ScopeProject, Values{}, Values{"project": "mse"})
	if err != nil {
		t.Errorf("valeur héritée refusée: %v", err)
	}
}

// Les formes se vérifient à la saisie. Une URL sans schéma est le cas réel : elle
// s'enregistre, puis chaque appel échoue avec un message qui ne la désigne pas.
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

// Un plugin sans réglage se valide sans rien exiger : le bon de livraison ne se
// configure pas, il s'applique ou non.
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
 * La superposition des portées.
 */

// Un champ vide au niveau d'un projet n'efface pas celui de l'instance.
//
// Le piège que cela évite : un formulaire qui n'affiche pas la clé d'API — parce
// qu'elle est secrète — la renverrait vide, et l'enregistrement effacerait la clé
// de l'instance pour tous les projets.
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

// Ce que le projet déclare l'emporte : c'est le second client, avec son serveur.
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

// Les champs sensibles sont nommés par portée : c'est ce qui dit au magasin quoi
// sceller, et à l'API quoi taire.
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

// Le découpage d'une liste est rendu une seule fois : deux découpages divergents
// donneraient deux lectures d'un statut nommé « En revue ».
func TestLeDecoupageDUneListeIgnoreLesEspacesEtLeVide(t *testing.T) {
	got := List(" En revue , Validé ,, ")
	if len(got) != 2 || got[0] != "En revue" || got[1] != "Validé" {
		t.Errorf("découpage = %q", got)
	}
	if items := List(""); len(items) != 0 {
		t.Errorf("liste vide = %q", items)
	}
}
