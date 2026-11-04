package configure

import (
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

/*
 * A stage's role, checked and not taken on trust.
 *
 * Without this check, a release's life cycle would change by accident: adding a notification to
 * the build stage would make the move into verification depend on a webhook.
 */

// roleCatalogue returns a catalogue carrying a build and a test.
func roleCatalogue() *catalog.Catalog {
	return &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "build", PipelineRef: "build-image",
			Produces: []catalog.Product{catalog.ProductImage}},
		{Kind: "unit-tests", PipelineRef: "unit", Verification: true,
			Produces: []catalog.Product{catalog.ProductResults}},
		{Kind: "go/build", PipelineRef: "go-build",
			Produces: []catalog.Product{catalog.ProductBinary}},
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
	service := Automations{Catalog: roleCatalogue()}

	err := service.checkRoles(planWithStage(plan.RoleBuild, pipelineGeste("unit-tests")))
	if err == nil {
		t.Fatal("a test pipeline was admitted into a build stage")
	}
	// The message must say what was missing and what the pipeline produces: without the second,
	// one looks for the mistake in the role rather than in the step.
	for _, expected := range []string{"build", "unit-tests", "image", "results"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("reason %q — it must carry %q", err, expected)
		}
	}
}

// An action in a stage with a role is refused.
//
// The most insidious case: a failed webhook would fail the release, although it has nothing
// to say about its quality.
func TestAnActionDoesNotEnterAStageWithARole(t *testing.T) {
	service := Automations{Catalog: roleCatalogue()}

	err := service.checkRoles(planWithStage(plan.RoleTest,
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
	service := Automations{Catalog: roleCatalogue()}

	err := service.checkRoles(planWithStage("",
		plan.Geste{Kind: plan.GesteAction, Ref: "webhook"},
		pipelineGeste("unit-tests")))
	if err != nil {
		t.Errorf("a stage with no role was refused: %v", err)
	}
}

// Both roles accept what matches them.
func TestEachRoleAdmitsWhatMatchesIt(t *testing.T) {
	service := Automations{Catalog: roleCatalogue()}

	for _, admitted := range []struct {
		role plan.Role
		ref  string
	}{{plan.RoleBuild, "build"}, {plan.RoleBuild, "go/build"}, {plan.RoleTest, "unit-tests"}} {
		if err := service.checkRoles(planWithStage(admitted.role, pipelineGeste(admitted.ref))); err != nil {
			t.Errorf("role %q with %q refused: %v", admitted.role, admitted.ref, err)
		}
	}
}
