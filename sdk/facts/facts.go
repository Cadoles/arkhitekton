// Package facts is what the dashboard keeps of a run: the format a run interpreter produces and the
// database stores.
//
// The format belongs to the dashboard, not to the plugins. A fact describes itself: its shape, its
// group, its tool and the producer that wrote it travel with it. Nothing at display time asks the
// plugin what a fact means, so a fact stays readable after its plugin is upgraded or removed.
//
// The shapes are closed and only the dashboard adds one. A shape is a storage schema, a rule of
// comparison between releases and a screen; a plugin bringing its own would make display depend on
// the plugin again.
//
// This package imports nothing of the dashboard: it is the contract the plugins compile against.
package facts

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Shape says how a set of facts is stored, compared and displayed.
type Shape string

const (
	// ShapeCases are test cases. Identity is suite + name; compared by change of status.
	ShapeCases Shape = "cases"
	// ShapeFindings are findings. Identity is the fingerprint; compared by appearance.
	ShapeFindings Shape = "findings"
	// ShapeMeasures are numeric measures. Identity is scenario + metric; compared with a baseline.
	ShapeMeasures Shape = "measures"
)

// Status is a set's overall verdict.
type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
	// StatusNA: the tool gave no verdict. It ran and crashed, or did not run. Distinct from a pass
	// with nothing found, which is the whole point of reading the run's state.
	StatusNA Status = "na"
)

// CaseStatus is a test case's fate.
type CaseStatus string

const (
	CasePassed  CaseStatus = "passed"
	CaseFailed  CaseStatus = "failed"
	CaseSkipped CaseStatus = "skipped"
	// CaseFlaky: passed after one or more retries.
	CaseFlaky CaseStatus = "flaky"
)

// Severity is the findings' scale.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
	SeverityInfo     Severity = "INFO"
)

// Producer names the plugin version that interpreted a run.
//
// Recorded on every stored set: replaying a run must go through the same interpretation, and a
// change of producer between two releases is what tells a real change from a change of reading.
type Producer struct {
	Plugin  string
	Version string
}

func (p Producer) String() string { return p.Plugin + "@" + p.Version }

var producerPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?)@(\S+)$`)

// ParseProducer reads `plugin@version`.
func ParseProducer(raw string) (Producer, error) {
	match := producerPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return Producer{}, fmt.Errorf("producer %q: expected plugin@version", raw)
	}
	return Producer{Plugin: match[1], Version: match[3]}, nil
}

// Set is what one tool of one run produced.
type Set struct {
	Shape Shape
	// Group gathers sets on screen and in the score: `unit`, `static`, `security`... Free, chosen by
	// the plugin; it replaces the closed list of families.
	Group string
	Tool  string
	// Status is the verdict. Left empty for cases, it is derived from them.
	Status  Status
	Summary string
	// RawObject is the key of the artifact the set was read from. It is what makes a replay possible.
	RawObject  string
	DurationMS int64

	// Exactly one of these is filled, according to Shape. A set with nothing in it and a verdict of
	// its own (a crashed tool) carries none.
	Cases    []Case
	Findings *Findings
	Measures *Measures
}

// Case is a test case, or a violation projected onto one.
type Case struct {
	Suite      string
	Name       string
	Status     CaseStatus
	DurationMS int
	// Message is the reason for the failure, on one line; Trace is the full detail.
	Message string
	Trace   string
}

// Key is the case's identity across runs.
func (c Case) Key() string { return c.Suite + "::" + c.Name }

// Findings is an audit: its findings and the aggregates only the producer can state.
type Findings struct {
	Items []Finding
	// FailedScanners names the scanners that did not run. Their silence is not a clean result.
	FailedScanners []string
	ScannerStatus  map[string]string
	Policy         map[string]string
	PreviousRunID  string
	RunID          string
	GeneratedAt    string
	Total          int
	Suppressed     int
	BySeverity     map[Severity]int
	DeltaNew       int
	DeltaExisting  int
	DeltaResolved  int
}

// Finding is one finding.
type Finding struct {
	Fingerprint        string
	RunID              string
	TargetKind         string
	TargetName         string
	Tool               string
	Category           string
	RuleID             string
	Severity           Severity
	CVSS               float64
	Title              string
	Description        string
	Location           string
	PackageName        string
	PackageInstalled   string
	PackageFixed       string
	Fixable            bool
	Status             string
	FirstSeen          string
	Suppressed         bool
	SuppressionReason  string
	SuppressionExpires string
}

// Measures is a load campaign.
type Measures struct {
	Scenario         string
	Items            []Measure
	VUs              int
	ThresholdsFailed bool
}

// Measure is one numeric measure.
type Measure struct {
	Metric      string
	Label       string
	Unit        string
	Value       float64
	LowerBetter bool
	Volume      bool
	ThresholdOK *bool
}

var (
	errNoGroup = errors.New("no group")
	errNoTool  = errors.New("no tool")
)

// Validate refuses a set the database could not store or the screens could not read.
//
// The host calls it on every set an interpreter returns: an interpreter is plugin code, and what it
// writes is shown to everyone.
func (s Set) Validate() error {
	var problems []error
	if strings.TrimSpace(s.Group) == "" {
		problems = append(problems, errNoGroup)
	}
	if strings.TrimSpace(s.Tool) == "" {
		problems = append(problems, errNoTool)
	}

	switch s.Status {
	case "", StatusPass, StatusWarn, StatusFail, StatusSkip, StatusNA:
	default:
		problems = append(problems, fmt.Errorf("unknown status %q", s.Status))
	}

	switch s.Shape {
	case ShapeCases:
		if s.Findings != nil || s.Measures != nil {
			problems = append(problems, errors.New("a set of cases carries findings or measures"))
		}
		for i, c := range s.Cases {
			if strings.TrimSpace(c.Suite) == "" || strings.TrimSpace(c.Name) == "" {
				problems = append(problems, fmt.Errorf("case %d: suite and name are its identity", i))
			}
			switch c.Status {
			case CasePassed, CaseFailed, CaseSkipped, CaseFlaky:
			default:
				problems = append(problems, fmt.Errorf("case %d: unknown status %q", i, c.Status))
			}
		}
	case ShapeFindings:
		if len(s.Cases) > 0 || s.Measures != nil {
			problems = append(problems, errors.New("a set of findings carries cases or measures"))
		}
		if s.Findings != nil {
			for i, f := range s.Findings.Items {
				if strings.TrimSpace(f.Fingerprint) == "" {
					problems = append(problems, fmt.Errorf("finding %d: no fingerprint", i))
				}
			}
		}
	case ShapeMeasures:
		if len(s.Cases) > 0 || s.Findings != nil {
			problems = append(problems, errors.New("a set of measures carries cases or findings"))
		}
		if s.Measures != nil && strings.TrimSpace(s.Measures.Scenario) == "" {
			problems = append(problems, errors.New("measures without a scenario"))
		}
	default:
		problems = append(problems, fmt.Errorf("unknown shape %q", s.Shape))
	}

	if s.Status == "" && s.Shape != ShapeCases {
		problems = append(problems, errors.New("only cases derive their status"))
	}
	return errors.Join(problems...)
}

// Counts tallies a set's cases.
type Counts struct {
	Total, Passed, Failed, Skipped, Flaky int
}

// Counts recounts the cases. Recounted rather than taken from the tool's announced totals, which
// lie when the process dies mid-run.
func (s Set) Counts() Counts {
	var counts Counts
	for _, c := range s.Cases {
		counts.Total++
		switch c.Status {
		case CasePassed:
			counts.Passed++
		case CaseFailed:
			counts.Failed++
		case CaseSkipped:
			counts.Skipped++
		case CaseFlaky:
			counts.Flaky++
		}
	}
	return counts
}

// Verdict is the set's status: the interpreter's when it gave one, otherwise derived from the
// cases. No case at all reads `na`: an empty report says nothing was measured.
func (s Set) Verdict() Status {
	if s.Status != "" {
		return s.Status
	}
	counts := s.Counts()
	switch {
	case counts.Total == 0:
		return StatusNA
	case counts.Failed > 0:
		return StatusFail
	case counts.Flaky > 0:
		// A flaky test does not block shipping, but staying silent about it would remove the only
		// signal there is on it.
		return StatusWarn
	}
	return StatusPass
}
