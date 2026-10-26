package plan

import (
	"errors"
	"strings"
	"testing"
)

// pipeline composes a pipeline step, the most common form.
func pipeline(ref string, when When) Geste {
	return Geste{Kind: GestePipeline, Ref: ref, When: when}
}

// advance walks a plan, marking each leaving step with the fate it is given, until
// nothing is left to decide.
//
// The engine reduced to what it really does: ask, run, record. Writing it here
// rather than simulating by hand makes the tests readable and exercises the loop
// itself — that it terminates, in particular.
func advance(t *testing.T, progress Progress, sorts map[string]State) Progress {
	t.Helper()

	for round := 0; ; round++ {
		if round > 100 {
			t.Fatal("the advancing loop does not terminate")
		}
		steps := progress.Next()
		if len(steps) == 0 {
			return progress
		}
		for _, step := range steps {
			state := Skipped
			if step.Run {
				state, _ = sorts[step.Geste.Ref]
				if state == "" {
					state = Succeeded
				}
			}
			var err error
			if progress, err = progress.Mark(step.Position, state); err != nil {
				t.Fatalf("marking: %v", err)
			}
		}
	}
}

/*
 * The model's shape: stages, parallel steps.
 */

// The example that motivated the work, in full.
//
// "Set a status, then three builds in parallel, then four tests if the builds
// succeeded, then set the final status." If it does not fit the model, the model
// is wrong.
func TestTheRequestedExampleRunsInFourStages(t *testing.T) {
	subject := Plan{Stages: []Stage{
		{Gestes: []Geste{{Kind: GesteAction, Ref: "forge.status"}}},
		{Gestes: []Geste{
			pipeline("build-web", ""), pipeline("build-cli", ""), pipeline("build-worker", ""),
		}},
		{Gestes: []Geste{
			pipeline("unit-tests", ""), pipeline("static-tests", ""),
			pipeline("e2e-tests", ""), pipeline("security-audit", ""),
		}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "forge.status", When: WhenAlways}}},
	}}

	if err := subject.Validate(); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	progress := Start(subject)

	// First stage: one step offered, and nothing else.
	firstOnes := progress.Next()
	if len(firstOnes) != 1 || firstOnes[0].Geste.Ref != "forge.status" {
		t.Fatalf("first stage: %+v", firstOnes)
	}

	// The next stage does not open until the first is settled.
	progress, _ = progress.Mark(firstOnes[0].Position, Running)
	if steps := progress.Next(); len(steps) != 0 {
		t.Errorf("a stage in flight let the next one through: %+v", steps)
	}

	progress, _ = progress.Mark(firstOnes[0].Position, Succeeded)

	// Second stage: the three builds together.
	builds := progress.Next()
	if len(builds) != 3 {
		t.Fatalf("%d builds offered, expected 3 in parallel", len(builds))
	}
	for _, step := range builds {
		if !step.Run {
			t.Errorf("build %q not launched after a successful stage", step.Geste.Ref)
		}
	}

	progress = advance(t, progress, nil)

	if !progress.Done() {
		t.Error("the plan is not settled")
	}
	if progress.Outcome() != Succeeded {
		t.Errorf("issue = %q", progress.Outcome())
	}
}

// The existing chain: one stage, N verifications in parallel. That is the shape
// this must reproduce exactly.
func TestTheVerificationChainIsASingleStagePlan(t *testing.T) {
	subject := Plan{Stages: []Stage{{Gestes: []Geste{
		pipeline("unit-tests", ""), pipeline("static-tests", ""),
		pipeline("integration-tests", ""), pipeline("security-audit", ""),
	}}}}

	if err := subject.Validate(); err != nil {
		t.Fatalf("plan refused: %v", err)
	}

	steps := Start(subject).Next()
	if len(steps) != 4 {
		t.Fatalf("%d verifications offered, expected 4", len(steps))
	}
	for _, step := range steps {
		if step.Geste.Kind != GestePipeline || !step.Run {
			t.Errorf("step %+v", step)
		}
	}
}

/*
 * Les conditions.
 */

// A failure closes what was conditioned on success, opens what was conditioned on
// failure, and lets through what must always leave.
func TestAFailureSteersTheNextStage(t *testing.T) {
	subject := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{Gestes: []Geste{
			pipeline("tests", WhenSuccess),
			{Kind: GesteAction, Ref: "prevenir", When: WhenFailure},
			{Kind: GesteAction, Ref: "status", When: WhenAlways},
		}},
	}}

	progress := Start(subject)
	firstOnes := progress.Next()
	progress, _ = progress.Mark(firstOnes[0].Position, Failed)

	suite := progress.Next()
	if len(suite) != 3 {
		t.Fatalf("%d steps offered", len(suite))
	}

	choice := map[string]bool{}
	for _, step := range suite {
		choice[step.Geste.Ref] = step.Run
	}
	if choice["tests"] {
		t.Error("the tests left after a failed build")
	}
	if !choice["prevenir"] {
		t.Error("the failure reaction did not leave")
	}
	if !choice["status"] {
		t.Error("the unconditional step did not leave")
	}
}

// A wholly skipped stage has not failed: it attempted nothing.
//
// Without that distinction, a step conditioned on failure would leave behind a
// stage nothing ran — and would warn of an outage that never happened.
func TestASkippedStageHasNotFailed(t *testing.T) {
	subject := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "reparer", When: WhenFailure}}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "alerter", When: WhenFailure}}},
	}}

	progress := Start(subject)
	firstOnes := progress.Next()
	// The build succeeds: stage 2 is therefore skipped entirely.
	progress, _ = progress.Mark(firstOnes[0].Position, Succeeded)

	secondItem := progress.Next()
	if len(secondItem) != 1 || secondItem[0].Run {
		t.Fatalf("second stage: %+v", secondItem)
	}
	progress, _ = progress.Mark(secondItem[0].Position, Skipped)

	third := progress.Next()
	if len(third) != 1 {
		t.Fatalf("third stage: %+v", third)
	}
	if third[0].Run {
		t.Error("a failure step left behind a skipped stage, which missed nothing")
	}
}

// A plan's outcome is failure as soon as one step failed, wherever it is.
//
// A concluding `always` step that succeeds does not redeem the failure that made
// it leave.
func TestAConcludingStepDoesNotRedeemAFailure(t *testing.T) {
	subject := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "status", When: WhenAlways}}},
	}}

	progress := advance(t, Start(subject), map[string]State{"build": Failed})

	if !progress.Done() {
		t.Fatal("plan not settled")
	}
	if progress.Outcome() != Failed {
		t.Errorf("outcome = %q, expected %q", progress.Outcome(), Failed)
	}
}

/*
 * Idempotence, which is the property the engine depends on.
 */

// Calling Next without running anything returns the same thing; calling it after
// marking the leaving steps returns nothing until the stage is settled.
func TestNextIsCallableAgainWithoutEffect(t *testing.T) {
	subject := Plan{Stages: []Stage{{Gestes: []Geste{
		pipeline("unit-tests", ""), pipeline("static-tests", ""),
	}}}}

	progress := Start(subject)
	first, second := progress.Next(), progress.Next()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("successive calls: %d then %d steps, expected 2 each time",
			len(first), len(second))
	}
	for index := range first {
		if first[index].Position != second[index].Position {
			t.Errorf("step %d: positions %+v then %+v — two calls must return "+
				"the same thing", index, first[index].Position, second[index].Position)
		}
	}

	for _, step := range progress.Next() {
		progress, _ = progress.Mark(step.Position, Running)
	}
	if steps := progress.Next(); len(steps) != 0 {
		t.Errorf("steps that already left are offered again: %+v", steps)
	}
}

// Marking outside the plan is refused rather than silently absorbed: it is the
// symptom of an engine that lost the correspondence between a job and its step,
// and absorbing it would leave the plan stuck with nothing said.
func TestMarkingOutsideThePlanIsRefused(t *testing.T) {
	progress := Start(Plan{Stages: []Stage{{Gestes: []Geste{pipeline("build", "")}}}})

	for _, position := range []Position{{Stage: 1}, {Stage: 0, Geste: 3}, {Stage: -1}} {
		if _, err := progress.Mark(position, Succeeded); !errors.Is(err, ErrInvalid) {
			t.Errorf("position %+v accepted: %v", position, err)
		}
	}
}

// Marking returns a copy: a caller holding the previous progress must not see it
// change underneath.
func TestMarkingDoesNotMutatePreviousProgress(t *testing.T) {
	before := Start(Plan{Stages: []Stage{{Gestes: []Geste{pipeline("build", "")}}}})

	after, err := before.Mark(Position{}, Succeeded)
	if err != nil {
		t.Fatalf("marking: %v", err)
	}
	if before.States[0][0] != Pending {
		t.Error("the previous progress was mutated")
	}
	if after.States[0][0] != Succeeded {
		t.Error("the mark did not take")
	}
}

/*
 * What a plan must declare in order to be runnable.
 */

// An empty plan is recognised without being invalid: "this project chains nothing"
// is a legitimate configuration, and the caller must be able to conclude at once.
func TestAnEmptyPlanIsRecognisedWithoutBeingInvalid(t *testing.T) {
	if !(Plan{}).Empty() {
		t.Error("a plan with no stage is not seen as empty")
	}
	if !(Plan{Stages: []Stage{{}}}).Empty() {
		t.Error("a stage with no step does not make the plan empty")
	}
	if (Plan{Stages: []Stage{{Gestes: []Geste{pipeline("build", "")}}}}).Empty() {
		t.Error("a populated plan is seen as empty")
	}
}

// A stage with no step is refused: it does nothing and shifts the conditions of the
// following ones, which would read the outcome of a stage that attempted nothing.
func TestAStageWithoutAStepIsRefused(t *testing.T) {
	err := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{},
	}}.Validate()

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("sign-off: %v", err)
	}
	if !strings.Contains(err.Error(), "stage 2") {
		t.Errorf("message %q — it must name the offending stage", err)
	}
}

// A failure step in the first stage would never leave: nothing precedes it. Refused
// rather than left in place — a declaration that cannot trigger reads like one that
// does not trigger.
func TestAFailureStepInTheFirstStageIsRefused(t *testing.T) {
	err := Plan{Stages: []Stage{
		{Gestes: []Geste{{Kind: GesteAction, Ref: "prevenir", When: WhenFailure}}},
	}}.Validate()

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("sign-off: %v", err)
	}
	if !strings.Contains(err.Error(), "never leave") {
		t.Errorf("message %q — it must say the step would never leave", err)
	}
}

// Inconsistent declarations are refused, each one named.
func TestInconsistentDeclarationsAreRefused(t *testing.T) {
	testCases := map[string]Plan{
		"no stage": {},
		"unknown kind": {Stages: []Stage{{Gestes: []Geste{
			{Kind: "webhook", Ref: "x"},
		}}}},
		"missing reference": {Stages: []Stage{{Gestes: []Geste{
			{Kind: GestePipeline, Ref: "  "},
		}}}},
		"unknown condition": {Stages: []Stage{{Gestes: []Geste{
			{Kind: GestePipeline, Ref: "build", When: "maybe"},
		}}}},
	}

	for name, subject := range testCases {
		if err := subject.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}

// A step with no label is named by its reference: the screen must always have
// something to show.
func TestAStepWithoutALabelIsNamedByItsReference(t *testing.T) {
	if got := (Geste{Ref: "unit-tests"}).Name(); got != "unit-tests" {
		t.Errorf("name = %q", got)
	}
	if got := (Geste{Ref: "unit-tests", Label: "Tests unitaires"}).Name(); got != "Tests unitaires" {
		t.Errorf("name = %q", got)
	}
}

// An absent condition means success: it is the chaining one writes without
// thinking, and requiring it to be declared would weigh every plan down.
func TestAnAbsentConditionMeansSuccess(t *testing.T) {
	if got := (Geste{}).Condition(); got != WhenSuccess {
		t.Errorf("condition = %q", got)
	}
}

/*
 * A stage's role.
 *
 * It is where a release's life cycle is anchored in the plan.
 */

// A stage with no role takes no part in the life cycle.
//
// That is the default, and it is what makes a notification stage harmless: an
// unreachable webhook must not fail a release.
func TestAStageWithoutARoleIsNoAnchor(t *testing.T) {
	subject := Plan{Stages: []Stage{
		{Role: RoleBuild, Gestes: []Geste{{Kind: GestePipeline, Ref: "build"}}},
		{Label: "Warn", Gestes: []Geste{{Kind: GesteAction, Ref: "webhook"}}},
	}}
	if err := subject.Validate(); err != nil {
		t.Fatalf("%v", err)
	}

	if got := subject.LastWithRole(RoleBuild); got != 0 {
		t.Errorf("build stage = %d, expected 0", got)
	}
	// No test stage: the life cycle has no anchor for this transition, and the caller
	// must be able to see that rather than aim at stage 0 by default.
	if got := subject.LastWithRole(RoleTest); got != -1 {
		t.Errorf("test stage = %d, expected -1", got)
	}
}

// The last stage of a role is authoritative.
//
// A plan may build in two passes — a base image, then the application on top. It is
// the conclusion of the last pass that says the build is done; aiming at the first
// would advance the release too early.
func TestTheLastStageOfARoleIsAuthoritative(t *testing.T) {
	subject := Plan{Stages: []Stage{
		{Role: RoleBuild, Gestes: []Geste{{Kind: GestePipeline, Ref: "build-base"}}},
		{Role: RoleBuild, Gestes: []Geste{{Kind: GestePipeline, Ref: "build-app"}}},
		{Role: RoleTest, Gestes: []Geste{{Kind: GestePipeline, Ref: "unit-tests"}}},
	}}
	if err := subject.Validate(); err != nil {
		t.Fatalf("%v", err)
	}

	if got := subject.LastWithRole(RoleBuild); got != 1 {
		t.Errorf("build stage = %d, expected the second", got)
	}
	if got := subject.LastWithRole(RoleTest); got != 2 {
		t.Errorf("test stage = %d", got)
	}
}

// An invented role is refused when saved.
//
// Ignored, it would give a stage that runs without ever advancing the release —
// hence a release stuck building with nothing saying so.
func TestAnUnknownRoleIsRefused(t *testing.T) {
	err := Plan{Stages: []Stage{
		{Role: "deploiement", Gestes: []Geste{{Kind: GestePipeline, Ref: "build"}}},
	}}.Validate()

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, expected ErrInvalid", err)
	}
	// The message names the known roles: looking them up in the source is not an
	// answer to give someone reading an API message.
	for _, expected := range []string{"deploiement", "build", "test"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("message %q — it must carry %q", err, expected)
		}
	}
}

/*
 * Le weight d'un geste (doc 15 §15.6).
 */

func weight(value float64) *float64 { return &value }

// A negative weight is refused.
//
// It would lower the score when the step succeeds and raise it when it fails.
func TestANegativeWeightIsRefused(t *testing.T) {
	subject := Plan{Stages: []Stage{{
		Role: RoleTest,
		Gestes: []Geste{{
			Kind: GestePipeline, Ref: "unit-tests", Weight: weight(-5),
		}},
	}}}

	err := subject.Validate()
	if err == nil {
		t.Fatal("a negative weight was accepted")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("reason %q — it must name the mistake", err)
	}
}

// A weight set outside a verification stage is refused.
//
// It would be saved, displayed, and have no effect: a release's score is computed
// on the steps of its verification stage, the only one that measures anything.
func TestAWeightOutsideAVerificationStageIsRefused(t *testing.T) {
	for _, role := range []Role{RoleBuild, ""} {
		subject := Plan{Stages: []Stage{{
			Role: role,
			Gestes: []Geste{{
				Kind: GestePipeline, Ref: "build", Weight: weight(20),
			}},
		}}}

		err := subject.Validate()
		if err == nil {
			t.Errorf("role %q: a weight was accepted where nothing would read it", role)
			continue
		}
		if !strings.Contains(err.Error(), string(RoleTest)) {
			t.Errorf("role %q: reason %q — it must say where a weight counts", role, err)
		}
	}
}

// A zero weight is a decision, and validates.
func TestAZeroWeightValidates(t *testing.T) {
	subject := Plan{Stages: []Stage{{
		Role: RoleTest,
		Gestes: []Geste{{
			Kind: GestePipeline, Ref: "unit-tests", Weight: weight(0),
		}},
	}}}

	if err := subject.Validate(); err != nil {
		t.Errorf("a zero weight was refused: %v", err)
	}
}
