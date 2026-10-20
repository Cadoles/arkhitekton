package parsers

import (
	"encoding/xml"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	XMLName xml.Name     `xml:"testsuite"`
	Name    string       `xml:"name,attr"`
	Time    string       `xml:"time,attr"`
	Suites  []junitSuite `xml:"testsuite"`
	Cases   []junitCase  `xml:"testcase"`
}

type junitCase struct {
	Name          string        `xml:"name,attr"`
	ClassName     string        `xml:"classname,attr"`
	Time          string        `xml:"time,attr"`
	Failures      []junitDetail `xml:"failure"`
	Errors        []junitDetail `xml:"error"`
	Skipped       *struct{}     `xml:"skipped"`
	FlakyFailures []junitDetail `xml:"flakyFailure"`
	RerunFailures []junitDetail `xml:"rerunFailure"`
}

type junitDetail struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Content string `xml:",chardata"`
}

// ParseJUnit reads a JUnit XML report. The error is the decoder's, unwrapped.
func ParseJUnit(raw []byte) (parse.JUnit, error) {
	var root junitSuites
	if err := xml.Unmarshal(raw, &root); err != nil {
		// The root is not <testsuites>: try a bare <testsuite> before giving up. That is Cypress's
		// case, and giving up here would make a successful campaign look as though it never ran.
		var single junitSuite
		if xml.Unmarshal(raw, &single) != nil {
			return parse.JUnit{}, err
		}
		root.Suites = []junitSuite{single}
	}

	var report parse.JUnit
	for _, suite := range root.Suites {
		collectJUnit(suite, "", &report)
		report.DurationMS += int64(junitMillis(suite.Time))
	}
	return report, nil
}

func collectJUnit(suite junitSuite, path string, report *parse.JUnit) {
	name := strings.TrimSpace(suite.Name)
	if path != "" && name != "" {
		name = path + " / " + name
	} else if name == "" {
		name = path
	}

	for _, source := range suite.Cases {
		report.Cases = append(report.Cases, junitRecord(source, name))
	}
	for _, nested := range suite.Suites {
		collectJUnit(nested, name, report)
	}
}

func junitRecord(source junitCase, path string) parse.JUnitCase {
	record := parse.JUnitCase{
		Path:       path,
		ClassName:  strings.TrimSpace(source.ClassName),
		Name:       strings.TrimSpace(source.Name),
		DurationMS: junitMillis(source.Time),
		Outcome:    parse.JUnitPassed,
	}

	reruns := append(append([]junitDetail{}, source.FlakyFailures...), source.RerunFailures...)
	switch {
	case len(source.Failures) > 0:
		record.Outcome = parse.JUnitFailed
		record.Detail = detailOf(source.Failures[0])
	case len(source.Errors) > 0:
		record.Outcome = parse.JUnitErrored
		record.Detail = detailOf(source.Errors[0])
	case len(reruns) > 0:
		record.Outcome = parse.JUnitRerun
		record.Detail = detailOf(reruns[0])
	case source.Skipped != nil:
		record.Outcome = parse.JUnitSkipped
	}
	return record
}

func detailOf(source junitDetail) *parse.JUnitDetail {
	return &parse.JUnitDetail{Message: source.Message, Type: source.Type, Content: source.Content}
}

// junitMillis converts a JUnit time, a float in seconds, to milliseconds.
func junitMillis(value string) int {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return int(seconds * 1000)
}
