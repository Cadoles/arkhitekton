// Package results reads the output of test tools and normalises it.
//
// Six tools, six formats, one question: what changed since last time? That is the
// question asked before shipping, and it can only be answered if results are
// comparable from one run to the next. All the normalisation exists for that.
//
// Two principles follow.
//
// A case's identity is stable. `suite` + `name` name the same test from one run to
// the next, whatever its order, its duration or how many tests surround it. Without
// that stability, "new failure" and "persistent failure" would be
// indistinguishable — and that is exactly the distinction that decides whether to
// ship.
//
// A tool that did not run is not a tool with no results. PHPStan crashing and
// PHPStan finding nothing both produce zero violations. The `meta` descriptor,
// emitted by the pipeline itself, settles it — the dashboard cannot infer it.
//
// Everything here is pure: no database, no network, no filesystem.
package results

import (
	"fmt"
	"strings"
)

// Family groups results by nature.
type Family string

const (
	FamilyUnit        Family = "unit"
	FamilyIntegration Family = "integration"
	FamilyE2E         Family = "e2e"
	FamilyStatic      Family = "static"
	FamilySecurity    Family = "security"
	FamilyLoad        Family = "load"
)

// Status is a test case's fate.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
	// StatusFlaky: the case eventually passed after one or more retries. Neither a
	// success nor a failure — it counts as half a failure in the score, because a flaky
	// test costs on every run without ever blocking.
	StatusFlaky Status = "flaky"
)

// Case is a normalised test case.
type Case struct {
	// Suite and Name form the case's identity, stable from run to run. The comparison
	// with the previous release is made on that pair.
	Suite string
	Name  string
	Status
	DurationMS int
	// Message is the reason for the failure, on one line. The full trace lives apart:
	// the message is read first, the trace unfolded.
	Message string
	Trace   string
}

// Key returns the case's identity.
//
// A function rather than a computed field: the concatenation must be the same
// everywhere, and a field left unfilled would make two identical cases pass for
// distinct ones.
func (c Case) Key() string { return c.Suite + "::" + c.Name }

// Report is a tool's normalised result.
type Report struct {
	Family Family
	Tool   string
	// Status is the overall verdict, derived from the cases — unless the `meta`
	// descriptor says the tool failed, in which case nothing is derivable.
	Status  string
	Total   int
	Passed  int
	Failed  int
	Skipped int
	Flaky   int
	// DurationMS is the cumulative duration. Zero when the tool does not report it —
	// distinct from an instantaneous run, but the interface then shows nothing rather
	// than "0 ms".
	DurationMS int64
	Summary    string
	Cases      []Case
}

// Overall statuses of a report.
const (
	ReportPass = "pass"
	ReportWarn = "warn"
	ReportFail = "fail"
	ReportSkip = "skip"
	ReportNA   = "na"
)

// tally recounts the cases and sets the overall verdict.
//
// Recounted rather than taken from the totals the tool announces: those lie
// regularly — a JUnit report says tests="12" while listing ten cases when the
// process dies mid-run. Counting what is really there avoids two phantom passing
// tests.
func (r *Report) tally() {
	r.Total, r.Passed, r.Failed, r.Skipped, r.Flaky = 0, 0, 0, 0, 0

	for _, testCase := range r.Cases {
		r.Total++
		switch testCase.Status {
		case StatusPassed:
			r.Passed++
		case StatusFailed:
			r.Failed++
		case StatusSkipped:
			r.Skipped++
		case StatusFlaky:
			r.Flaky++
		}
	}

	switch {
	case r.Total == 0:
		r.Status = ReportNA
	case r.Failed > 0:
		r.Status = ReportFail
	case r.Flaky > 0:
		// A flaky test does not block shipping, but staying silent about it would remove the
		// only signal there is on it.
		r.Status = ReportWarn
	default:
		r.Status = ReportPass
	}
}

// ErrUnsupported reports an unrecognised format.
type ErrUnsupported struct {
	Tool   string
	Detail string
}

func (e ErrUnsupported) Error() string {
	return fmt.Sprintf("unrecognised format for %q: %s", e.Tool, e.Detail)
}

// firstLine returns the first non-empty line of a text.
//
// PHP tools' failure messages often run to ten lines, the first carrying the
// substance and the rest being the trace. Showing all ten in a table makes the table
// unreadable.
func firstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
