package capability

import (
	"errors"
	"testing"
)

func TestARefReadsBackAsWritten(t *testing.T) {
	ref, err := ParseRef("php/unit-tests")
	if err != nil {
		t.Fatal(err)
	}
	if ref.String() != "php/unit-tests" || ref.Slug() != "php-unit-tests" || ref.LabelValue() != "php.unit-tests" {
		t.Errorf("ref = %+v", ref)
	}
}

func TestARefNeedsBothSegments(t *testing.T) {
	for _, raw := range []string{"unit-tests", "php/", "/unit-tests", "PHP/unit", "php/unit_tests", "a/b/c"} {
		if _, err := ParseRef(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestTwoCapabilitiesCannotClaimOneTektonPipeline(t *testing.T) {
	_, err := NewRegistry(
		Plugin{ID: "php", Version: "1", Pipelines: []Pipeline{{Name: "unit", Spec: PipelineSpec{PipelineRef: "unit"}}}},
		Plugin{ID: "acme", Version: "1", Pipelines: []Pipeline{{Name: "unit", Spec: PipelineSpec{PipelineRef: "unit"}}}},
	)
	if err == nil {
		t.Fatal("ambiguous registry accepted")
	}
}

func TestTheSameNameInTwoPluginsIsTwoCapabilities(t *testing.T) {
	registry, err := NewRegistry(
		Plugin{ID: "php", Version: "1", Pipelines: []Pipeline{{Name: "unit", Spec: PipelineSpec{PipelineRef: "php-unit"}}}},
		Plugin{ID: "go", Version: "1", Pipelines: []Pipeline{{Name: "unit", Spec: PipelineSpec{PipelineRef: "go-unit"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ref, _ := registry.ByPipelineRef("go-unit"); ref != (Ref{Plugin: "go", Name: "unit"}) {
		t.Errorf("go-unit read by %s", ref)
	}
}

func TestARunIsNotReadByAnotherVersion(t *testing.T) {
	registry, err := NewRegistry(Plugin{ID: "php", Version: "2", Pipelines: []Pipeline{
		{Name: "unit", Spec: PipelineSpec{PipelineRef: "php-unit"}, Interpreter: nil},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Interpreter(Ref{Plugin: "php", Name: "unit"}, "1"); !errors.Is(err, ErrVersionDrift) {
		t.Errorf("err = %v, expected a version drift", err)
	}
}
