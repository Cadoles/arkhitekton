package mse

import (
	"context"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

// integrationReader reads the integration tests' JUnit report.
type integrationReader struct{}

func (integrationReader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	const tool = "phpunit-integration"
	if record, readable := interpret.Readable(records, tool); readable {
		report := record.Payload.(parse.JUnit)
		cases := interpret.JUnitCases(report)
		return []facts.Set{{
			Shape: facts.ShapeCases, Group: "integration", Tool: tool, RawObject: record.Object,
			Cases: cases, DurationMS: report.DurationMS, Summary: interpret.SummarizeTests(cases),
		}}, nil
	}
	// The tests ran inside a deployed MSE: when they leave no report, saying they did not conclude
	// is what tells a failed deployment from a clean run.
	if summary, ran := interpret.NotConcluded(run, "run-integration-tests"); ran {
		return []facts.Set{{
			Shape: facts.ShapeCases, Group: "integration", Tool: tool,
			Status: facts.StatusNA, Summary: summary,
		}}, nil
	}
	return nil, nil
}

// e2eReader reads the end-to-end campaign's mochawesome report. Its Pipeline is the operator's, so
// only the report speaks: Cypress's exit code is the number of failed tests, not a verdict on
// whether it ran.
type e2eReader struct{}

func (e2eReader) Interpret(_ context.Context, _ capability.RunState, records []parse.Record) ([]facts.Set, error) {
	const tool = "cypress"
	record, readable := interpret.Readable(records, tool)
	if !readable {
		return nil, nil
	}
	report := record.Payload.(parse.Mochawesome)
	cases := interpret.MochaCases(report)
	return []facts.Set{{
		Shape: facts.ShapeCases, Group: "e2e", Tool: tool, RawObject: record.Object,
		Cases: cases, DurationMS: report.DurationMS, Summary: interpret.SummarizeTests(cases),
	}}, nil
}
