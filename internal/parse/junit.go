package parse

import (
	"encoding/xml"
	"strconv"
	"strings"
)

/*
 * JUnit XML, the format common to PHPUnit, Cypress and most test runners.
 *
 * "Common" is an abuse of language: it has no normative schema, and each tool produces a variant
 * of it. The record keeps what the variants share and decides nothing about identity: which of the
 * class name and the suite path names a case is the interpreter's call. [JUnitCase.Suite] gives the
 * usual answer.
 *
 * - **The root varies.** PHPUnit emits an enclosing `<testsuites>`, Cypress sometimes emits
 * `<testsuite>` alone.
 * - **The suites nest.** The path of enclosing suites is kept, so two tests of the same name in
 * different files stay two tests.
 * - **The time is in floating-point seconds**, not in milliseconds, and it is sometimes missing.
 */

// JUnitXML parses a JUnit XML report into a [JUnit].
const JUnitXML ID = "junit-xml"

// JUnit is a JUnit report.
type JUnit struct {
	// DurationMS is the sum of the top-level suites' times.
	DurationMS int64
	Cases      []JUnitCase
}

// JUnitOutcome is what the report says of a case.
type JUnitOutcome string

const (
	JUnitPassed  JUnitOutcome = "passed"
	JUnitFailed  JUnitOutcome = "failed"
	JUnitErrored JUnitOutcome = "errored"
	JUnitSkipped JUnitOutcome = "skipped"
	// JUnitRerun: the case failed, then passed on a retry (`flakyFailure`, `rerunFailure`).
	JUnitRerun JUnitOutcome = "rerun"
)

// JUnitCase is one `<testcase>`.
type JUnitCase struct {
	// Path is the enclosing suites' names, outermost first, joined with " / ".
	Path string
	// ClassName is often more precise than the suite: PHPUnit puts the fully qualified class there,
	// where the suite carries the file name.
	ClassName  string
	Name       string
	DurationMS int
	Outcome    JUnitOutcome
	// Detail is the first failure or error, or the first failed attempt of a rerun case.
	Detail *JUnitDetail
}

// JUnitDetail is a `<failure>`, an `<error>` or a failed attempt.
type JUnitDetail struct {
	Message string
	Type    string
	Content string
}

// Suite is the usual identity of a case's suite: the class name, else the suite path. Renaming a
// file must not break a test's identity.
func (c JUnitCase) Suite() string {
	if c.ClassName != "" {
		return c.ClassName
	}
	return c.Path
}

// Text is the detail's message, or its body when the tool leaves `message` empty.
func (d JUnitDetail) Text() string {
	if message := strings.TrimSpace(d.Message); message != "" {
		return message
	}
	return strings.TrimSpace(d.Content)
}

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
func ParseJUnit(raw []byte) (JUnit, error) {
	var root junitSuites
	if err := xml.Unmarshal(raw, &root); err != nil {
		// The root is not <testsuites>: try a bare <testsuite> before giving up. That is Cypress's
		// case, and giving up here would make a successful campaign look as though it never ran.
		var single junitSuite
		if xml.Unmarshal(raw, &single) != nil {
			return JUnit{}, err
		}
		root.Suites = []junitSuite{single}
	}

	var report JUnit
	for _, suite := range root.Suites {
		collectJUnit(suite, "", &report)
		report.DurationMS += int64(junitMillis(suite.Time))
	}
	return report, nil
}

func collectJUnit(suite junitSuite, path string, report *JUnit) {
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

func junitRecord(source junitCase, path string) JUnitCase {
	record := JUnitCase{
		Path:       path,
		ClassName:  strings.TrimSpace(source.ClassName),
		Name:       strings.TrimSpace(source.Name),
		DurationMS: junitMillis(source.Time),
		Outcome:    JUnitPassed,
	}

	reruns := append(append([]junitDetail{}, source.FlakyFailures...), source.RerunFailures...)
	switch {
	case len(source.Failures) > 0:
		record.Outcome = JUnitFailed
		record.Detail = detailOf(source.Failures[0])
	case len(source.Errors) > 0:
		record.Outcome = JUnitErrored
		record.Detail = detailOf(source.Errors[0])
	case len(reruns) > 0:
		record.Outcome = JUnitRerun
		record.Detail = detailOf(reruns[0])
	case source.Skipped != nil:
		record.Outcome = JUnitSkipped
	}
	return record
}

func detailOf(source junitDetail) *JUnitDetail {
	return &JUnitDetail{Message: source.Message, Type: source.Type, Content: source.Content}
}

// junitMillis converts a JUnit time, a float in seconds, to milliseconds.
func junitMillis(value string) int {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return int(seconds * 1000)
}
