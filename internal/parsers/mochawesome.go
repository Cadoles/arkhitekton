package parsers

import (
	"encoding/json"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

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
func ParseMochawesome(raw []byte) (parse.Mochawesome, error) {
	var source mochawesomeReport
	if err := json.Unmarshal(raw, &source); err != nil {
		return parse.Mochawesome{}, err
	}

	report := parse.Mochawesome{DurationMS: int64(source.Stats.Duration)}
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

func collectMocha(suite mochawesomeSuite, file string, enclosing []parse.MochaSuite, report *parse.Mochawesome) {
	chain := append(append([]parse.MochaSuite{}, enclosing...), parse.MochaSuite{
		Title: strings.TrimSpace(suite.Title), File: strings.TrimSpace(suite.File),
	})
	for _, test := range suite.Tests {
		report.Tests = append(report.Tests, mochaRecord(test, file, chain))
	}
	for _, nested := range suite.Suites {
		collectMocha(nested, file, chain, report)
	}
}

func mochaRecord(test mochawesomeTest, file string, chain []parse.MochaSuite) parse.MochaTest {
	return parse.MochaTest{
		File: file, Suites: chain,
		Title: strings.TrimSpace(test.Title), FullTitle: strings.TrimSpace(test.FullTitle),
		DurationMS: test.Duration,
		Pass:       test.Pass, Fail: test.Fail, Pending: test.Pending, CurrentRetry: test.CurrentRetry,
		ErrMessage: test.Err.Message, ErrStack: test.Err.Stack,
	}
}
