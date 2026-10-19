package results

import (
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

// ParseMochawesome reads a mochawesome report into Cypress's report.
//
// The JSON rather than the HTML: the HTML is readable by a human and by nobody else.
func ParseMochawesome(raw []byte) (Report, error) {
	report := Report{Family: FamilyE2E, Tool: "cypress"}

	source, err := parse.ParseMochawesome(raw)
	if err != nil {
		return report, ErrUnsupported{Tool: "cypress", Detail: err.Error()}
	}

	for _, test := range source.Tests {
		report.Cases = append(report.Cases, mochaToCase(test))
	}
	report.DurationMS = source.DurationMS
	report.tally()
	report.Summary = SummarizeJUnit(report)
	return report, nil
}

func mochaToCase(test parse.MochaTest) Case {
	testCase := Case{Suite: test.Suite(), Name: test.Name(), DurationMS: test.DurationMS}

	switch {
	case test.Pending:
		testCase.Status = StatusSkipped
	case test.Fail:
		testCase.Status = StatusFailed
		testCase.Message = firstLine(test.ErrMessage)
		testCase.Trace = strings.TrimSpace(test.ErrStack)
	case test.Pass && test.CurrentRetry > 0:
		// Passed, but not on the first attempt.
		testCase.Status = StatusFlaky
		testCase.Message = firstLine(test.ErrMessage)
	case test.Pass:
		testCase.Status = StatusPassed
	default:
		// Neither passed, failed nor pending: Cypress produces this state when the spec dies before
		// running the case. Ignoring it would lose a test.
		testCase.Status = StatusSkipped
		testCase.Message = "case not run"
	}
	return testCase
}
