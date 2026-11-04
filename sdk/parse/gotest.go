package parse

/*
 * `go test -json`, the test runner's own stream of events.
 *
 * Read natively rather than converted to JUnit: the stream already says each test's outcome,
 * duration and output, subtests included, and a conversion would need a tool downloaded at every
 * run. What JUnit cannot say is kept too: a package whose tests never ran, because it does not
 * build or because TestMain failed, is a failure of its own.
 */

// GoTestJSON parses `go test -json` output into a [GoTest].
const GoTestJSON ID = "go-test-json"

// GoTest is a `go test -json` report.
type GoTest struct {
	// DurationMS is the sum of the packages' times.
	DurationMS int64
	Cases      []GoTestCase
}

// GoTestOutcome is what the stream says of a test.
type GoTestOutcome string

const (
	GoTestPassed  GoTestOutcome = "passed"
	GoTestFailed  GoTestOutcome = "failed"
	GoTestSkipped GoTestOutcome = "skipped"
)

// GoTestCase is a test, a subtest, or a package that failed outside its tests.
type GoTestCase struct {
	// Package is the import path.
	Package string
	// Name is the test's name, `TestX/sub` for a subtest. Empty for a package that failed outside
	// any test: it does not build, TestMain failed, a panic escaped.
	Name       string
	DurationMS int
	Outcome    GoTestOutcome
	// Output is what the test printed, the failure's message and trace included. Bounded: a test
	// that logs megabytes keeps its end, where the failure is.
	Output string
}
