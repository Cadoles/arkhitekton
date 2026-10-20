package main

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
)

func TestTheBuiltinPluginsFormARegistry(t *testing.T) {
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	if ref, found := registry.ByPipelineRef("load-test"); !found || ref.String() != "k6/load" {
		t.Errorf("load-test read by %v", ref)
	}
}

// A capability that ships manifests ships the Pipeline it names.
func TestAShippedPipelineIsAmongItsManifests(t *testing.T) {
	registry, _ := builtin.Registry()
	for _, ref := range registry.Refs() {
		pipeline, _, _ := registry.Pipeline(ref)
		if len(pipeline.Spec.Manifests) == 0 {
			continue
		}
		var found bool
		for _, manifest := range pipeline.Spec.Manifests {
			if manifest.Kind == "Pipeline" && manifest.Name == pipeline.Spec.PipelineRef {
				found = true
			}
		}
		if !found {
			t.Errorf("%s ships no Pipeline %q", ref, pipeline.Spec.PipelineRef)
		}
	}
}

func shippedCatalogue(t *testing.T) *catalog.Catalog {
	t.Helper()
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := catalog.Load("../../deploy/base/config/catalog.yaml", catalog.WithCapabilities(registry))
	if err != nil {
		t.Fatalf("shipped catalogue: %v", err)
	}
	return shipped
}

// The shipped catalogue as the scheduler reads it: every field that builds a PipelineRun.
func TestTheShippedCatalogueResolvesAsRecorded(t *testing.T) {
	type resolved struct {
		catalog.Pipeline
		Capability    string
		Unavailable   string
		ExternalTasks []string
	}
	var pipelines []resolved
	for _, pipeline := range shippedCatalogue(t).Pipelines {
		pipelines = append(pipelines, resolved{pipeline, pipeline.Capability, pipeline.Unavailable, pipeline.ExternalTasks})
	}
	golden(t, "catalogue.json", pipelines)
}
