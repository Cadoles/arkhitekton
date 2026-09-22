package worker

import (
	"context"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeSecurityStore struct {
	results  []store.TestResult
	audits   []store.SecurityAudit
	findings [][]store.SecurityFinding
	known    map[string]bool
}

func (f *fakeSecurityStore) SaveTestResult(
	_ context.Context, result store.TestResult, _ []store.TestCase,
) (int64, error) {
	f.results = append(f.results, result)
	return int64(len(f.results)), nil
}

func (f *fakeSecurityStore) TestResultExists(_ context.Context, _ int64, tool string) (bool, error) {
	return f.known[tool], nil
}

func (f *fakeSecurityStore) SaveSecurityAudit(
	_ context.Context, audit store.SecurityAudit, findings []store.SecurityFinding,
) error {
	f.audits = append(f.audits, audit)
	f.findings = append(f.findings, findings)
	return nil
}

const cleanSummary = `{
  "run_id": "2026-08-14T10-00-00Z",
  "generated_at": "2026-08-14T10:05:00Z",
  "total": 3,
  "by_severity": {"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 0},
  "by_category": {"sca": 3},
  "delta": {"new": 1, "existing": 2, "resolved": 4},
  "suppressed": 2,
  "scanner_status": {"trivy-fs": "ok", "composer-audit": "ok"},
  "policy": {"fail_on": "CRITICAL"},
  "previous_run_id": "2026-08-01T10-00-00Z"
}`

const brokenSummary = `{
  "run_id": "2026-08-14T11-00-00Z",
  "total": 0,
  "by_severity": {},
  "delta": {"new": 0, "existing": 0, "resolved": 0},
  "scanner_status": {"trivy-image": "error", "composer-audit": "ok"},
  "failed_scanners": ["trivy-image"]
}`

const findingsJSONL = `{"fingerprint":"abc123","run_id":"2026-08-14T10-00-00Z","target":{"kind":"repo","name":"mse"},"tool":{"name":"trivy"},"category":"sca","rule_id":"CVE-2026-1111","severity":"HIGH","cvss":7.5,"title":"Flaw in guzzle","package":{"name":"guzzlehttp/guzzle","installed":"7.4.0","fixed":"7.8.2"},"fixable":true,"status":"new","suppression":null}
{"fingerprint":"def456","run_id":"2026-08-14T10-00-00Z","target":{"kind":"repo","name":"mse"},"tool":{"name":"semgrep"},"category":"sast","rule_id":"php.lang.security","severity":"MEDIUM","title":"SQL concatenation","location":{"file":"src/Repo.php","start_line":42},"status":"existing","suppression":{"reason":"validated false positive","approved_by":"vcarroy","expires":"2026-12-31"}}
`

func newSecurityIngester(objects fakeObjects) (*fakeSecurityStore, *SecurityIngester) {
	audits := &fakeSecurityStore{known: map[string]bool{}}
	return audits, NewSecurityIngester(audits, objects, slog.New(slog.DiscardHandler))
}

func TestIngestSecurityAudit(t *testing.T) {
	objects := fakeObjects{
		"raw/security-audit-1/report/summary.json":   cleanSummary,
		"raw/security-audit-1/report/findings.jsonl": findingsJSONL,
	}
	audits, ingester := newSecurityIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "security-audit-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	if len(audits.audits) != 1 {
		t.Fatalf("%d audit(s)", len(audits.audits))
	}
	audit := audits.audits[0]
	// The counters come from `by_severity`, which already excludes the suppressed and the resolved
	// ones. Recounting them from the findings would give something else.
	if audit.High != 1 || audit.Medium != 2 || audit.Critical != 0 {
		t.Errorf("compteurs = %+v", audit)
	}
	if audit.DeltaResolved != 4 || audit.Suppressed != 2 {
		t.Errorf("delta = %+v", audit)
	}
	if !audit.Measured() {
		t.Error("an audit whose every scanner concluded must be measurable")
	}

	// A HIGH with no critical: an alert, not a failure.
	if audits.results[0].Status != "warn" {
		t.Errorf("status = %q, expected warn", audits.results[0].Status)
	}
}

// The heart of the signal: a failed scanner produces zero findings, exactly like a clean audit.
// Only its descriptor tells them apart, and confusing them would display "compliant" on a
// partial audit.
func TestAFailedScannerMakesTheAuditUnmeasurable(t *testing.T) {
	objects := fakeObjects{"raw/security-audit-2/report/summary.json": brokenSummary}
	audits, ingester := newSecurityIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "security-audit-2"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	audit := audits.audits[0]
	if audit.Measured() {
		t.Error("a failed scanner must take the signal out of the score")
	}
	if len(audit.FailedScanners) != 1 || audit.FailedScanners[0] != "trivy-image" {
		t.Errorf("failed scanners = %v", audit.FailedScanners)
	}
	// `na` and not `fail`: nothing says the code is bad, only that it could not be looked at.
	if audits.results[0].Status != "na" {
		t.Errorf("status = %q, expected na", audits.results[0].Status)
	}
	// The summary names the culprit.
	if audits.results[0].Summary == "" || audits.results[0].Total != 0 {
		t.Errorf("result = %+v", audits.results[0])
	}
}

// The manager's contract is consumed as it is: fingerprint, severity, status, suppression.
// Nothing is renormalised here.
func TestFindingsKeepTheManagerContract(t *testing.T) {
	objects := fakeObjects{
		"raw/security-audit-1/report/summary.json":   cleanSummary,
		"raw/security-audit-1/report/findings.jsonl": findingsJSONL,
	}
	audits, ingester := newSecurityIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "security-audit-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	findings := audits.findings[0]
	if len(findings) != 2 {
		t.Fatalf("%d finding(s)", len(findings))
	}

	byPrint := map[string]store.SecurityFinding{}
	for _, finding := range findings {
		byPrint[finding.Fingerprint] = finding
	}

	guzzle := byPrint["abc123"]
	if guzzle.Severity != "HIGH" || guzzle.PackageInstalled != "7.4.0" || guzzle.PackageFixed != "7.8.2" {
		t.Errorf("finding SCA = %+v", guzzle)
	}
	// `installed` and `fixed` together: it is the gap that decides on an action.
	if !guzzle.Fixable || guzzle.Status != "new" {
		t.Errorf("finding SCA = %+v", guzzle)
	}

	semgrep := byPrint["def456"]
	if semgrep.Location != "src/Repo.php:42" {
		t.Errorf("emplacement = %q", semgrep.Location)
	}
	// A suppressed finding stays recorded, with its reason: the trace of what was set aside is
	// worth as much as the list of what remains.
	if !semgrep.Suppressed || semgrep.SuppressionReason == "" {
		t.Errorf("suppression = %+v", semgrep)
	}
}

// The observer sees a finished run again at restart.
func TestSecurityIngestIsIdempotent(t *testing.T) {
	objects := fakeObjects{"raw/security-audit-1/report/summary.json": cleanSummary}
	audits, ingester := newSecurityIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "security-audit-1"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	audits.known["security-audit"] = true
	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "security-audit-1"}); err != nil {
		t.Fatalf("seconde: %v", err)
	}
	if len(audits.audits) != 1 {
		t.Errorf("%d audit(s), expected 1", len(audits.audits))
	}
}

// Most pipelines produce no audit: that is not an anomaly.
func TestARunWithoutAuditIsIgnored(t *testing.T) {
	audits, ingester := newSecurityIngester(fakeObjects{"raw/unit-1/junit.xml": "<testsuites/>"})

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 3, Name: "unit-1"}); err != nil {
		t.Errorf("error = %v, expected none", err)
	}
	if len(audits.audits) != 0 {
		t.Error("nothing must be recorded")
	}
}

// A summary with no findings.jsonl stays usable: the counters are enough for the score, and the
// detail is only a display convenience.
func TestASummaryWithoutFindingsIsStillIngested(t *testing.T) {
	objects := fakeObjects{"raw/security-audit-1/report/summary.json": cleanSummary}
	audits, ingester := newSecurityIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "security-audit-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(audits.audits) != 1 || len(audits.findings[0]) != 0 {
		t.Errorf("audit = %+v", audits.audits)
	}
	if audits.audits[0].High != 1 {
		t.Error("the counters must stay usable")
	}
}
