package http

import (
	"slices"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

/*
 * Le catalogue des faits du noyau, et la seconde vérité qu'il constitue.
 *
 * `coreEventChoices` est écrit à la main : les faits du noyau n'ont pas de
 * descripteur, et leur en donner un pour la seule commodité de cette liste
 * ajouterait un registre à tenir aligné. Le prix de ce choix est que la liste peut
 * dériver de ce que les constructeurs produisent — et la dérive est **muette** :
 * l'éditeur proposerait un champ qu'aucune charge ne porte, et l'appariement
 * n'apparierait jamais.
 *
 * Ces deux tests sont le contrepoids de ce choix.
 */

// instant fixes the date of the example facts.
var instant = time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

// The catalogue offers exactly the three core facts.
//
// The reduction removed three of them. Leaving them here would have left them choosable in
// the editor, for reactions nothing triggers any more.
func TestTheCatalogueOffersTheCoresThreeFacts(t *testing.T) {
	attendus := map[string]bool{
		string(event.VersionCreated): true, string(event.ReleaseCreated): true,
		string(event.ReleaseReleased): true,
	}

	for _, choice := range coreEventChoices() {
		if !attendus[choice.Type] {
			t.Errorf("fact %q offered — it is not one of the core's three", choice.Type)
		}
		delete(attendus, choice.Type)
		if choice.Label == "" {
			t.Errorf("fact %q with no label — the editor's list would be mute", choice.Type)
		}
	}
	for manquant := range attendus {
		t.Errorf("fact %q is not offered to the editor", manquant)
	}
}

// Every announced field exists in the payload the constructor produces.
//
// That is the check that counts: a misspelled field here would be offered in the editor,
// saved without complaint, and the match would never match. Nothing would say so — a
// reaction that does not leave looks like a reaction nobody triggered.
func TestEveryAnnouncedFieldExistsInThePayload(t *testing.T) {
	echeance := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// The constructors filled in completely: an optional field left bare would make its
	// absence look like a typo.
	charges := map[string]map[string]string{
		string(event.VersionCreated): event.VersionCreatedAt(
			instant, 805, "v3.2.0", "open", "redmine", "412", &echeance).Payload,
		string(event.ReleaseCreated): event.ReleaseCreatedAt(
			instant, 7, 42, "1.8.0-rc1", "pre", "main", "1.8.0").Payload,
		string(event.ReleaseReleased): event.ReleaseReleasedAt(
			instant, 7, 42, "1.8.0", "vcarroy").Payload,
	}

	for _, choice := range coreEventChoices() {
		charge, connu := charges[choice.Type]
		if !connu {
			t.Errorf("fact %q with no sample — the check does not cover it", choice.Type)
			continue
		}
		for _, champ := range choice.Fields {
			if _, present := charge[champ]; !present {
				t.Errorf("fact %q: field %q is offered and the payload does not carry it",
					choice.Type, champ)
			}
		}
		// And the reverse: a field carried but not announced is matchable without being offered.
		// Less serious — nothing breaks — but it is a capability offered without saying so.
		for champ := range charge {
			if !slices.Contains(choice.Fields, champ) {
				t.Errorf("fact %q: the payload carries %q and the editor does not offer it",
					choice.Type, champ)
			}
		}
	}
}

/*
 * Le rôle d'une étape, vérifié et pas cru sur parole.
 *
 * Sans ce contrôle, le cycle de vie d'une release changerait par accident :
 * ajouter une notification à l'étape de construction ferait que le passage en
 * vérification dépendrait d'un webhook (doc 15 §15.3).
 */

// roleCatalogue returns a catalogue carrying a build and a test.
func roleCatalogue() *catalog.Catalog {
	return &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "build", PipelineRef: "build-image",
			Produces: []catalog.Product{catalog.ProductImage}},
		{Kind: "unit-tests", PipelineRef: "unit", Verification: true,
			Produces: []catalog.Product{catalog.ProductResults}},
	}}
}

func planWithStage(role plan.Role, steps ...plan.Geste) plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{Role: role, Gestes: steps}}}
}

func pipelineGeste(ref string) plan.Geste {
	return plan.Geste{Kind: plan.GestePipeline, Ref: ref}
}

// A build stage whose step produces no image is refused.
func TestABuildStageRequiresAStepThatProducesAnImage(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	err := handler.checkRoles(planWithStage(plan.RoleBuild, pipelineGeste("unit-tests")))
	if err == nil {
		t.Fatal("a test pipeline was admitted into a build stage")
	}
	// The message must say what was missing and what the pipeline produces: without the second,
	// one looks for the mistake in the role rather than in the step.
	for _, attendu := range []string{"build", "unit-tests", "image", "results"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("reason %q — it must carry %q", err, attendu)
		}
	}
}

// An action in a stage with a role is refused.
//
// The most insidious case: a failed webhook would fail the release, although it has nothing
// to say about its quality.
func TestAnActionDoesNotEnterAStageWithARole(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	err := handler.checkRoles(planWithStage(plan.RoleTest,
		plan.Geste{Kind: plan.GesteAction, Ref: "webhook"}))
	if err == nil {
		t.Fatal("an action was admitted into a stage with a role")
	}
	if !strings.Contains(err.Error(), "quality") {
		t.Errorf("reason %q — it must say why it is refused", err)
	}
}

// A stage with no role carries what it likes.
//
// It is what allows warning a team without putting the release's state at stake: the defect
// the stage role corrects.
func TestAStageWithNoRoleCarriesWhatItLikes(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	err := handler.checkRoles(planWithStage("",
		plan.Geste{Kind: plan.GesteAction, Ref: "webhook"},
		pipelineGeste("unit-tests")))
	if err != nil {
		t.Errorf("a stage with no role was refused: %v", err)
	}
}

// Both roles accept what matches them.
func TestEachRoleAdmitsWhatMatchesIt(t *testing.T) {
	handler := &AutomationsHandler{catalog: roleCatalogue()}

	for role, ref := range map[plan.Role]string{
		plan.RoleBuild: "build", plan.RoleTest: "unit-tests",
	} {
		if err := handler.checkRoles(planWithStage(role, pipelineGeste(ref))); err != nil {
			t.Errorf("role %q with %q refused: %v", role, ref, err)
		}
	}
}
