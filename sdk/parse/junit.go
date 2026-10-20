package parse

import "strings"

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
