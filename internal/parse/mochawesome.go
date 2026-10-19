package parse

import (
	"encoding/json"
	"strings"
)

/*
 * Mochawesome JSON, Cypress's detailed report.
 *
 * A test that passed after a retry carries `currentRetry > 0` and `pass: true`. The record keeps
 * both; reading it as a plain success would erase the campaign's only flakiness signal.
 */

// MochawesomeJSON parses a mochawesome report into a [Mochawesome].
const MochawesomeJSON ID = "mochawesome-json"

// Mochawesome is a mochawesome report.
type Mochawesome struct {
	DurationMS int64
	Tests      []MochaTest
}

// MochaTest is one test with the blocks enclosing it.
type MochaTest struct {
	// File is the spec's path, from the result the test belongs to.
	File string
	// Suites are the enclosing `describe` blocks, outermost first.
	Suites       []MochaSuite
	Title        string
	FullTitle    string
	DurationMS   int
	Pass         bool
	Fail         bool
	Pending      bool
	CurrentRetry int
	ErrMessage   string
	ErrStack     string
}

// MochaSuite is a `describe` block.
type MochaSuite struct {
	Title string
	File  string
}

// Suite is the usual identity of a test's suite: the spec file, extended by the blocks' titles. A
// block's own file wins over the enclosing path: renaming a `describe` must not make all its tests
// new.
func (t MochaTest) Suite() string {
	name := t.File
	for _, suite := range t.Suites {
		next := suite.File
		if next == "" {
			next = name
		}
		if suite.Title != "" && next != "" {
			next = next + " / " + suite.Title
		}
		name = next
	}
	return name
}

// Name is the test's full title, else its title.
func (t MochaTest) Name() string {
	if t.FullTitle != "" {
		return t.FullTitle
	}
	return t.Title
}

type mochawesomeReport struct {
	Stats struct {
		Duration float64 `json:"duration"`
	} `json:"stats"`
	Results []mochawesomeResult `json:"results"`
}

type mochawesomeResult struct {
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

// ParseMochawesome reads a mochawesome report. The error is the decoder's, unwrapped.
func ParseMochawesome(raw []byte) (Mochawesome, error) {
	var source mochawesomeReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return Mochawesome{}, err
	}

	report := Mochawesome{DurationMS: int64(source.Stats.Duration)}
	for _, result := range source.Results {
		file := strings.TrimSpace(result.File)
		for _, test := range result.Tests {
			report.Tests = append(report.Tests, mochaRecord(test, file, nil))
		}
		for _, nested := range result.Suites {
			collectMocha(nested, file, nil, &report)
		}
	}
	return report, nil
}

func collectMocha(suite mochawesomeSuite, file string, enclosing []MochaSuite, report *Mochawesome) {
	chain := append(append([]MochaSuite{}, enclosing...), MochaSuite{
		Title: strings.TrimSpace(suite.Title), File: strings.TrimSpace(suite.File),
	})
	for _, test := range suite.Tests {
		report.Tests = append(report.Tests, mochaRecord(test, file, chain))
	}
	for _, nested := range suite.Suites {
		collectMocha(nested, file, chain, report)
	}
}

func mochaRecord(test mochawesomeTest, file string, chain []MochaSuite) MochaTest {
	return MochaTest{
		File: file, Suites: chain,
		Title: strings.TrimSpace(test.Title), FullTitle: strings.TrimSpace(test.FullTitle),
		DurationMS: test.Duration,
		Pass:       test.Pass, Fail: test.Fail, Pending: test.Pending, CurrentRetry: test.CurrentRetry,
		ErrMessage: test.Err.Message, ErrStack: test.Err.Stack,
	}
}
