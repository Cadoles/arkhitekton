package action

import (
	"errors"
	"maps"
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

	if !ids[Webhook] {
		t.Errorf("actions déclarées = %v", ids)
	}
}

// Le registre s'ouvre dans l'autre sens : un plugin déclare ses gestes comme il
// déclare ses faits, et l'appelant ne voit qu'une liste.
//
// Ce que ce test tient est la jonction : sans elle, on branche n'importe quelle
// cause sur un jeu de conséquences figé.
func TestLesActionsDUnPluginRejoignentLeRegistre(t *testing.T) {
	found := false
	for _, descriptor := range All() {
		if descriptor.Plugin == "" {
			continue
		}
		found = true
		// Préfixée par son plugin : deux forges déclarant `status` produiraient
		// sinon des gestes indiscernables.
		if !strings.HasPrefix(descriptor.ID, descriptor.Plugin+".") {
			t.Errorf("action %q du plugin %q: référence non préfixée",
				descriptor.ID, descriptor.Plugin)
		}
		// Aucun champ sensible, et c'est structurel : une action de plugin parle à
		// sa propre frontière, déjà réglée. Elle ne désigne aucun jeton, et ne peut
		// donc pas servir à en faire sortir un vers une cible saisie ailleurs.
		for _, field := range descriptor.Fields {
			if field.SecretRef {
				t.Errorf("action %q: le champ %q désigne un secret",
					descriptor.ID, field.Name)
			}
		}
	}
	if !found {
		t.Fatal("aucun plugin ne déclare d'action — la jonction n'est pas branchée")
	}

	// Résolue par le registre réuni, sans que l'appelant ait à savoir d'où elle
	// vient.
	descriptor, err := Get("gitea.status")
	if err != nil {
		t.Fatalf("gitea.status: %v", err)
	}
	if descriptor.Plugin != "gitea" {
		t.Errorf("plugin = %q", descriptor.Plugin)
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

// Un jeu fermé se vérifie ici plutôt que chez le destinataire : une valeur
// inventée serait refusée par la forge, au moment le moins utile.
//
// La liste vient du descripteur du plugin et non d'une condition sur
// l'identifiant : c'est ce qui permettra à une seconde forge d'avoir ses propres
// états sans modifier ce paquet.
func TestUneValeurHorsDuJeuFermeEstRefusee(t *testing.T) {
	status, err := Get("gitea.status")
	if err != nil {
		t.Fatalf("gitea.status: %v", err)
	}

	base := map[string]string{"commit": "abc1234", "context": "dashboard"}
	invalide := maps.Clone(base)
	invalide["state"] = "vert"

	err = status.Validate(invalide)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "success") {
		t.Errorf("message %q — il doit nommer les états admis", err)
	}

	for _, state := range []string{"pending", "success", "failure", "error"} {
		params := maps.Clone(base)
		params["state"] = state
		if err := status.Validate(params); err != nil {
			t.Errorf("état %q refusé: %v", state, err)
		}
	}
}

// Un jeu fermé ne doit pas interdire le gabarit qui le remplira.
//
// Sans cette exception, un état ne pourrait s'écrire qu'en dur, et une réaction
// qui veut refléter l'issue du fait déclencheur deviendrait deux réactions.
func TestUnGabaritTraverseUnJeuFerme(t *testing.T) {
	status, _ := Get("gitea.status")

	if err := status.Validate(map[string]string{
		"commit": "{git_commit}", "state": "{outcome}", "context": "dashboard",
	}); err != nil {
		t.Errorf("gabarit refusé sur un champ à jeu fermé: %v", err)
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
