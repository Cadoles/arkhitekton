package capabilities

import (
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
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
