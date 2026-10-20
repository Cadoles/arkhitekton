package facts

import (
	"strings"
	"testing"
)

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

// Attachments are kept beside any shape; a set of attachments carries nothing else and states its
// own verdict.
func TestAttachmentsAreValidatedWhateverTheShape(t *testing.T) {
	video := Attachment{Name: "login.cy.js.mp4", MediaType: "video/mp4", Object: "attachments/run/a/login.mp4", Size: 10}
	cases := Set{Shape: ShapeCases, Group: "e2e", Tool: "cypress", Attachments: []Attachment{video},
		Cases: []Case{{Suite: "login", Name: "works", Status: CasePassed}}}
	if err := cases.Validate(); err != nil {
		t.Errorf("cases with a video: %v", err)
	}

	only := Set{Shape: ShapeAttachments, Group: "report", Tool: "html", Status: StatusNA, Attachments: []Attachment{video}}
	if err := only.Validate(); err != nil {
		t.Errorf("a set of attachments: %v", err)
	}
	only.Cases = cases.Cases
	if err := only.Validate(); err == nil {
		t.Error("a set of attachments carrying cases accepted")
	}

	for _, broken := range []Attachment{
		{Name: "", MediaType: "image/png", Object: "o"},
		{Name: "a", MediaType: "png", Object: "o"},
		{Name: "a", MediaType: "image/png", Object: ""},
		{Name: "a", MediaType: "image/png", Object: "o", Case: &CaseRef{Suite: "s"}},
		{Name: "a", MediaType: "image/png", Object: "o", Attributes: map[string]string{"Bad Key": "x"}},
	} {
		set := Set{Shape: ShapeAttachments, Group: "g", Tool: "t", Status: StatusNA, Attachments: []Attachment{broken}}
		if err := set.Validate(); err == nil {
			t.Errorf("%+v accepted", broken)
		}
	}
}

// Free attributes stay readable: named, bounded.
func TestAttributesAreBounded(t *testing.T) {
	set := Set{Shape: ShapeCases, Group: "e2e", Tool: "cypress", Attributes: map[string]string{"browser": "chrome 128"}}
	if err := set.Validate(); err != nil {
		t.Errorf("%v", err)
	}
	set.Attributes["notes"] = strings.Repeat("x", 2000)
	if err := set.Validate(); err == nil {
		t.Error("an attribute of 2000 characters accepted")
	}
}
