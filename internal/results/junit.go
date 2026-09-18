package results

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

/*
JUnit XML — le format commun de PHPUnit, Cypress et de la plupart des lanceurs.

« Commun » est un abus de langage : il n'a pas de schéma normatif, et chaque outil
en produit une variante. Trois écarts comptent ici, et chacun a une conséquence
visible s'il est ignoré.

  - **La racine varie.** PHPUnit émet `<testsuites>` englobant, Cypress émet
    parfois `<testsuite>` seul. Ne gérer que le premier ferait lire zéro test sur
    une campagne E2E réussie.

  - **Les suites s'imbriquent.** Un `<testsuite>` peut en contenir d'autres. À plat,
    les noms de classes disparaîtraient et deux tests homonymes de fichiers
    différents fusionneraient — c'est-à-dire un faux « échec résolu ».

  - **Le temps est en secondes flottantes**, pas en millisecondes, et il manque
    parfois.
*/

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
	Name string `xml:"name,attr"`
	// ClassName is often more precise than the parent suite's name: PHPUnit puts the
	// fully qualified class there, where the suite carries the file name.
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failures  []junitDetail `xml:"failure"`
	Errors    []junitDetail `xml:"error"`
	Skipped   *struct{}     `xml:"skipped"`
	// Rerun and Flaky: Cypress and PHPUnit 10 use these to report a case that passed
	// on retry. Treating them as successes would erase the only signal of instability.
	FlakyFailures []junitDetail `xml:"flakyFailure"`
	RerunFailures []junitDetail `xml:"rerunFailure"`
}

type junitDetail struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Content string `xml:",chardata"`
}

// ParseJUnit lit un rapport JUnit XML.
func ParseJUnit(family Family, tool string, raw []byte) (Report, error) {
	report := Report{Family: family, Tool: tool}

	var root junitSuites
	if err := xml.Unmarshal(raw, &root); err != nil {
		// The root is not <testsuites>: try a bare <testsuite> before giving up. That is
		// Cypress's case, and giving up here would make a successful end-to-end campaign
		// look as though it never ran.
		var single junitSuite
		if single.unmarshal(raw) != nil {
			return report, ErrUnsupported{Tool: tool, Detail: err.Error()}
		}
		root.Suites = []junitSuite{single}
	}

	for _, suite := range root.Suites {
		collectSuite(suite, "", &report)
	}

	report.DurationMS = junitDuration(root.Suites)
	report.tally()
	return report, nil
}

func (s *junitSuite) unmarshal(raw []byte) error { return xml.Unmarshal(raw, s) }

// collectSuite walks the suite tree while keeping the path.
//
// The path is what gives a case a stable identity: two `testLogin` tests in
// different classes must stay two tests, otherwise the comparison with the
// previous release would report a resolved failure where nothing moved.
func collectSuite(suite junitSuite, path string, report *Report) {
	name := strings.TrimSpace(suite.Name)
	if path != "" && name != "" {
		name = path + " / " + name
	} else if name == "" {
		name = path
	}

	for _, testCase := range suite.Cases {
		report.Cases = append(report.Cases, junitToCase(testCase, name))
	}
	for _, nested := range suite.Suites {
		collectSuite(nested, name, report)
	}
}

func junitToCase(source junitCase, suiteName string) Case {
	// `classname` wins: PHPUnit puts the fully qualified class there, where the suite
	// carries the file name. Renaming a file must not break a test's identity.
	suite := strings.TrimSpace(source.ClassName)
	if suite == "" {
		suite = suiteName
	}

	testCase := Case{
		Suite: suite, Name: strings.TrimSpace(source.Name),
		Status: StatusPassed, DurationMS: junitMillis(source.Time),
	}

	details := append(append([]junitDetail{}, source.Failures...), source.Errors...)
	reruns := append(append([]junitDetail{}, source.FlakyFailures...), source.RerunFailures...)

	switch {
	case len(details) > 0:
		testCase.Status = StatusFailed
		testCase.Message = firstLine(detailMessage(details[0]))
		testCase.Trace = strings.TrimSpace(details[0].Content)
	case len(reruns) > 0:
		// The case eventually passed: flaky, not failed.
		testCase.Status = StatusFlaky
		testCase.Message = firstLine(detailMessage(reruns[0]))
		testCase.Trace = strings.TrimSpace(reruns[0].Content)
	case source.Skipped != nil:
		testCase.Status = StatusSkipped
	}

	return testCase
}

func detailMessage(detail junitDetail) string {
	if message := strings.TrimSpace(detail.Message); message != "" {
		return message
	}
	// Some tools leave `message` empty and put everything in the body.
	return strings.TrimSpace(detail.Content)
}

// junitMillis converts a JUnit time to milliseconds.
//
// The format is a float in seconds. Taking it for milliseconds would display
// three-millisecond campaigns, which goes unnoticed for a long time.
func junitMillis(value string) int {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return int(seconds * 1000)
}

func junitDuration(suites []junitSuite) int64 {
	var total int64
	for _, suite := range suites {
		total += int64(junitMillis(suite.Time))
	}
	return total
}

// SummarizeJUnit returns a report's summary line.
func SummarizeJUnit(report Report) string {
	parts := []string{fmt.Sprintf("%d test(s)", report.Total)}
	if report.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", report.Failed))
	}
	if report.Flaky > 0 {
		parts = append(parts, fmt.Sprintf("%d instable(s)", report.Flaky))
	}
	if report.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", report.Skipped))
	}
	return strings.Join(parts, ", ")
}
