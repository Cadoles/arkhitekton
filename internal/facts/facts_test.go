package facts

import "testing"

func TestAProducerReadsBackAsWritten(t *testing.T) {
	producer, err := ParseProducer("php@1.4.2")
	if err != nil {
		t.Fatal(err)
	}
	if producer != (Producer{Plugin: "php", Version: "1.4.2"}) || producer.String() != "php@1.4.2" {
		t.Errorf("producer = %+v", producer)
	}
	if _, err := ParseProducer("php"); err == nil {
		t.Error("producer without version accepted")
	}
}

func TestASetCarriesOnlyItsShape(t *testing.T) {
	set := Set{Shape: ShapeCases, Group: "unit", Tool: "phpunit",
		Cases: []Case{{Suite: "A", Name: "b", Status: CasePassed}}, Measures: &Measures{Scenario: "s"}}
	if err := set.Validate(); err == nil {
		t.Error("cases with measures accepted")
	}
}

func TestACaseNeedsItsIdentity(t *testing.T) {
	set := Set{Shape: ShapeCases, Group: "unit", Tool: "phpunit",
		Cases: []Case{{Name: "b", Status: CasePassed}}}
	if err := set.Validate(); err == nil {
		t.Error("case without suite accepted")
	}
}

// A crashed tool leaves a verdict and nothing else. That must stay storable: it is the only trace
// that the tool did not run.
func TestACrashedToolIsAVerdictWithoutContent(t *testing.T) {
	set := Set{Shape: ShapeCases, Group: "static", Tool: "phpstan", Status: StatusNA, Summary: "exit 137"}
	if err := set.Validate(); err != nil {
		t.Error(err)
	}
}

func TestOnlyCasesDeriveTheirStatus(t *testing.T) {
	set := Set{Shape: ShapeMeasures, Group: "load", Tool: "k6", Measures: &Measures{Scenario: "s"}}
	if err := set.Validate(); err == nil {
		t.Error("measures without status accepted")
	}
}
