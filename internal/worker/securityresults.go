package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"forge.cadoles.com/CNOUS/mse-tekton/images/tekton-mse-manager/pkg/security"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Ingesting the security audits.
 *
 * The dashboard **renormalises nothing**. `pkg/security`'s contract is imported as it is: same
 * types, same fingerprints, same severity scale. Reimplementing the merging of severities or
 * the deduplication here would give two truths about the same findings — the pipeline's, which
 * decides whether the build fails, and the interface's, which displays it. They would diverge
 * at the first scanner added, and it is the interface one would believe.
 *
 * Two files are read, and both count. `findings.jsonl` carries the detail; `summary.json`
 * carries what the detail cannot say — which scanners failed. An audit in which Trivy did not
 * run produces zero image vulnerabilities, exactly like a clean audit.
 */

// SecurityStore is what the ingester needs from the database.
type SecurityStore interface {
	SaveTestResult(ctx context.Context, result store.TestResult, cases []store.TestCase) (int64, error)
	TestResultExists(ctx context.Context, runID int64, tool string) (bool, error)
	SaveSecurityAudit(ctx context.Context, audit store.SecurityAudit, findings []store.SecurityFinding) error
}

// SecurityIngester records the audits a run published.
type SecurityIngester struct {
	store   SecurityStore
	objects Objects
	logger  *slog.Logger
}

func NewSecurityIngester(audits SecurityStore, objects Objects, logger *slog.Logger) *SecurityIngester {
	return &SecurityIngester{store: audits, objects: objects, logger: logger}
}

// securityTool is the name under which the audit is recorded in `test_results`.
//
// A single name for every scanner: what is recorded is the **normalised** report, not each
// tool's output. The individual scanners are named in `scanner_status`.
const securityTool = "security-audit"

// Ingest reads a finished run's normalised report.
func (i *SecurityIngester) Ingest(ctx context.Context, run LoadRun) error {
	prefix := "raw/" + run.Name + "/"
	objects, err := i.objects.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("artifacts of run %q: %w", run.Name, err)
	}

	var summaryKey, findingsKey string
	for _, object := range objects {
		switch path.Base(object.Key) {
		case security.SummaryFileName:
			summaryKey = object.Key
		case security.FindingsFileName:
			findingsKey = object.Key
		}
	}

	if summaryKey == "" {
		// No audit in this run: that is not an anomaly, most pipelines produce none.
		return nil
	}

	already, err := i.store.TestResultExists(ctx, run.ID, securityTool)
	if err != nil {
		return err
	}
	if already {
		return nil
	}

	summary, err := i.readSummary(ctx, summaryKey)
	if err != nil {
		return err
	}

	var findings []security.Finding
	if findingsKey != "" {
		findings, err = i.readFindings(ctx, findingsKey)
		if err != nil {
			return err
		}
	}

	audit := toAudit(summary)

	// The report's status follows the policy the manager applied, and not a threshold reinvented
	// here: it is the manager that decided whether the pipeline failed.
	status := results.ReportPass
	switch {
	case !audit.Measured():
		// A failed scanner allows no verdict. `na` and not `fail`: nothing says the code is bad, only
		// that it could not be looked at.
		status = results.ReportNA
	case audit.Critical > 0:
		status = results.ReportFail
	case audit.High > 0:
		status = results.ReportWarn
	}

	resultID, err := i.store.SaveTestResult(ctx, store.TestResult{
		RunID: run.ID, ReleaseID: run.ReleaseID,
		Family: string(results.FamilySecurity), Tool: securityTool, Status: status,
		Total: audit.Total, Failed: audit.Critical + audit.High,
		Summary:   summarizeAudit(audit),
		RawObject: summaryKey,
	}, nil)
	if err != nil {
		return err
	}

	audit.TestResultID = resultID
	stored := make([]store.SecurityFinding, 0, len(findings))
	for _, finding := range findings {
		stored = append(stored, toFinding(finding))
	}

	if err := i.store.SaveSecurityAudit(ctx, audit, stored); err != nil {
		return err
	}

	i.logger.Info("security audit recorded",
		"run", run.Name, "findings", len(stored), "critiques", audit.Critical,
		"hauts", audit.High, "failed scanners", audit.FailedScanners)
	return nil
}

func (i *SecurityIngester) readSummary(ctx context.Context, key string) (security.Summary, error) {
	var summary security.Summary

	content, err := i.objects.Get(ctx, key)
	if err != nil {
		return summary, err
	}
	defer content.Close()

	if err := json.NewDecoder(content).Decode(&summary); err != nil {
		return summary, fmt.Errorf("audit summary %q unreadable: %w", key, err)
	}
	return summary, nil
}

func (i *SecurityIngester) readFindings(ctx context.Context, key string) ([]security.Finding, error) {
	content, err := i.objects.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer content.Close()

	raw, err := readLimited(content)
	if err != nil {
		return nil, fmt.Errorf("findings %q illisibles: %w", key, err)
	}

	// `UnmarshalFindings` is the manager's: the JSONL format and its tolerance of empty lines are
	// its business, not ours.
	return security.UnmarshalFindings(raw), nil
}

func toAudit(summary security.Summary) store.SecurityAudit {
	audit := store.SecurityAudit{
		RunID: summary.RunID, GeneratedAt: summary.GeneratedAt,
		Total: summary.Total, Suppressed: summary.Suppressed,
		DeltaNew: summary.Delta.New, DeltaExisting: summary.Delta.Existing,
		DeltaResolved:  summary.Delta.Resolved,
		FailedScanners: summary.FailedScanners,
		ScannerStatus:  summary.ScannerStatus,
		Policy:         summary.Policy,
		PreviousRunID:  summary.PreviousRunID,
	}

	// The counters come from the manager's table, indexed by the severity label. Recounting them
	// from the findings would give a different result: `BySeverity` already excludes the suppressed
	// and the resolved ones.
	audit.Critical = summary.BySeverity[string(security.SeverityCritical)]
	audit.High = summary.BySeverity[string(security.SeverityHigh)]
	audit.Medium = summary.BySeverity[string(security.SeverityMedium)]
	audit.Low = summary.BySeverity[string(security.SeverityLow)]

	return audit
}

func toFinding(finding security.Finding) store.SecurityFinding {
	stored := store.SecurityFinding{
		Fingerprint: finding.Fingerprint, RunID: finding.RunID,
		TargetKind: finding.Target.Kind, TargetName: finding.Target.Name,
		Tool: finding.Tool.Name, Category: finding.Category, RuleID: finding.RuleID,
		Severity: string(finding.Severity), CVSS: finding.CVSS,
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
		// Should not happen: `pkg/security` brings every tool back to its scale. The case is handled so
		// that the audit is not lost whole — the table's constraint would refuse an empty string — and
		// `INFO` is kept for want of better: inventing `HIGH` would be just as wrong, the other way
		// round.
		stored.Severity = string(security.SeverityInfo)
	}
	return stored
}

func summarizeAudit(audit store.SecurityAudit) string {
	if !audit.Measured() {
		return fmt.Sprintf("incomplete audit: %s did not conclude",
			strings.Join(audit.FailedScanners, ", "))
	}

	parts := []string{fmt.Sprintf("%d finding(s)", audit.Total)}
	if audit.Critical > 0 {
		parts = append(parts, fmt.Sprintf("%d critique(s)", audit.Critical))
	}
	if audit.High > 0 {
		parts = append(parts, fmt.Sprintf("%d haut(s)", audit.High))
	}
	if audit.DeltaNew > 0 {
		parts = append(parts, fmt.Sprintf("%d nouveau(x)", audit.DeltaNew))
	}
	if audit.Suppressed > 0 {
		parts = append(parts, fmt.Sprintf("%d suppressed by exception", audit.Suppressed))
	}
	return strings.Join(parts, ", ")
}
