package app

import (
	"testing"

	"github.com/Cadoles/arkhitekton/internal/capabilities"
	"github.com/Cadoles/arkhitekton/internal/catalog"
	"github.com/Cadoles/arkhitekton/internal/plugins/builtin"
	"github.com/Cadoles/arkhitekton/sdk"
	"github.com/Cadoles/arkhitekton/sdk/capability"
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
	registry, err := carriedRegistry()
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := catalog.Load("../deploy/base/config/catalog.yaml", catalog.WithCapabilities(registry))
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

// A bound capability's definitions carry their plugin: without it, uninstalling the plugin could
// not withdraw them.
func TestBoundDefinitionsNameTheirPlugin(t *testing.T) {
	registry, err := capabilities.New(capability.Plugin{ID: "gotest", Version: "1", APIVersion: sdk.APIVersion,
		Pipelines: []capability.Pipeline{{Name: "unit-tests", Spec: capability.PipelineSpec{
			PipelineRef: "go-unit-tests",
			Manifests:   []capability.Manifest{{Kind: "Pipeline", Name: "go-unit-tests"}, {Kind: "Task", Name: "go-test"}},
		}}}})
	if err != nil {
		t.Fatal(err)
	}
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{{Kind: "gotest/unit-tests", Capability: "gotest/unit-tests"}}}

	resources, err := boundResources(pipelines, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("resources = %+v", resources)
	}
	for _, resource := range resources {
		if resource.Plugin != "gotest" {
			t.Errorf("%s ships with plugin %q", resource.Name, resource.Plugin)
		}
	}
}

// The review binds three secrets, the way its operator would from a file: each workspace gets its
// source, and none is a service account. Its settings are the plugin's, not a binding's.
func TestTheReviewBindsItsSecretsAndConfiguration(t *testing.T) {
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := catalog.Parse([]byte(`
capabilities:
  - ref: review/pull-request
    timeout: 45m
    needs:
      source-read:  { secret: { configKey: project.deployKeySecret } }
      model-access: { secret: { configKey: project.reviewSecret } }
      forge-read:   { secret: { secretName: arkhitekton-review-forge } }
    volumes:
      shared-data: { storageClassName: standard }
`), catalog.WithCapabilities(registry))
	if err != nil {
		t.Fatal(err)
	}
	pipeline, found := parsed.Find("review/pull-request")
	if !found || pipeline.Unavailable != "" || pipeline.ServiceAccountName != "" || pipeline.MaxConcurrent != 1 {
		t.Fatalf("pipeline = %+v", pipeline)
	}
	sources := map[string]string{}
	for _, workspace := range pipeline.Workspaces {
		switch {
		case workspace.Secret != nil:
			sources[workspace.Name] = workspace.Secret.SecretName + workspace.Secret.ConfigKey
		case workspace.VolumeClaimTemplate != nil:
			sources[workspace.Name] = "volume"
		}
	}
	expected := map[string]string{
		"git-credentials": "project.deployKeySecret", "model-access": "project.reviewSecret",
		"forge-token": "arkhitekton-review-forge", "shared-data": "volume",
	}
	if len(sources) != len(expected) {
		t.Errorf("workspaces = %v", sources)
	}
	for name, source := range expected {
		if sources[name] != source {
			t.Errorf("%s = %q, expected %q", name, sources[name], source)
		}
	}
}
