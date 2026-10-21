package guest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// echo turns each record it reads into a case naming what it received.
type echo struct{}

func (echo) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	set := facts.Set{Shape: facts.ShapeCases, Group: "unit", Tool: "echo", Summary: run.Status}
	for _, record := range records {
		name := string(record.Parser)
		switch payload := record.Payload.(type) {
		case parse.Bytes:
			name += ":" + string(payload)
		case parse.JUnit:
			name += ":" + payload.Cases[0].Name
		case parse.K6:
			name += ":" + strings.Join(keys(payload.Metrics), ",")
		}
		if record.Err != nil {
			name += ":" + record.Err.Error()
		}
		set.Cases = append(set.Cases, facts.Case{Suite: record.Tool, Name: name, Status: facts.CasePassed})
	}
	return []facts.Set{set}, nil
}

func keys(metrics map[string]parse.K6Metric) []string {
	var names []string
	for name := range metrics {
		names = append(names, name)
	}
	return names
}

func input(t *testing.T, records ...parse.Record) []byte {
	t.Helper()
	encoded := Input{ABI: ABI, Run: capability.RunState{Status: "succeeded"}}
	for _, record := range records {
		wire, err := EncodeRecord(record)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Records = append(encoded.Records, wire)
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func answer(t *testing.T, raw []byte) Output {
	t.Helper()
	var output Output
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return output
}

// Records cross the boundary in their own types: bytes for raw, the parsed report otherwise, the
// parsing failure as text. A k6 metric's statistics travel, its error value cannot.
func TestRecordsCrossTheBoundaryInTheirTypes(t *testing.T) {
	output := answer(t, Handle(input(t,
		parse.Record{Tool: "gotest", Parser: parse.Raw, Payload: parse.Bytes(`{"Action":"pass"}`)},
		parse.Record{Tool: "phpunit", Parser: parse.JUnitXML, Payload: parse.JUnit{Cases: []parse.JUnitCase{{Name: "testA"}}}},
		parse.Record{Tool: "k6", Parser: parse.K6SummaryJSON, Payload: parse.K6{Metrics: map[string]parse.K6Metric{
			"http_reqs": {Values: map[string]float64{"rate": 4}, Err: errors.New("odd")}}}},
		parse.Record{Tool: "phpstan", Parser: parse.PHPStanJSON, Err: errors.New("truncated")},
	), echo{}))

	if output.Error != "" || len(output.Sets) != 1 || output.Sets[0].Summary != "succeeded" {
		t.Fatalf("output = %+v", output)
	}
	var names []string
	for _, c := range output.Sets[0].Cases {
		names = append(names, c.Name)
	}
	expected := []string{`raw:{"Action":"pass"}`, "junit-xml:testA", "k6-summary-json:http_reqs", "phpstan-json:truncated"}
	if strings.Join(names, "|") != strings.Join(expected, "|") {
		t.Errorf("names = %q", names)
	}
}

type panicking struct{}

func (panicking) Interpret(context.Context, capability.RunState, []parse.Record) ([]facts.Set, error) {
	panic("index out of range")
}

// A panicking interpreter answers with an error rather than bringing the module down; so do a
// foreign ABI and a module that registered nothing.
func TestFailuresAnswerWithAnError(t *testing.T) {
	if output := answer(t, Handle(input(t), panicking{})); !strings.Contains(output.Error, "index out of range") {
		t.Errorf("panic: %+v", output)
	}
	if output := answer(t, Handle([]byte(`{"abi":2}`), echo{})); !strings.Contains(output.Error, "ABI 2") {
		t.Errorf("abi: %+v", output)
	}
	if output := answer(t, Handle(input(t), nil)); !strings.Contains(output.Error, "Register") {
		t.Errorf("nothing registered: %+v", output)
	}
	if output := answer(t, Handle([]byte("not json"), echo{})); output.Error == "" {
		t.Errorf("unreadable input: %+v", output)
	}
}
