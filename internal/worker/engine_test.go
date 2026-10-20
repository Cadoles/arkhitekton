package worker

import (
	"context"
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
