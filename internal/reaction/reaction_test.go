package reaction

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

func planDeTest() plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{
		{Kind: plan.GestePipeline, Ref: "unit-tests"},
	}}}}
}

func sujet(match map[string]string) Reaction {
	return Reaction{
		Name: "Prévenir l'équipe", EventType: "release.created",
		Enabled: true, Match: match, Plan: planDeTest(),
	}
}

/*
 * L'appariement.
 */

// Toutes les clés déclarées doivent correspondre : un appariement est une
// conjonction.
func TestUnAppariementEstUneConjonction(t *testing.T) {
	sujet := sujet(map[string]string{"kind": "final", "outcome": "succeeded"})

	if !sujet.Matches(map[string]string{"kind": "final", "outcome": "succeeded", "tag": "1.8.0"}) {
		t.Error("une charge qui satisfait tout n'apparie pas")
	}
	if sujet.Matches(map[string]string{"kind": "final", "outcome": "failed"}) {
		t.Error("une charge qui ne satisfait qu'une clé apparie")
	}
}

// L'appartenance s'écrit avec des virgules : `preprod,production` se saisit dans
// un champ de texte et se relit sans documentation.
func TestLAppartenanceSecritAvecDesVirgules(t *testing.T) {
	sujet := sujet(map[string]string{"environment": "preprod, production"})

	for _, valeur := range []string{"preprod", "production"} {
		if !sujet.Matches(map[string]string{"environment": valeur}) {
			t.Errorf("environnement %q n'apparie pas", valeur)
		}
	}
	if sujet.Matches(map[string]string{"environment": "qualif"}) {
		t.Error("un environnement hors de la liste apparie")
	}
}

// **Une clé absente de la charge n'apparie pas.**
//
// Le contraire ferait partir une réaction sur un fait qui ne porte pas ce dont
// elle parle — le cas exact d'un `environment` attendu sur un fait qui n'en a
// aucun.
func TestUneCleAbsenteDeLaChargeNapariePas(t *testing.T) {
	sujet := sujet(map[string]string{"environment": "production"})

	if sujet.Matches(map[string]string{"tag": "1.8.0"}) {
		t.Error("une charge sans le champ apparié a déclenché la réaction")
	}
}

// Un appariement vide part sur tout fait de ce type : c'est la déclaration la
// plus simple, et elle doit rester possible.
func TestUnAppariementVidePartSurToutFaitDeCeType(t *testing.T) {
	if !sujet(nil).Matches(map[string]string{"n'importe": "quoi"}) {
		t.Error("un appariement vide n'apparie pas")
	}
	if !sujet(map[string]string{}).Matches(nil) {
		t.Error("un appariement vide n'apparie pas une charge vide")
	}
}

/*
 * La validation, faite à l'enregistrement.
 */

// Une réaction sans nom est refusée : c'est par lui qu'on la retrouve dans une
// liste, et « réaction 7 » n'apprend rien.
func TestUneReactionSansNomEstRefusee(t *testing.T) {
	sujet := sujet(nil)
	sujet.Name = "  "

	err := sujet.Validate()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "nom") {
		t.Errorf("%v", err)
	}
}

// Une réaction qui n'écoute aucun fait est refusée.
func TestUneReactionSansFaitEcouteEstRefusee(t *testing.T) {
	sujet := sujet(nil)
	sujet.EventType = ""

	if err := sujet.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

// Un appariement sans valeur est refusé : il apparierait un champ absent, ce que
// la règle refuse par ailleurs, et donnerait une réaction qui ne part jamais sans
// qu'on sache pourquoi.
func TestUnAppariementSansValeurEstRefuse(t *testing.T) {
	err := sujet(map[string]string{"kind": "  "}).Validate()

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "kind") {
		t.Errorf("%v", err)
	}
}

// Le plan est validé avec la réaction : une réaction dont le plan ne tient pas
// n'a pas de sens, et laisser passer reporterait le diagnostic au moment du
// déclenchement.
func TestLePlanEstValideAvecLaReaction(t *testing.T) {
	sujet := sujet(nil)
	sujet.Plan = plan.Plan{Stages: []plan.Stage{{}}}

	if err := sujet.Validate(); err == nil {
		t.Error("une réaction au plan invalide a été acceptée")
	}
}

/*
 * Ce que la couche au-dessus doit vérifier.
 */

// Les références sont énumérées par nature, pour que la couche qui a les
// registres vérifie leur existence **à l'enregistrement**.
func TestLesReferencesSonEnumereesParNature(t *testing.T) {
	sujet := sujet(nil)
	sujet.Plan = plan.Plan{Stages: []plan.Stage{
		{Gestes: []plan.Geste{
			{Kind: plan.GestePipeline, Ref: "unit-tests"},
			{Kind: plan.GestePipeline, Ref: "static-tests"},
		}},
		{Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}}},
	}}

	pipelines, actions := sujet.Refs()
	if len(pipelines) != 2 || pipelines[0] != "unit-tests" {
		t.Errorf("pipelines = %v", pipelines)
	}
	if len(actions) != 1 || actions[0] != "webhook" {
		t.Errorf("actions = %v", actions)
	}
}

// Le déclencheur d'une réaction se distingue de celui de la chaîne intégrée : les
// deux partagent la même unicité par fait, et doivent pouvoir coexister sans que
// l'une bloque l'autre.
func TestLeDeclencheurSeDistingueDeLaChaineIntegree(t *testing.T) {
	sujet := sujet(nil)
	sujet.ID = 42

	if got := sujet.Trigger(); got != "reaction:42" {
		t.Errorf("déclencheur = %q", got)
	}
	if strings.HasPrefix(sujet.Trigger(), "builtin:") {
		t.Error("une réaction déclarée se confond avec la chaîne intégrée")
	}
}
