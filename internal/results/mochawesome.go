package results

import (
	"encoding/json"
	"strings"
)

/*
 * Mochawesome — Cypress's JSON output.
 *
 * The JSON rather than the HTML, and it is a decision, not a convenience: the HTML is readable by
 * a human and by nobody else. The pipeline produces both, and it is the JSON that is parsed.
 *
 * The format nests the suites, and reports flakiness differently from JUnit: a case that passed
 * after a retry carries `currentRetry > 0` **and** `pass: true`. Reading it as a success would
 * erase the E2E campaign's only flakiness signal, that is, precisely what one is trying to watch.
 */

type mochawesomeReport struct {
	Stats struct {
		Suites   int     `json:"suites"`
		Tests    int     `json:"tests"`
		Duration float64 `json:"duration"`
	} `json:"stats"`
	Results []mochawesomeResult `json:"results"`
}

type mochawesomeResult struct {
	// File is the spec's path. A better suite identifier than the title, which can be
	// renamed without the test changing.
	File   string             `json:"file"`
	Suites []mochawesomeSuite `json:"suites"`
	Tests  []mochawesomeTest  `json:"tests"`
}

type mochawesomeSuite struct {
	Title  string             `json:"title"`
	File   string             `json:"file"`
	Suites []mochawesomeSuite `json:"suites"`
	Tests  []mochawesomeTest  `json:"tests"`
}

type mochawesomeTest struct {
	Title        string `json:"title"`
	FullTitle    string `json:"fullTitle"`
	Duration     int    `json:"duration"`
	Pass         bool   `json:"pass"`
	Fail         bool   `json:"fail"`
	Pending      bool   `json:"pending"`
	CurrentRetry int    `json:"currentRetry"`
	Err          struct {
		Message string `json:"message"`
		Stack   string `json:"estack"`
	} `json:"err"`
}

// ParseMochawesome reads a mochawesome report.
func ParseMochawesome(raw []byte) (Report, error) {
	report := Report{Family: FamilyE2E, Tool: "cypress"}

	var source mochawesomeReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return report, ErrUnsupported{Tool: "cypress", Detail: err.Error()}
	}

	for _, result := range source.Results {
		suite := strings.TrimSpace(result.File)
		for _, test := range result.Tests {
			report.Cases = append(report.Cases, mochaToCase(test, suite))
		}
		for _, nested := range result.Suites {
			collectMochaSuite(nested, suite, &report)
		}
	}

	report.DurationMS = int64(source.Stats.Duration)
	report.tally()
	report.Summary = SummarizeJUnit(report)
	return report, nil
}

func collectMochaSuite(suite mochawesomeSuite, path string, report *Report) {
	// The file wins over the title: renaming a `describe` must not make all its tests
	// "new" at the next comparison.
	name := strings.TrimSpace(suite.File)
	if name == "" {
		name = path
	}
	if title := strings.TrimSpace(suite.Title); title != "" && name != "" {
		name = name + " / " + title
	}

	for _, test := range suite.Tests {
		report.Cases = append(report.Cases, mochaToCase(test, name))
	}
	for _, nested := range suite.Suites {
		collectMochaSuite(nested, name, report)
	}
}

func mochaToCase(test mochawesomeTest, suite string) Case {
	name := strings.TrimSpace(test.FullTitle)
	if name == "" {
		name = strings.TrimSpace(test.Title)
	}

	testCase := Case{Suite: suite, Name: name, DurationMS: test.Duration}

	switch {
	case test.Pending:
		testCase.Status = StatusSkipped
	case test.Fail:
		testCase.Status = StatusFailed
		testCase.Message = firstLine(test.Err.Message)
		testCase.Trace = strings.TrimSpace(test.Err.Stack)
	case test.Pass && test.CurrentRetry > 0:
		// Passed, but not on the first attempt.
		testCase.Status = StatusFlaky
		testCase.Message = firstLine(test.Err.Message)
	case test.Pass:
		testCase.Status = StatusPassed
	default:
		// Neither passed, failed nor pending: Cypress produces this state when the spec
		// dies before running the case. Ignoring it would lose a test.
		testCase.Status = StatusSkipped
		testCase.Message = "case not run"
	}

	return testCase
}
