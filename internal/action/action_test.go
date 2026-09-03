package action

import (
	"errors"
	"strings"
	"testing"
)

// Le registre se lit, et chaque action se décrit assez pour qu'un éditeur en
// compose le formulaire.
func TestChaqueActionSeDecritAssezPourEtreEditee(t *testing.T) {
	ids := map[string]bool{}
	for _, descriptor := range Registry() {
		if ids[descriptor.ID] {
			t.Errorf("identifiant %q déclaré deux fois", descriptor.ID)
		}
		ids[descriptor.ID] = true

		if descriptor.Label == "" || descriptor.Summary == "" {
			t.Errorf("action %q sans libellé ou sans résumé", descriptor.ID)
		}
		for _, field := range descriptor.Fields {
			if field.Name == "" || field.Label == "" {
				t.Errorf("action %q: champ mal déclaré %+v", descriptor.ID, field)
			}
		}
	}

	if !ids[Webhook] || !ids[ForgeStatus] {
		t.Errorf("actions déclarées = %v", ids)
	}
}

// Une action inconnue nomme celles qui existent.
func TestUneActionInconnueNommeCellesQuiExistent(t *testing.T) {
	_, err := Get("slack.post")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("%v, attendu ErrUnknown", err)
	}
	if !strings.Contains(err.Error(), Webhook) {
		t.Errorf("message %q — il doit nommer les actions internes", err)
	}
}

/*
 * La validation, faite à l'enregistrement plutôt qu'à l'exécution.
 */

// Un paramètre requis manquant est refusé, et nommé.
func TestUnParametreRequisManquantEstRefuse(t *testing.T) {
	webhook, _ := Get(Webhook)

	err := webhook.Validate(map[string]string{"body": `{"tag":"{tag}"}`})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, attendu ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "URL") {
		t.Errorf("message %q — il doit nommer le champ manquant", err)
	}
}

// Un paramètre que l'action ne déclare pas est refusé plutôt qu'ignoré : ignoré,
// il donne un écran qui dit « enregistré » sur une valeur que rien ne relira.
func TestUnParametreInconnuEstRefuse(t *testing.T) {
	webhook, _ := Get(Webhook)

	err := webhook.Validate(map[string]string{
		"url": "https://hooks.example.fr/x", "methode": "PUT",
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "methode") {
		t.Errorf("%v", err)
	}
}

// La forme de l'URL est vérifiée ici ; sa **cible** l'est au moment de connecter,
// par le transport. Les deux ne se remplacent pas : celui-ci attrape une faute de
// frappe, le second attrape une falsification.
func TestLaFormeDeLURLEstVerifieeALaSaisie(t *testing.T) {
	webhook, _ := Get(Webhook)

	for _, cible := range []string{"hooks.example.fr", "/hook", "file:///etc/passwd"} {
		if err := webhook.Validate(map[string]string{"url": cible}); !errors.Is(err, ErrInvalid) {
			t.Errorf("URL %q acceptée: %v", cible, err)
		}
	}

	if err := webhook.Validate(map[string]string{
		"url": "https://hooks.example.fr/services/abc",
	}); err != nil {
		t.Errorf("URL valide refusée: %v", err)
	}
}

// L'état d'un statut de commit est un jeu fermé côté forge : une valeur inventée
// serait refusée par elle, au moment le moins utile.
func TestUnEtatDeStatutInventeEstRefuse(t *testing.T) {
	status, _ := Get(ForgeStatus)

	err := status.Validate(map[string]string{"state": "vert", "context": "dashboard"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "success") {
		t.Errorf("message %q — il doit nommer les états admis", err)
	}

	for _, state := range StatusStates {
		if err := status.Validate(map[string]string{
			"state": state, "context": "dashboard",
		}); err != nil {
			t.Errorf("état %q refusé: %v", state, err)
		}
	}
}

// Un gabarit dont une accolade n'est pas fermée produirait une valeur littérale
// incompréhensible chez le destinataire.
func TestUnGabaritMalAppariéEstRefuse(t *testing.T) {
	webhook, _ := Get(Webhook)

	err := webhook.Validate(map[string]string{
		"url": "https://hooks.example.fr/x", "body": `{"tag":"{tag"}`,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

/*
 * L'interpolation.
 */

// Les variables connues sont remplacées par la charge du fait.
func TestLesVariablesConnuesSontRemplacees(t *testing.T) {
	got := Expand(`{"release":"{tag}","issue":"{outcome}"}`, map[string]string{
		"tag": "1.8.0-rc1", "outcome": "succeeded",
	})

	want := `{"release":"1.8.0-rc1","issue":"succeeded"}`
	if got != want {
		t.Errorf("gabarit rendu = %q", got)
	}
}

// **Une variable inconnue reste littérale.**
//
// Un `{tag}` visible dans un message de messagerie se remarque et se corrige,
// là qu'un trou silencieux se lit comme une release sans nom — et personne ne
// saurait dire si c'est le gabarit ou la donnée qui manque.
func TestUneVariableInconnueResteLitterale(t *testing.T) {
	got := Expand("release {tag} en {environnement}", map[string]string{"tag": "1.8.0"})

	if got != "release 1.8.0 en {environnement}" {
		t.Errorf("gabarit rendu = %q", got)
	}
}

// Une accolade ouverte sans fermeture ne fait pas boucler l'interpolation, et
// ressort telle quelle.
func TestUneAccoladeOuverteNeFaitPasBoucler(t *testing.T) {
	for _, gabarit := range []string{"a {b", "{", "}", "{}", "{a}{", ""} {
		got := Expand(gabarit, map[string]string{"a": "x"})
		if got == "" && gabarit != "" {
			t.Errorf("gabarit %q rendu vide", gabarit)
		}
	}

	// Le cas nominal reste juste au milieu du bruit.
	if got := Expand("{a}{", map[string]string{"a": "x"}); got != "x{" {
		t.Errorf("gabarit rendu = %q", got)
	}
}

// Un gabarit sans variable traverse inchangé, sans allocation inutile.
func TestUnGabaritSansVariableTraverseInchange(t *testing.T) {
	if got := Expand("texte simple", map[string]string{"a": "x"}); got != "texte simple" {
		t.Errorf("gabarit rendu = %q", got)
	}
}

// Le cas principal, et celui qui a été trouvé de travers : un corps JSON.
//
// Un balayage naïf — première accolade ouvrante, première fermante — faisait de
// `{"release":"{tag}"` un nom de variable, avalait la vraie et rendait le gabarit
// littéral. Ce n'est pas un cas tordu : c'est **la** forme d'un corps de webhook.
func TestUnCorpsJSONNestPasAvaleParSesAccolades(t *testing.T) {
	got := Expand(`{"text":"release {tag} : {outcome}","nested":{"a":"{tag}"}}`,
		map[string]string{"tag": "1.8.0-rc1", "outcome": "succeeded"})

	want := `{"text":"release 1.8.0-rc1 : succeeded","nested":{"a":"1.8.0-rc1"}}`
	if got != want {
		t.Errorf("gabarit rendu = %q\nattendu           = %q", got, want)
	}
}

// Ce qui ressemble à une structure et non à un nom traverse tel quel.
func TestCeQuiNestPasUnNomTraverseInchange(t *testing.T) {
	cas := map[string]string{
		`{}`:                `{}`,
		`{ }`:               `{ }`,
		`{"a":1}`:           `{"a":1}`,
		`{a b}`:             `{a b}`,
		`{tag with spaces}`: `{tag with spaces}`,
	}

	for gabarit, want := range cas {
		if got := Expand(gabarit, map[string]string{"tag": "x"}); got != want {
			t.Errorf("gabarit %q rendu %q, attendu %q", gabarit, got, want)
		}
	}
}
