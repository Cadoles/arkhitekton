// Package results grades a release: the quality score composed from the facts its runs left.
//
// Everything here is pure: no database, no network, no filesystem.
package results

// Group gathers results on screen and in the score. Free, chosen by the plugin that read the run;
// the score knows how to grade the groups below and leaves the others out.
type Group string

// The groups the score grades.
const (
	GroupUnit        Group = "unit"
	GroupIntegration Group = "integration"
	GroupE2E         Group = "e2e"
	GroupStatic      Group = "static"
	GroupSecurity    Group = "security"
	GroupLoad        Group = "load"
)

// Report is a set of test results, counted.
type Report struct {
	// Status is the overall verdict: pass, warn, fail, skip or na.
	Status  string
	Total   int
	Passed  int
	Failed  int
	Skipped int
	Flaky   int
}

// Overall statuses of a report.
const (
	ReportPass = "pass"
	ReportWarn = "warn"
	ReportFail = "fail"
	ReportSkip = "skip"
	ReportNA   = "na"
)
