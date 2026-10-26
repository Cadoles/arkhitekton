package reaction

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

func planForTest() plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{
		{Kind: plan.GestePipeline, Ref: "unit-tests"},
	}}}}
}

func subject(match map[string]string) Reaction {
	return Reaction{
		Name: "Warn the team", EventType: "release.created",
		Enabled: true, Match: match, Plan: planForTest(),
	}
}

/*
 * L'appariement.
 */

// Every declared key must match: a match is a conjunction.
func TestAMatchIsAConjunction(t *testing.T) {
	subject := subject(map[string]string{"kind": "final", "outcome": "succeeded"})

	if !subject.Matches(map[string]string{"kind": "final", "outcome": "succeeded", "tag": "1.8.0"}) {
		t.Error("a payload satisfying everything does not match")
	}
	if subject.Matches(map[string]string{"kind": "final", "outcome": "failed"}) {
		t.Error("a payload satisfying only one key matches")
	}
}

// Membership is written with commas: `preprod,production` is typed into a text
// field and reads back without documentation.
func TestMembershipIsWrittenWithCommas(t *testing.T) {
	subject := subject(map[string]string{"environment": "preprod, production"})

	for _, value := range []string{"preprod", "production"} {
		if !subject.Matches(map[string]string{"environment": value}) {
			t.Errorf("environment %q does not match", value)
		}
	}
	if subject.Matches(map[string]string{"environment": "qualif"}) {
		t.Error("an environment outside the list matches")
	}
}

// A key absent from the payload does not match.
//
// The opposite would fire a reaction on a fact that does not carry what it talks
// about — the exact case of an `environment` expected on a fact that has none.
func TestAKeyAbsentFromThePayloadDoesNotMatch(t *testing.T) {
	subject := subject(map[string]string{"environment": "production"})

	if subject.Matches(map[string]string{"tag": "1.8.0"}) {
		t.Error("a payload lacking the matched field triggered the reaction")
	}
}

// An empty match leaves on any fact of that type: it is the simplest declaration,
// and it must stay possible.
func TestAnEmptyMatchLeavesOnAnyFactOfThatType(t *testing.T) {
	if !subject(nil).Matches(map[string]string{"n'importe": "quoi"}) {
		t.Error("an empty match does not match")
	}
	if !subject(map[string]string{}).Matches(nil) {
		t.Error("an empty match does not match an empty payload")
	}
}

/*
 * The validation, done when a reaction is recorded.
 */

// A reaction with no name is refused: the name is how it is found in a list, and
// "reaction 7" teaches nothing.
func TestAReactionWithoutANameIsRefused(t *testing.T) {
	subject := subject(nil)
	subject.Name = "  "

	err := subject.Validate()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "name") {
		t.Errorf("%v", err)
	}
}

// A reaction listening for no fact is refused.
func TestAReactionListeningForNoFactIsRefused(t *testing.T) {
	subject := subject(nil)
	subject.EventType = ""

	if err := subject.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

// A match with no value is refused: it would match an absent field, which the rule
// refuses elsewhere, and would give a reaction that never leaves with nobody
// knowing why.
func TestAMatchWithoutAValueIsRefused(t *testing.T) {
	err := subject(map[string]string{"kind": "  "}).Validate()

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "kind") {
		t.Errorf("%v", err)
	}
}

// The plan is validated with the reaction: a reaction whose plan does not hold
// makes no sense, and letting it through would postpone the diagnosis to the moment
// it triggers.
func TestThePlanIsValidatedWithTheReaction(t *testing.T) {
	subject := subject(nil)
	subject.Plan = plan.Plan{Stages: []plan.Stage{{}}}

	if err := subject.Validate(); err == nil {
		t.Error("a reaction with an invalid plan was accepted")
	}
}

/*
 * What the layer above must check.
 */

// References are listed by kind, so the layer that has the registries can check
// they exist when saving.
func TestReferencesAreListedByKind(t *testing.T) {
	subject := subject(nil)
	subject.Plan = plan.Plan{Stages: []plan.Stage{
		{Gestes: []plan.Geste{
			{Kind: plan.GestePipeline, Ref: "unit-tests"},
			{Kind: plan.GestePipeline, Ref: "static-tests"},
		}},
		{Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}}},
	}}

	pipelines, actions := subject.Refs()
	if len(pipelines) != 2 || pipelines[0] != "unit-tests" {
		t.Errorf("pipelines = %v", pipelines)
	}
	if len(actions) != 1 || actions[0] != "webhook" {
		t.Errorf("actions = %v", actions)
	}
}

// A reaction's trigger differs from the built-in chain's: both share the same
// uniqueness per fact, and must coexist without either blocking the other.
func TestTheTriggerDiffersFromTheBuiltInChain(t *testing.T) {
	subject := subject(nil)
	subject.ID = 42

	if got := subject.Trigger(); got != "reaction:42" {
		t.Errorf("trigger = %q", got)
	}
	if strings.HasPrefix(subject.Trigger(), "builtin:") {
		t.Error("a declared reaction is confused with the built-in chain")
	}
}
