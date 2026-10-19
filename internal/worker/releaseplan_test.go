package worker

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The door.
 *
 * The question is single: **does build then verify fit into a single plan?** These tests answer
 * it on the shape of the composed plan, before a line of editor has been written.
 *
 * What they hold is not the queueing — the runner takes care of that — but the structure: two
 * stages, their roles, and the separation between what the step fixes and what the context
 * binds.
 */

// testCatalogue returns a catalogue carrying one build and two verifications.
func testCatalogue(t *testing.T) *catalog.Catalog {
	t.Helper()

	const source = `
pipelines:
  - kind: core/build
    pipelineRef: build-image
    label: Build
    produces: [image]
    # The kind's real parameters: the plan no longer sets the revision or the tag
    # literally, it reads what the catalogue declares it wants. A mute test catalogue
    # would make that read look correct although it would return nothing.
    params:
      revision:   { source: input, required: true, suggest: git-ref }
      version:    { source: input, required: true, suggest: release-tag }
      tag-name:   { source: input, required: true, suggest: release-tag }
      image-name: { source: input, required: true, suggest: image-name, label: Published repository }
      target:     { source: input, required: true, suggest: build-target }
      dockerfile: { source: input }
      context:    { source: input }
  - kind: security-audit
    pipelineRef: audit
    label: Security audit
    verification: true
    produces: [results]
    params:
      # An image **reference**: the analysis bears on what the build published, and
      # the step can target the artifact rather than recompose it.
      portal-image: { source: input, required: true, suggest: image, image: portal }
    uploads:
      results:
        param: results-upload-url
        artifact: audit.json
        family: security
  - kind: unit-tests
    pipelineRef: unit-tests
    label: Unit tests
    verification: true
    produces: [results]
    params:
      revision: { source: input, required: true, suggest: git-ref }
    uploads:
      results:
        param: results-upload-url
        artifact: unit-tests.xml
        family: unit
        format: junit
  - kind: e2e-tests
    pipelineRef: e2e-tests
    label: Tests E2E
    verification: true
    produces: [results]
    params:
      revision: { source: input, required: true, suggest: git-ref }
    uploads:
      results:
        param: results-upload-url
        artifact: e2e-tests.xml
        family: e2e
        format: junit
`
	registry, err := catalog.Parse([]byte(source))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	return registry
}

// testPipeline returns one kind of the test catalogue.
func testPipeline(t *testing.T, kind string) catalog.Pipeline {
	t.Helper()
	pipeline, found := testCatalogue(t).Find(kind)
	if !found {
		t.Fatalf("kind %q missing from the test catalogue", kind)
	}
	return pipeline
}

func twoImageProject() store.Project {
	return store.Project{
		ID: 1, Slug: "mse", Repository: "CNOUS/mse",
		Dockerfile: "Dockerfile", BuildContext: ".",
		MultiStage: true,
		Targets: []store.Target{
			{Stage: "web", Image: "portal"},
			{Stage: "cli", Image: "portal-cli"},
		},
	}
}

// The door: both halves of the chain enter a single plan.
func TestBuildAndVerifyFitIntoASinglePlan(t *testing.T) {
	sujet := DefaultAutomation(testCatalogue(t), twoImageProject(), nil)

	if err := sujet.Validate(); err != nil {
		t.Fatalf("the composed plan must be valid: %v", err)
	}
	if len(sujet.Stages) != 2 {
		t.Fatalf("%d stage(s), expected build then verify", len(sujet.Stages))
	}

	// The roles are where the life cycle is anchored: without them, nothing would advance the
	// release once `release.verified` is removed.
	if sujet.Stages[0].Role != plan.RoleBuild {
		t.Errorf("role of the first stage = %q", sujet.Stages[0].Role)
	}
	if sujet.Stages[1].Role != plan.RoleTest {
		t.Errorf("role of the second stage = %q", sujet.Stages[1].Role)
	}
	if sujet.LastWithRole(plan.RoleBuild) != 0 || sujet.LastWithRole(plan.RoleTest) != 1 {
		t.Errorf("ancrages = %d et %d",
			sujet.LastWithRole(plan.RoleBuild), sujet.LastWithRole(plan.RoleTest))
	}

	// One step per image: MSE's three images stop being a loop in the API and become steps that are
	// readable and re-runnable.
	if len(sujet.Stages[0].Gestes) != 2 {
		t.Fatalf("%d build step(s), expected one per target",
			len(sujet.Stages[0].Gestes))
	}
	libelles := []string{sujet.Stages[0].Gestes[0].Label, sujet.Stages[0].Gestes[1].Label}
	if libelles[0] != "portal" || libelles[1] != "portal-cli" {
		t.Errorf("labels = %v — they tell apart two steps of the same reference", libelles)
	}

	// The verification starts **if the build succeeded**. It is that condition which replaces the
	// `release.built` fact: the same thing, without a round trip through the fact feed.
	for _, geste := range sujet.Stages[1].Gestes {
		if geste.Condition() != plan.WhenSuccess {
			t.Errorf("step %q conditioned on %q, expected %q",
				geste.Ref, geste.Condition(), plan.WhenSuccess)
		}
	}
}

// The step carries what the declaration fixes, the context what depends on the fact.
//
// It is the separation that makes a plan reusable from one release to the next: a plan that
// froze the revision would serve only once.
func TestTheStepFixesTheImageAndTheContextBindsTheRevision(t *testing.T) {
	sujet := DefaultAutomation(testCatalogue(t), twoImageProject(), nil)
	geste := sujet.Stages[0].Gestes[0]

	// Nothing that depends on the release is in the step.
	for _, interdit := range []string{"revision", "version", "tag-name"} {
		if _, present := geste.Params[interdit]; present {
			t.Errorf("the step freezes %q, which depends on the release", interdit)
		}
	}
	if geste.Params["image-name"] != "portal" || geste.Params["target"] != "web" {
		t.Errorf("parameters of the step = %v", geste.Params)
	}

	contexte := releaseContext(
		store.Release{ID: 42, Tag: "3.2.0-rc8"}, "c1f8f8cd", "reg.example.fr/mse")
	params, err := stepParams(testPipeline(t, buildKind), geste, contexte)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if params["revision"] != "c1f8f8cd" {
		t.Errorf("revision bound = %q", params["revision"])
	}
	if params["version"] != "3.2.0-rc8" || params["tag-name"] != "3.2.0-rc8" {
		t.Errorf("tag bound = %v", params)
	}
	// And what the step fixed is still there.
	if params["image-name"] != "portal" {
		t.Errorf("the step's image was lost: %v", params)
	}
}

// A step cannot redefine the revision built.
//
// Without that rule, two steps of one release could target two commits and publish two images
// under the same tag — the version invariant would fall with not a single message.
func TestAStepCannotRedefineTheRevision(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: buildKind,
		Params: map[string]string{
			"image-name": "portal", "target": "web",
			"revision": "another-commit",
		},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "the-right-commit", "reg")

	params, err := stepParams(testPipeline(t, buildKind), geste, contexte)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := params["revision"]; got != "the-right-commit" {
		t.Errorf("revision = %q — the context must win over the step", got)
	}
}

// What the project does not verify is not chained.
//
// A project with no E2E campaign saw the pipeline follow on, fail for want of a scenario, and
// leave its release waiting for a verdict that would never come.
func TestAKindNotEnabledIsNotChained(t *testing.T) {
	sujet := DefaultAutomation(testCatalogue(t), twoImageProject(),
		func(kind string) bool { return kind != "e2e-tests" })

	if len(sujet.Stages) != 2 {
		t.Fatalf("%d stage(s)", len(sujet.Stages))
	}
	for _, geste := range sujet.Stages[1].Gestes {
		if geste.Ref == "e2e-tests" {
			t.Error("a kind the project has not enabled was chained")
		}
	}
	// The catalogue's other two kinds remain, and that is what proves the filter bears on a kind
	// and not on the whole stage.
	if len(sujet.Stages[1].Gestes) != 2 {
		t.Errorf("%d verification(s), expected the two enabled", len(sujet.Stages[1].Gestes))
	}
}

// A project that builds nothing and verifies nothing returns an empty plan.
//
// It is a legitimate configuration, distinct from an invalid plan: the caller concludes at once
// rather than waiting for a verdict nobody will give.
func TestAProjectWithNothingReturnsAnEmptyPlan(t *testing.T) {
	sujet := DefaultAutomation(testCatalogue(t),
		store.Project{ID: 2, Slug: "vide"},
		func(string) bool { return false })

	if !sujet.Empty() {
		t.Errorf("plan = %+v, expected empty", sujet)
	}
}

// testAdvances keeps the transitions requested.
type testAdvances struct {
	demandes []string
	etat     string
}

func (a *testAdvances) AdvanceReleaseState(
	_ context.Context, _ int64, from, to string,
) (bool, error) {
	a.demandes = append(a.demandes, from+"->"+to)
	if a.etat != from {
		// Conditioned on the starting state, as in the database: a repetition writes nothing.
		return false, nil
	}
	a.etat = to
	return true, nil
}

func releaseRun() store.PlanRun {
	id := int64(7)
	return store.PlanRun{ID: 1, ReleaseID: &id}
}

// The settling of a stage advances the release, and replaces the build count.
func TestTheSettlingOfAStageAdvancesTheRelease(t *testing.T) {
	suivi := &testAdvances{etat: string(release.StateBuilding)}
	cycle := NewReleaseLifecycle(suivi, quietLogger())
	ctx := context.Background()

	cycle.StageSettled(ctx, releaseRun(), plan.RoleBuild, plan.Succeeded)
	if suivi.etat != string(release.StateVerifying) {
		t.Fatalf("state = %q, expected verifying", suivi.etat)
	}

	cycle.StageSettled(ctx, releaseRun(), plan.RoleTest, plan.Succeeded)
	if suivi.etat != string(release.StateReady) {
		t.Errorf("state = %q, expected ready", suivi.etat)
	}
}

// The same stage announced ten times advances the release only once.
//
// The engine keeps no "already announced" mark: it re-announces every settled stage at each
// advance. The idempotence comes from the conditional write, and that is what is exercised
// here.
func TestAStageAnnouncedAgainDoesNotAdvanceTwice(t *testing.T) {
	suivi := &testAdvances{etat: string(release.StateBuilding)}
	cycle := NewReleaseLifecycle(suivi, quietLogger())
	ctx := context.Background()

	for range 10 {
		cycle.StageSettled(ctx, releaseRun(), plan.RoleBuild, plan.Succeeded)
	}

	if suivi.etat != string(release.StateVerifying) {
		t.Errorf("state = %q", suivi.etat)
	}
	// Ten requests, a single advance: it is the database that decides, not the engine.
	if len(suivi.demandes) != 10 {
		t.Errorf("%d requests, expected 10", len(suivi.demandes))
	}
}

// A skipped stage touches nothing.
//
// It happens when the build failed: the tests are skipped, and the release is already `failed`
// from the stage before. Making it land elsewhere would overwrite a state that is already
// right.
func TestASkippedStageDoesNotTouchTheRelease(t *testing.T) {
	suivi := &testAdvances{etat: string(release.StateFailed)}
	cycle := NewReleaseLifecycle(suivi, quietLogger())

	cycle.StageSettled(context.Background(), releaseRun(),
		plan.RoleTest, plan.Skipped)

	if len(suivi.demandes) != 0 {
		t.Errorf("transitions requested = %v, expected none", suivi.demandes)
	}
}

// A plan with no release touches nothing: a declared reaction has no business moving a life
// cycle that does not concern it.
func TestAPlanWithNoReleaseAdvancesNothing(t *testing.T) {
	suivi := &testAdvances{etat: string(release.StateBuilding)}
	NewReleaseLifecycle(suivi, quietLogger()).StageSettled(
		context.Background(), store.PlanRun{ID: 2}, plan.RoleBuild, plan.Succeeded)

	if len(suivi.demandes) != 0 {
		t.Errorf("transitions requested = %v", suivi.demandes)
	}
}

// A build stage still in progress does not advance the release.
//
// # The property the build count held
//
// A release builds several images. Until the last has concluded, it stays building — the
// observer held that through `CountBuilds`, a count in the database re-read at every conclusion.
//
// The stage holds it with no query: it is **settled** only when all its steps are, and
// [plan.Progress.StageOutcome] returns nothing before that.
func TestAPartialBuildDoesNotAdvanceTheRelease(t *testing.T) {
	sujet := DefaultAutomation(testCatalogue(t), twoImageProject(), nil)
	progress := plan.Start(sujet)

	// The first image concluded, the second is still running.
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 0}, plan.Succeeded)
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 1}, plan.Running)

	if _, settled := progress.StageOutcome(0); settled {
		t.Fatal("a stage one of whose steps is still running must not be settled")
	}

	// The second concludes: the stage is settled, and only then.
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 1}, plan.Succeeded)
	outcome, settled := progress.StageOutcome(0)
	if !settled || outcome != plan.Succeeded {
		t.Errorf("outcome = %q, settled = %v", outcome, settled)
	}
}

// A failed image concludes the stage without waiting for the others.
//
// Waiting would keep running builds whose result is already known to be useless, and would
// delay by as much the moment the screen says what is wrong.
func TestAFailedImageFailsTheStageAsSoonAsItSettles(t *testing.T) {
	sujet := DefaultAutomation(testCatalogue(t), twoImageProject(), nil)
	progress := plan.Start(sujet)

	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 0}, plan.Failed)
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 1}, plan.Succeeded)

	outcome, settled := progress.StageOutcome(0)
	if !settled {
		t.Fatal("the stage must be settled")
	}
	// A failure wins over a success: the missing image is missing, whatever the success of the
	// others.
	if outcome != plan.Failed {
		t.Errorf("outcome = %q, expected failed", outcome)
	}
}

/*
 * Passing an artifact from one stage to the next.
 *
 * The second door. The question: can a test step target what the build has just published,
 * without a language of expressions?
 */

// An artifact template is bound to the context, like the others.
func TestAStepTargetsAnEarlierStagesArtifact(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: "security-audit",
		Params: map[string]string{"portal-image": "{artifacts.portal.image}"},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "abc", "reg")
	contexte["artifacts.portal.image"] = "reg.example.fr/portal:1.0.0"

	params, err := stepParams(testPipeline(t, "security-audit"), geste, contexte)
	if err != nil {
		t.Fatalf("%v", err)
	}
	// The artifact wins over the fallback: with no declaration, the run would have recomposed
	// `reg/portal:1.0.0`, which holds even when nobody has published it.
	if params["portal-image"] != "reg.example.fr/portal:1.0.0" {
		t.Errorf("image = %q", params["portal-image"])
	}
}

// An artifact that was not produced fails the step, naming it.
//
// Started with its literal template, the pipeline would clone, fail at the `pull`, and the
// reason would speak of a repository that cannot be found — a long way from the cause. A test
// that silently does not run reads as a test that passed.
func TestAMissingArtifactFailsTheStepNamingIt(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: "security-audit",
		Params: map[string]string{"portal-image": "{artifacts.portal.image}"},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "abc", "reg")

	_, err := stepParams(testPipeline(t, "security-audit"), geste, contexte)
	if err == nil {
		t.Fatal("a missing artifact must fail the step")
	}
	if !strings.Contains(err.Error(), "artifacts.portal.image") {
		t.Errorf("reason %q — it must name the missing artifact", err)
	}
}

// The other unresolved variables stay literal, and that is deliberate.
//
// A `{tag}` visible in a message is noticed and fixed. An artifact, on the other hand, names
// something that was supposed to exist: its absence is an error in the plan.
func TestAnOrdinaryUnresolvedVariableDoesNotFail(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: buildKind,
		Params: map[string]string{
			"note": "construit depuis {branche}",
			// The required ones, without which the refusal would bear on them and not on the note.
			"image-name": "portal", "target": "web",
		},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "abc", "reg")

	params, err := stepParams(testPipeline(t, buildKind), geste, contexte)
	if err != nil {
		t.Fatalf("%v — only an artifact reference must be required", err)
	}
	if params["note"] != "construit depuis {branche}" {
		t.Errorf("note = %q", params["note"])
	}
}
