package results

import (
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

// ParseJUnit reads a JUnit XML report into a tool's report.
//
// The case's suite is its class name, else its suite path: renaming a file must not break a test's
// identity.
func ParseJUnit(family Family, tool string, raw []byte) (Report, error) {
	report := Report{Family: family, Tool: tool}

	source, err := parse.ParseJUnit(raw)
	if err != nil {
		return report, ErrUnsupported{Tool: tool, Detail: err.Error()}
	}

	for _, record := range source.Cases {
		report.Cases = append(report.Cases, junitToCase(record))
	}
	report.DurationMS = source.DurationMS
	report.tally()
	return report, nil
}

func junitToCase(record parse.JUnitCase) Case {
	testCase := Case{
		Suite: record.Suite(), Name: record.Name,
		Status: StatusPassed, DurationMS: record.DurationMS,
	}

	switch record.Outcome {
	case parse.JUnitFailed, parse.JUnitErrored:
		testCase.Status = StatusFailed
	case parse.JUnitRerun:
		// The case eventually passed: flaky, not failed.
		testCase.Status = StatusFlaky
	case parse.JUnitSkipped:
		testCase.Status = StatusSkipped
	}
	if record.Detail != nil {
		testCase.Message = firstLine(record.Detail.Text())
		testCase.Trace = strings.TrimSpace(record.Detail.Content)
	}
	return testCase
}

// SummarizeJUnit returns a report's summary line.
func SummarizeJUnit(report Report) string {
	parts := []string{fmt.Sprintf("%d test(s)", report.Total)}
	if report.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", report.Failed))
	}
	if report.Flaky > 0 {
		parts = append(parts, fmt.Sprintf("%d flaky", report.Flaky))
	}
	if report.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", report.Skipped))
	}
	return strings.Join(parts, ", ")
}
