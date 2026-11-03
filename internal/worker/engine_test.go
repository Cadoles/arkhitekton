package worker

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The order between recording a run and concluding its step.
 */

// orderedStore keeps the order of the writes.
type orderedStore struct {
	*fakePlanStore
	order    []string
	results  map[int64]map[string]string
	produits []store.PlanArtifact
}

func (m *orderedStore) RunResults(_ context.Context, jobID int64) (map[string]string, error) {
	m.order = append(m.order, "read")
	return m.results[jobID], nil
}

func (m *orderedStore) RecordPlanArtifacts(
	_ context.Context, _ int64, _ *int64, artifacts []store.PlanArtifact,
) error {
	m.order = append(m.order, "collected")
	m.produits = append(m.produits, artifacts...)
	return nil
}

func (m *orderedStore) PlanArtifacts(context.Context, int64) ([]store.PlanArtifact, error) {
	return m.produits, nil
}

// A concluded step's results become its artifacts.
//
// # What this test holds, and nothing held
//
// The collection **re-reads** the run's results by its job. They must therefore be written
// before the step is concluded. They were not: the observation concluded the job *then*
// recorded the run, and the collection read an empty row. Three successful builds, no
// artifact kept — observed on the cluster, not in a test.
//
// This test holds the half that depends on the engine: given a run whose results are
// readable, concluding the step keeps them.
func TestAConcludedStepsResultsBecomeItsArtifacts(t *testing.T) {
	base := &orderedStore{
		fakePlanStore: newFakePlanStore(),
		results: map[int64]map[string]string{
			77: {"image": "reg.example.fr/portal:1.0.0", "tag": "1.0.0"},
		},
	}

	subject := plan.Plan{Stages: []plan.Stage{{
		Role:   plan.RoleBuild,
		Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "build", Label: "portal"}},
	}}}

	engine := NewEngine(base, stepRunnerFunc(
		func(context.Context, store.PlanRun, plan.Geste) (*int64, error) {
			id := int64(77)
			return &id, nil
		}), quietLogger()).
		WithArtifacts(base, func(string) string { return "image" })

	engine.Trigger(context.Background(), store.PlanRun{
		Trigger: "trial", EventKey: "trial:1", Plan: subject,
	})
	engine.Advance(context.Background(), 77, plan.Succeeded)

	if len(base.produits) != 2 {
		t.Fatalf("%d artifact(s) kept, expected image and tag: %+v",
			len(base.produits), base.produits)
	}
	byName := map[string]store.PlanArtifact{}
	for _, a := range base.produits {
		byName[a.Name] = a
	}
	if byName["image"].Reference != "reg.example.fr/portal:1.0.0" {
		t.Errorf("image = %q", byName["image"].Reference)
	}
	// The kind comes from the catalogue's `produces`, not from the result's name.
	if byName["image"].Kind != "image" {
		t.Errorf("nature = %q", byName["image"].Kind)
	}
}

// A stage that settles at once does not stop the run. An action, a skipped step: nothing comes
// back later to start what follows, so the engine goes on by itself.
func TestStagesThatSettleAtOnceLeadToTheNext(t *testing.T) {
	base := newFakePlanStore()
	var ran []string
	runner := stepRunnerFunc(func(_ context.Context, _ store.PlanRun, geste plan.Geste) (*int64, error) {
		ran = append(ran, geste.Ref)
		if geste.Kind == plan.GesteAction {
			return nil, nil
		}
		id := int64(len(ran))
		return &id, nil
	})
	engine := NewEngine(base, runner, quietLogger())

	subject := plan.Plan{Stages: []plan.Stage{
		{Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "status-pending"}}},
		{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "build"}}},
		{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "unit-tests"}}},
		{Gestes: []plan.Geste{
			{Kind: plan.GesteAction, Ref: "release"},
			{Kind: plan.GesteAction, Ref: "summary", When: plan.WhenAlways},
		}},
	}}
	engine.Trigger(context.Background(), store.PlanRun{Trigger: "trial", EventKey: "trial:1", Plan: subject})
	if strings.Join(ran, ",") != "status-pending,build" {
		t.Fatalf("ran %v: the build waits behind an action", ran)
	}

	// The build fails: the tests are skipped, the release stays put, the summary leaves, and the
	// run concludes, all in the same pass.
	engine.Advance(context.Background(), 2, plan.Failed)
	if strings.Join(ran, ",") != "status-pending,build,summary" {
		t.Errorf("ran %v", ran)
	}
	if run, _ := base.PlanRunByID(context.Background(), 1); run.State != store.PlanFailed {
		t.Errorf("run state %q, expected failed", run.State)
	}
}
