package http

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The score, step by step.
 *
 * What these tests hold is the **change of axis**: two steps of the same kind count separately,
 * and each weighs what it declares. The model by signal could say neither.
 */

// testScore implements what the score needs, and nothing else.
type scoreDeTest struct {
	run     store.PlanRun
	sansRun bool
	// steps attaches a result to the position of the step that produced it, as the chain
	// result → run → job → step would.
	steps map[int64]store.GesteRef
}

func (s *scoreDeTest) PlanRunByEvent(_ context.Context, _, _ string) (store.PlanRun, error) {
	if s.sansRun {
		return store.PlanRun{}, store.ErrPlanRunNotFound
	}
	return s.run, nil
}

func (s *scoreDeTest) ResultGestes(context.Context, int64) (map[int64]store.GesteRef, error) {
	return s.steps, nil
}

func (s *scoreDeTest) LatestSecurityAudit(context.Context, int64) (store.SecurityAudit, error) {
	return store.SecurityAudit{}, store.ErrAuditNotFound
}

func (s *scoreDeTest) ListReleaseLoadResults(context.Context, int64) ([]store.LoadResult, error) {
	return nil, nil
}

func (s *scoreDeTest) ListResultMeasures(context.Context, int64) ([]store.BenchMeasure, error) {
	return nil, nil
}

func (s *scoreDeTest) Baselines(context.Context, string) (map[string]store.Baseline, error) {
	return nil, nil
}

// verificationPlan returns a plan with one verification stage.
func verificationPlan(steps ...plan.Geste) store.PlanRun {
	return store.PlanRun{
		Trigger: plan.ReleaseTrigger, EventKey: event.ReleaseCreatedKey(7),
		Plan: plan.Plan{Stages: []plan.Stage{
			{Role: plan.RoleBuild, Gestes: []plan.Geste{
				{Kind: plan.GestePipeline, Ref: "build", Label: "portal"},
			}},
			{Role: plan.RoleTest, Gestes: steps},
		}},
	}
}

func testReport(id int64, family string, total, failed int) store.TestResult {
	return store.TestResult{
		ID: id, Family: family, Tool: family, Status: "passed",
		Total: total, Passed: total - failed, Failed: failed,
	}
}

func weightOf(value float64) *float64 { return &value }

// Two campaigns of the same nature count separately, and each carries its own weight.
//
// That is the case the per-signal model could not express: the two were conflated into a
// single component, and no weighting could separate them.
func TestTwoStepsOfTheSameKindCountSeparately(t *testing.T) {
	source := &scoreDeTest{
		run: verificationPlan(
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests",
				Label: "E2E portail", Weight: weightOf(90)},
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests",
				Label: "E2E back-office", Weight: weightOf(10)},
		),
		// The first report comes from step 1.0, the second from step 1.1.
		steps: map[int64]store.GesteRef{
			1: {Stage: 1, Position: 0},
			2: {Stage: 1, Position: 1},
		},
	}

	// The portal passes, the back office fails entirely.
	reports := []store.TestResult{
		testReport(1, "e2e", 10, 0),
		testReport(2, "e2e", 10, 10),
	}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(breakdown.Signals) != 2 {
		t.Fatalf("%d component(s), expected two steps", len(breakdown.Signals))
	}
	if breakdown.Signals[0].Label != "E2E portail" ||
		breakdown.Signals[1].Label != "E2E back-office" {
		t.Errorf("labels = %q / %q — the plan's order is not kept",
			breakdown.Signals[0].Label, breakdown.Signals[1].Label)
	}
	// 90% at 100 and 10% at 0. One axis per signal would have summed both reports into one —
	// 10 failures out of 20 — and returned 50.
	if breakdown.Score != 90 {
		t.Errorf("score = %d, expected 90", breakdown.Score)
	}
}

// A step that declares nothing weighs what its nature weighed.
//
// Nobody loses the agreed weighting by typing nothing.
func TestAStepWithNoWeightCarriesTheOriginalWeighting(t *testing.T) {
	source := &scoreDeTest{
		run: verificationPlan(
			plan.Geste{Kind: plan.GestePipeline, Ref: "unit-tests", Label: "Tests unitaires"},
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests", Label: "Tests E2E"},
		),
		steps: map[int64]store.GesteRef{
			1: {Stage: 1, Position: 0},
			2: {Stage: 1, Position: 1},
		},
	}
	reports := []store.TestResult{testReport(1, "unit", 10, 0), testReport(2, "e2e", 10, 0)}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	attendus := map[string]float64{"Tests unitaires": 25, "Tests E2E": 20}
	for _, composant := range breakdown.Signals {
		if attendu, connu := attendus[composant.Label]; connu && composant.Declared != attendu {
			t.Errorf("%s: declared weight = %v, expected %v (original weighting)",
				composant.Label, composant.Declared, attendu)
		}
	}
}

// A step that produced nothing stays displayed, and its weight reads.
//
// The catalogue says what it would have measured: without it, the cell would disappear
// instead of saying it is waiting.
func TestAStepWithNoResultStaysDisplayedWithItsWeight(t *testing.T) {
	registry := &catalog.Catalog{Pipelines: []catalog.Pipeline{{
		Kind: "unit-tests", PipelineRef: "unit",
		Produces: []catalog.Product{catalog.ProductResults},
		Uploads: map[string]catalog.Upload{
			"results": {Param: "results-upload-url", Artifact: "unit.xml", Family: "unit"},
		},
	}}}

	source := &scoreDeTest{
		run: verificationPlan(plan.Geste{
			Kind: plan.GestePipeline, Ref: "unit-tests", Label: "Tests unitaires",
		}),
		steps: map[int64]store.GesteRef{},
	}

	breakdown, err := computeScore(context.Background(), source, 7, nil, registry)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(breakdown.Signals) != 1 {
		t.Fatalf("%d composant(s)", len(breakdown.Signals))
	}
	composant := breakdown.Signals[0]
	if composant.Measured {
		t.Error("a step with no result declares itself measured")
	}
	// The weight comes from the signal, which only the catalogue could name here.
	if composant.Declared != 25 {
		t.Errorf("declared weight = %v, expected 25 — the catalogue did not name its kind",
			composant.Declared)
	}
	if len(breakdown.Redistributed) != 1 {
		t.Errorf("redistributed = %v", breakdown.Redistributed)
	}
}

// A zero weight removes the step from the score, and says so.
func TestAZeroWeightStepDoesNotScore(t *testing.T) {
	source := &scoreDeTest{
		run: verificationPlan(
			plan.Geste{Kind: plan.GestePipeline, Ref: "unit-tests",
				Label: "Tests unitaires", Weight: weightOf(100)},
			plan.Geste{Kind: plan.GestePipeline, Ref: "e2e-tests",
				Label: "E2E indicatif", Weight: weightOf(0)},
		),
		steps: map[int64]store.GesteRef{
			1: {Stage: 1, Position: 0},
			2: {Stage: 1, Position: 1},
		},
	}
	// The end-to-end tests fail entirely, and must not count.
	reports := []store.TestResult{testReport(1, "unit", 10, 0), testReport(2, "e2e", 10, 10)}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if breakdown.Score != 100 {
		t.Errorf("score = %d, expected 100 — the zero-weight step counted", breakdown.Score)
	}
	if len(breakdown.Signals) != 2 {
		t.Errorf("%d component(s) — the zero-weight one vanished from the screen",
			len(breakdown.Signals))
	}
}

// A release with no plan keeps its score, computed by family.
//
// Releases predating plans have none. Losing their score would be worse than computing it
// on an axis that no longer exists.
func TestAReleaseWithNoPlanKeepsItsScoreByKind(t *testing.T) {
	source := &scoreDeTest{sansRun: true}
	reports := []store.TestResult{testReport(1, "unit", 10, 0)}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if !breakdown.Measured || breakdown.Score != 100 {
		t.Errorf("score = %d (measured: %v)", breakdown.Score, breakdown.Measured)
	}
	// Six components, one per nature, and their keys say so.
	if len(breakdown.Signals) != 6 {
		t.Fatalf("%d component(s), expected the six kinds", len(breakdown.Signals))
	}
	if breakdown.Signals[0].Key != results.SignalKey(results.SignalUnit) {
		t.Errorf("key = %q, expected that of one component per kind",
			breakdown.Signals[0].Key)
	}
}

// A result no step produced does not enter the score.
//
// It comes from a manual launch from the queue, which no plan caused. Counting it would
// grade a release on a run its automation did not ask for.
func TestAResultOutsideThePlanDoesNotEnterTheScore(t *testing.T) {
	source := &scoreDeTest{
		run: verificationPlan(plan.Geste{
			Kind: plan.GestePipeline, Ref: "unit-tests",
			Label: "Tests unitaires", Weight: weightOf(100),
		}),
		steps: map[int64]store.GesteRef{1: {Stage: 1, Position: 0}},
	}
	reports := []store.TestResult{
		testReport(1, "unit", 10, 0),
		// Relaunched by hand, and catastrophic. It must not count.
		testReport(2, "unit", 10, 10),
	}

	breakdown, err := computeScore(context.Background(), source, 7, reports, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if breakdown.Score != 100 {
		t.Errorf("score = %d — a result outside the plan counted", breakdown.Score)
	}
}
