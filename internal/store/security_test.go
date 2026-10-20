package store

import (
	"context"
	"errors"
	"testing"
)

func auditFixture(t *testing.T, db *DB, failed []string) (int64, int64) {
	t.Helper()
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "security-audit-1")

	resultID, err := db.SaveTestResult(ctx, TestResult{Producer: "test@1", Shape: "findings",
		RunID: runID, ReleaseID: &releaseID, Group: "security",
		Tool: "security-audit", Status: "warn", Total: 3,
	}, nil)
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	if err := db.SaveSecurityAudit(ctx, SecurityAudit{
		TestResultID: resultID, RunID: "2026-08-14T10-00-00Z",
		Total: 3, Critical: 0, High: 1, Medium: 2, Suppressed: 2,
		DeltaNew: 1, DeltaExisting: 2, DeltaResolved: 4,
		FailedScanners: failed,
		ScannerStatus:  map[string]string{"trivy-fs": "ok"},
		Policy:         map[string]string{"fail_on": "CRITICAL"},
	}, []SecurityFinding{
		{Fingerprint: "a", RunID: "r", TargetKind: "repo", TargetName: "mse",
			Tool: "trivy", Category: "sca", RuleID: "CVE-1", Severity: "HIGH",
			Title: "Faille", PackageName: "guzzle", PackageInstalled: "7.4.0",
			PackageFixed: "7.8.2", Fixable: true, Status: "new"},
		{Fingerprint: "b", RunID: "r", TargetKind: "repo", TargetName: "mse",
			Tool: "semgrep", Category: "sast", RuleID: "php.sql", Severity: "MEDIUM",
			Title: "Concatenation", Status: "existing",
			Suppressed: true, SuppressionReason: "false positive"},
		{Fingerprint: "c", RunID: "r", TargetKind: "repo", TargetName: "mse",
			Tool: "trivy", Category: "sca", RuleID: "CVE-2", Severity: "CRITICAL",
			Title: "Critique", Status: "new"},
	}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	return releaseID, resultID
}

func TestLatestSecurityAudit(t *testing.T) {
	db := migrated(t)
	releaseID, resultID := auditFixture(t, db, nil)

	audit, err := db.LatestSecurityAudit(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if audit.TestResultID != resultID || audit.High != 1 || audit.DeltaResolved != 4 {
		t.Errorf("audit = %+v", audit)
	}
	if audit.Policy["fail_on"] != "CRITICAL" || audit.ScannerStatus["trivy-fs"] != "ok" {
		t.Errorf("policy = %v, status = %v", audit.Policy, audit.ScannerStatus)
	}
	if !audit.Measured() {
		t.Error("no failed scanner: the audit must be measurable")
	}
}

// A scanner that did not run does not prove no vulnerability exists.
func TestAnAuditWithAFailedScannerIsNotMeasured(t *testing.T) {
	db := migrated(t)
	releaseID, _ := auditFixture(t, db, []string{"trivy-image"})

	audit, err := db.LatestSecurityAudit(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if audit.Measured() {
		t.Error("a failed scanner must remove the signal from the score")
	}
	if len(audit.FailedScanners) != 1 {
		t.Errorf("scanners = %v", audit.FailedScanners)
	}
}

// Severity order is not alphabetical: a textual sort would put HIGH after LOW and MEDIUM.
func TestFindingsAreOrderedBySeverity(t *testing.T) {
	db := migrated(t)
	_, resultID := auditFixture(t, db, nil)

	findings, err := db.ListSecurityFindings(context.Background(), resultID, true)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(findings) != 3 {
		t.Fatalf("%d finding(s)", len(findings))
	}
	if findings[0].Severity != "CRITICAL" || findings[1].Severity != "HIGH" {
		t.Errorf("ordre = %s, %s, %s",
			findings[0].Severity, findings[1].Severity, findings[2].Severity)
	}
}

// A suppressed finding stays recorded; it is simply left out of the count.
func TestSuppressedFindingsAreKeptButFilterable(t *testing.T) {
	db := migrated(t)
	_, resultID := auditFixture(t, db, nil)
	ctx := context.Background()

	all, _ := db.ListSecurityFindings(ctx, resultID, true)
	active, _ := db.ListSecurityFindings(ctx, resultID, false)

	if len(all) != 3 || len(active) != 2 {
		t.Errorf("%d in total, %d active", len(all), len(active))
	}
	for _, finding := range active {
		if finding.Suppressed {
			t.Error("a suppressed finding must not appear among the active ones")
		}
	}
	// The reason accompanies the suppression: without it, the exception is indefensible in
	// review.
	for _, finding := range all {
		if finding.Suppressed && finding.SuppressionReason == "" {
			t.Error("a suppression with no reason must not be kept silent")
		}
	}
}

// The gap between the installed version and the one that fixes it is what decides an
// action.
func TestPackageVersionsAreKeptApart(t *testing.T) {
	db := migrated(t)
	_, resultID := auditFixture(t, db, nil)

	findings, _ := db.ListSecurityFindings(context.Background(), resultID, false)
	for _, finding := range findings {
		if finding.Fingerprint != "a" {
			continue
		}
		if finding.PackageInstalled != "7.4.0" || finding.PackageFixed != "7.8.2" {
			t.Errorf("package = %+v", finding)
		}
		return
	}
	t.Error("finding not found")
}

func TestLatestSecurityAuditOnAReleaseWithout(t *testing.T) {
	db := migrated(t)
	releaseID := releaseFor(t, db, "1.9.0-rc1")

	if _, err := db.LatestSecurityAudit(context.Background(), releaseID); !errors.Is(err, ErrAuditNotFound) {
		t.Errorf("error = %v, expected ErrAuditNotFound", err)
	}
}
