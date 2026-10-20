// Package interpret holds the readings most run interpreters share: test reports into cases,
// summary lines, and what the run's state says about a tool that left no report.
//
// Part of the contract plugins compile against. An interpreter is free to ignore it: these are
// the usual readings, not the only ones.
package interpret

import (
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// JUnitCases reads a JUnit report with the usual identity: the class name, else the suite path.
func JUnitCases(report parse.JUnit) []facts.Case {
	cases := make([]facts.Case, 0, len(report.Cases))
	for _, record := range report.Cases {
		testCase := facts.Case{
			Suite: record.Suite(), Name: record.Name,
			Status: facts.CasePassed, DurationMS: record.DurationMS,
		}
		switch record.Outcome {
		case parse.JUnitFailed, parse.JUnitErrored:
			testCase.Status = facts.CaseFailed
		case parse.JUnitRerun:
			// The case eventually passed: flaky, not failed.
			testCase.Status = facts.CaseFlaky
		case parse.JUnitSkipped:
			testCase.Status = facts.CaseSkipped
		}
		if record.Detail != nil {
			testCase.Message = parse.FirstLine(record.Detail.Text())
			testCase.Trace = strings.TrimSpace(record.Detail.Content)
		}
		cases = append(cases, testCase)
	}
	return cases
}

// MochaCases reads a mochawesome report. A test that passed after a retry is flaky; one neither
// passed, failed nor pending died with its spec, and is counted as not run rather than lost.
func MochaCases(report parse.Mochawesome) []facts.Case {
	cases := make([]facts.Case, 0, len(report.Tests))
	for _, test := range report.Tests {
		testCase := facts.Case{Suite: test.Suite(), Name: test.Name(), DurationMS: test.DurationMS}
		switch {
		case test.Pending:
			testCase.Status = facts.CaseSkipped
		case test.Fail:
			testCase.Status = facts.CaseFailed
			testCase.Message = parse.FirstLine(test.ErrMessage)
			testCase.Trace = strings.TrimSpace(test.ErrStack)
		case test.Pass && test.CurrentRetry > 0:
			testCase.Status = facts.CaseFlaky
			testCase.Message = parse.FirstLine(test.ErrMessage)
		case test.Pass:
			testCase.Status = facts.CasePassed
		default:
			testCase.Status = facts.CaseSkipped
			testCase.Message = "case not run"
		}
		cases = append(cases, testCase)
	}
	return cases
}

// SummarizeTests is the summary line of a set of test cases.
func SummarizeTests(cases []facts.Case) string {
	var failed, flaky, skipped int
	for _, testCase := range cases {
		switch testCase.Status {
		case facts.CaseFailed:
			failed++
		case facts.CaseFlaky:
			flaky++
		case facts.CaseSkipped:
			skipped++
		}
	}
	parts := []string{fmt.Sprintf("%d test(s)", len(cases))}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if flaky > 0 {
		parts = append(parts, fmt.Sprintf("%d flaky", flaky))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	return strings.Join(parts, ", ")
}

// SummarizeViolations is the summary line of violations projected onto cases, one file per suite.
func SummarizeViolations(cases []facts.Case, noun string) string {
	if len(cases) == 0 {
		return "no " + strings.TrimSuffix(noun, "(s)")
	}
	files := map[string]bool{}
	for _, testCase := range cases {
		files[testCase.Suite] = true
	}
	return fmt.Sprintf("%d %s in %d file(s)", len(cases), noun, len(files))
}

// NotConcluded says whether a task ran and left no readable report, and how to say so.
//
// A tool that crashed and a tool that found nothing both leave nothing to count. The run's state
// tells them apart: a task whose steps terminated ran; if its report is missing or unreadable, it
// did not conclude. A task that never started is not reported at all: nothing was measured, and
// nothing failed.
func NotConcluded(run capability.RunState, task string) (summary string, ran bool) {
	for _, state := range run.Tasks {
		if state.Name != task {
			continue
		}
		var code *int32
		for _, step := range state.Steps {
			if step.ExitCode != nil {
				code = step.ExitCode
				if *code != 0 {
					break
				}
			}
		}
		if code == nil {
			return "", false
		}
		summary := fmt.Sprintf("the tool did not conclude (code %d)", *code)
		if state.Reason != "" {
			summary += ": " + state.Reason
		}
		return summary, true
	}
	return "", false
}

// Readable returns the record of a tool when it was parsed without error.
func Readable(records []parse.Record, tool string) (parse.Record, bool) {
	for _, record := range records {
		if record.Tool == tool && record.Err == nil && record.Payload != nil {
			return record, true
		}
	}
	return parse.Record{}, false
}
