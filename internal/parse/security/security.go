// Package security reads the normalised security report a scan pipeline publishes.
//
// The report is two files: `summary.json`, the aggregates, and `findings.jsonl`, one finding per
// line. Their format is set by the producer, the pipeline's normalisation step, which merges the
// scanners, deduplicates and applies exceptions. This package declares only what the dashboard
// reads of it. It decides nothing about severities or fingerprints: renormalising here would give
// two truths about the same findings, the one that fails the build and the one that is displayed.
package security

import (
	"encoding/json"
	"strings"
)

// The names under which the producer publishes the report's files.
const (
	SummaryFileName  = "summary.json"
	FindingsFileName = "findings.jsonl"
)

// Severity is the report's single scale; every scanner is brought back to it by the producer.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
	SeverityInfo     Severity = "INFO"
)

// Summary is the aggregates file.
type Summary struct {
	RunID         string            `json:"run_id"`
	GeneratedAt   string            `json:"generated_at"`
	Total         int               `json:"total"`
	BySeverity    map[string]int    `json:"by_severity"`
	ByCategory    map[string]int    `json:"by_category"`
	ByTarget      map[string]int    `json:"by_target"`
	Delta         Delta             `json:"delta"`
	Suppressed    int               `json:"suppressed"`
	Policy        map[string]string `json:"policy"`
	ScannerStatus map[string]string `json:"scanner_status"`
	PreviousRunID string            `json:"previous_run_id,omitempty"`
	// FailedScanners is what the findings cannot say: an audit in which a scanner did not run
	// produces zero findings for it, exactly like a clean one.
	FailedScanners []string `json:"failed_scanners,omitempty"`
}

// Delta compares the run with the previous one.
type Delta struct {
	New      int `json:"new"`
	Existing int `json:"existing"`
	Resolved int `json:"resolved"`
}

// Finding is one line of the findings file.
type Finding struct {
	Fingerprint string       `json:"fingerprint"`
	RunID       string       `json:"run_id"`
	Target      Target       `json:"target"`
	Tool        Tool         `json:"tool"`
	Category    string       `json:"category"`
	RuleID      string       `json:"rule_id"`
	Severity    Severity     `json:"severity"`
	CVSS        float64      `json:"cvss,omitempty"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Location    *Location    `json:"location,omitempty"`
	Package     *Package     `json:"package,omitempty"`
	Fixable     bool         `json:"fixable"`
	Status      string       `json:"status"`
	FirstSeen   string       `json:"first_seen,omitempty"`
	Suppression *Suppression `json:"suppression"`
}

// Suppressed says whether an exception in force covers the finding. It stays in the data set but
// leaves the counts.
func (f Finding) Suppressed() bool { return f.Suppression != nil }

// Target is what was analysed: a repository or an image.
type Target struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Revision string `json:"revision,omitempty"`
}

// Tool names the scanner that reported the finding.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Location points at the finding in its target.
type Location struct {
	File      string `json:"file,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
}

// Package is the dependency concerned, for dependency and image findings.
type Package struct {
	Name      string `json:"name,omitempty"`
	Installed string `json:"installed,omitempty"`
	Fixed     string `json:"fixed,omitempty"`
}

// Suppression is the exception covering a finding.
type Suppression struct {
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
}

// UnmarshalFindings reads a findings file.
//
// An unreadable line is skipped rather than failing the whole file: one corrupt finding must not
// cost the audit.
func UnmarshalFindings(raw []byte) []Finding {
	findings := make([]Finding, 0)
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var finding Finding
		if err := json.Unmarshal([]byte(trimmed), &finding); err != nil {
			continue
		}
		findings = append(findings, finding)
	}
	return findings
}
