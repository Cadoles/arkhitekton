package security

import (
	"context"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
	report "forge.cadoles.com/vcarroy/arkhitekton/sdk/parse/security"
)

// tool is the name the audit is recorded under: one for every scanner, since what is recorded is
// the merged report. The scanners are named in the scanner status.
const tool = "security-audit"

// auditReader reads the merged report as the pipeline wrote it.
//
// It renormalises nothing: the severities, the fingerprints and the deduplication are the
// pipeline's, which decides whether the build fails. A second reading here would give two truths.
type auditReader struct{}

func (auditReader) Interpret(_ context.Context, _ capability.RunState, records []parse.Record) ([]facts.Set, error) {
	var summary *report.Summary
	var summaryObject string
	var findings []report.Finding
	for _, record := range records {
		if record.Err != nil {
			continue
		}
		switch payload := record.Payload.(type) {
		case report.Summary:
			summary, summaryObject = &payload, record.Object
		case []report.Finding:
			findings = payload
		}
	}
	if summary == nil {
		// No audit in this run: not an anomaly, most pipelines produce none.
		return nil, nil
	}

	audit := &facts.Findings{
		RunID: summary.RunID, GeneratedAt: summary.GeneratedAt,
		Total: summary.Total, Suppressed: summary.Suppressed,
		DeltaNew: summary.Delta.New, DeltaExisting: summary.Delta.Existing, DeltaResolved: summary.Delta.Resolved,
		FailedScanners: summary.FailedScanners, ScannerStatus: summary.ScannerStatus,
		Policy: summary.Policy, PreviousRunID: summary.PreviousRunID,
		// The counters are the producer's: they already exclude what is suppressed or resolved, and
		// recounting from the findings would not.
		BySeverity: map[facts.Severity]int{},
	}
	for _, severity := range []facts.Severity{
		facts.SeverityCritical, facts.SeverityHigh, facts.SeverityMedium, facts.SeverityLow,
	} {
		audit.BySeverity[severity] = summary.BySeverity[string(severity)]
	}
	for _, finding := range findings {
		audit.Items = append(audit.Items, toFinding(finding))
	}

	return []facts.Set{{
		Shape: facts.ShapeFindings, Group: "security", Tool: tool, RawObject: summaryObject,
		Status: status(audit), Summary: summarize(audit), Findings: audit,
	}}, nil
}

// status follows the policy the pipeline applied. A scanner that failed allows no verdict: `na`,
// not `fail`, since nothing says the code is bad, only that it could not be looked at.
func status(audit *facts.Findings) facts.Status {
	switch {
	case len(audit.FailedScanners) > 0:
		return facts.StatusNA
	case audit.BySeverity[facts.SeverityCritical] > 0:
		return facts.StatusFail
	case audit.BySeverity[facts.SeverityHigh] > 0:
		return facts.StatusWarn
	}
	return facts.StatusPass
}

func summarize(audit *facts.Findings) string {
	if len(audit.FailedScanners) > 0 {
		return fmt.Sprintf("incomplete audit: %s did not conclude", strings.Join(audit.FailedScanners, ", "))
	}
	parts := []string{fmt.Sprintf("%d finding(s)", audit.Total)}
	if critical := audit.BySeverity[facts.SeverityCritical]; critical > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", critical))
	}
	if high := audit.BySeverity[facts.SeverityHigh]; high > 0 {
		parts = append(parts, fmt.Sprintf("%d high", high))
	}
	if audit.DeltaNew > 0 {
		parts = append(parts, fmt.Sprintf("%d new", audit.DeltaNew))
	}
	if audit.Suppressed > 0 {
		parts = append(parts, fmt.Sprintf("%d suppressed by exception", audit.Suppressed))
	}
	return strings.Join(parts, ", ")
}

func toFinding(finding report.Finding) facts.Finding {
	stored := facts.Finding{
		Fingerprint: finding.Fingerprint, RunID: finding.RunID,
		TargetKind: finding.Target.Kind, TargetName: finding.Target.Name,
		Tool: finding.Tool.Name, Category: finding.Category, RuleID: finding.RuleID,
		Severity: facts.Severity(finding.Severity), CVSS: finding.CVSS,
		Title: finding.Title, Description: finding.Description,
		Fixable: finding.Fixable, Status: finding.Status, FirstSeen: finding.FirstSeen,
		Suppressed: finding.Suppressed(),
	}
	if finding.Location != nil {
		stored.Location = finding.Location.File
		if finding.Location.StartLine > 0 {
			stored.Location = fmt.Sprintf("%s:%d", finding.Location.File, finding.Location.StartLine)
		}
	}
	if finding.Package != nil {
		stored.PackageName = finding.Package.Name
		stored.PackageInstalled = finding.Package.Installed
		stored.PackageFixed = finding.Package.Fixed
	}
	if finding.Suppression != nil {
		stored.SuppressionReason = finding.Suppression.Reason
		stored.SuppressionExpires = finding.Suppression.Expires
	}
	if stored.Severity == "" {
		// The producer brings every tool back to its scale; an empty one is kept as INFO rather than
		// lose the audit to the table's constraint.
		stored.Severity = facts.SeverityInfo
	}
	return stored
}
