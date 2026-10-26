package capabilities

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

func TestTwoCapabilitiesCannotClaimOneTektonPipeline(t *testing.T) {
	_, err := New(
		capability.Plugin{ID: "php", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{Name: "unit", Spec: capability.PipelineSpec{PipelineRef: "unit"}}}},
		capability.Plugin{ID: "acme", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{Name: "unit", Spec: capability.PipelineSpec{PipelineRef: "unit"}}}},
	)
	if err == nil {
		t.Fatal("ambiguous registry accepted")
	}
}

func TestTheSameNameInTwoPluginsIsTwoCapabilities(t *testing.T) {
	registry, err := New(
		capability.Plugin{ID: "php", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{Name: "unit", Spec: capability.PipelineSpec{PipelineRef: "php-unit"}}}},
		capability.Plugin{ID: "go", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{Name: "unit", Spec: capability.PipelineSpec{PipelineRef: "go-unit"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref, _ := registry.ByPipelineRef("go-unit"); ref != (capability.Ref{Plugin: "go", Name: "unit"}) {
		t.Errorf("go-unit read by %s", ref)
	}
}

func TestARunIsNotReadByAnotherVersion(t *testing.T) {
	registry, err := New(capability.Plugin{ID: "php", Version: "2", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{
		{Name: "unit", Spec: capability.PipelineSpec{PipelineRef: "php-unit"}, Interpreter: nil},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Interpreter(capability.Ref{Plugin: "php", Name: "unit"}, "1"); !errors.Is(err, ErrVersionDrift) {
		t.Errorf("err = %v, expected a version drift", err)
	}
}

func TestAPluginOfAnotherMajorVersionIsRefused(t *testing.T) {
	for _, version := range []string{"", "0", "2.1"} {
		if _, err := New(capability.Plugin{ID: "php", Version: "1", APIVersion: version}); err == nil {
			t.Errorf("sdk %q accepted", version)
		}
	}
	if _, err := New(capability.Plugin{ID: "php", Version: "1", APIVersion: sdk.APIVersion + ".3"}); err != nil {
		t.Errorf("a later minor version refused: %v", err)
	}
}

// A plugin declaring actions with nothing to carry them out is refused at start-up, rather than
// failing on the first reaction that uses one.
func TestAPluginDeclaringActionsItCannotRunIsRefused(t *testing.T) {
	_, err := New(capability.Plugin{
		ID: "forge", Version: "1", APIVersion: sdk.APIVersion,
		Actions: []capability.ActionSpec{{Suffix: "status"}},
	})
	if err == nil {
		t.Error("actions without an executor accepted")
	}
}

// A declared reading becomes the pipeline's interpreter; a pipeline cannot read its runs both in
// code and by declaration, and a reading that does not hold is refused at load.
func TestADeclaredReadingBecomesTheInterpreter(t *testing.T) {
	pipeline := capability.Pipeline{Name: "unit", Spec: capability.PipelineSpec{
		PipelineRef: "unit", Produces: []capability.Product{capability.ProductResults},
		Artifacts: []capability.Artifact{{Name: "junit", Object: "junit.xml", Format: capability.FormatFile,
			Members: []capability.Member{{Match: "junit.xml", Parser: parse.JUnitXML, Tool: "phpunit"}}}},
	}, Reading: []byte(`
apiVersion: arkhitekton.reading/v1
tools:
  - tool: phpunit
    group: unit
    summary: tests
    cases:
      - { from: junit.cases, suite: "{path}", name: "{name}", status: passed }
`)}
	plugin := capability.Plugin{ID: "php", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{pipeline}}

	registry, err := New(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if interpreter, err := registry.Interpreter(capability.Ref{Plugin: "php", Name: "unit"}, "1"); err != nil || interpreter == nil {
		t.Errorf("interpreter %v, err %v", interpreter, err)
	}

	both := plugin
	both.Pipelines = []capability.Pipeline{pipeline}
	both.Pipelines[0].Interpreter = noReading{}
	if _, err := New(both); err == nil {
		t.Error("a pipeline reading in code and by declaration accepted")
	}

	broken := plugin
	broken.Pipelines = []capability.Pipeline{pipeline}
	broken.Pipelines[0].Reading = []byte("apiVersion: arkhitekton.reading/v1\ntools: []\n")
	if _, err := New(broken); err == nil {
		t.Error("an empty reading accepted")
	}
}

type noReading struct{}

func (noReading) Interpret(context.Context, capability.RunState, []parse.Record) ([]facts.Set, error) {
	return nil, nil
}

// An update leaves the earlier version behind to read the runs it launched; it launches nothing.
func TestAnEarlierVersionReadsTheRunsItLaunched(t *testing.T) {
	unit := func(interpreter capability.Interpreter) []capability.Pipeline {
		return []capability.Pipeline{{Name: "unit", Interpreter: interpreter, Spec: capability.PipelineSpec{
			PipelineRef: "go-unit", Produces: []capability.Product{capability.ProductResults}}}}
	}
	earlier, current := version("1.0.0"), version("1.0.1")
	registry, err := New(capability.Plugin{ID: "go", Version: "1.0.1", APIVersion: sdk.APIVersion, Pipelines: unit(current)})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Retain(capability.Plugin{ID: "go", Version: "1.0.0", APIVersion: sdk.APIVersion, Pipelines: unit(earlier)}); err != nil {
		t.Fatal(err)
	}

	ref := capability.Ref{Plugin: "go", Name: "unit"}
	for recorded, expected := range map[string]capability.Interpreter{"1.0.0": earlier, "1.0.1": current} {
		if interpreter, err := registry.Interpreter(ref, recorded); err != nil || interpreter != expected {
			t.Errorf("a run launched by %s read by %v, %v", recorded, interpreter, err)
		}
	}
	if _, plugin, _ := registry.Pipeline(ref); plugin.Version != "1.0.1" {
		t.Errorf("launched by %s", plugin.Version)
	}
	if _, err := registry.Interpreter(ref, "0.9.0"); !errors.Is(err, ErrVersionDrift) {
		t.Errorf("a version neither carried nor retained: %v", err)
	}
	if err := registry.Retain(capability.Plugin{ID: "other", Version: "1", APIVersion: sdk.APIVersion}); err == nil {
		t.Error("an earlier version of a plugin not carried retained")
	}
}

// version is an interpreter told apart by its version.
type version string

func (version) Interpret(context.Context, capability.RunState, []parse.Record) ([]facts.Set, error) {
	return nil, nil
}
