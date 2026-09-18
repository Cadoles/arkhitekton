package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SecurityAudit is an audit's aggregate, as the summary report carries it.
type SecurityAudit struct {
	TestResultID int64
	// RunID is the identifier the audit gives itself, as the normaliser writes it into its
	// report — not a PipelineRun's.
	RunID string
	// RunName is the PipelineRun that produced the audit. Empty if its projection was purged:
	// the report outlives it.
	RunName       string
	GeneratedAt   string
	Total         int
	Critical      int
	High          int
	Medium        int
	Low           int
	Suppressed    int
	DeltaNew      int
	DeltaExisting int
	DeltaResolved int
	// FailedScanners names the scanners that did not conclude. It is what separates "no
	// vulnerability" from "we could not look", and therefore what decides whether the security
	// signal enters the score.
	FailedScanners []string
	ScannerStatus  map[string]string
	Policy         map[string]string
	PreviousRunID  string
	CreatedAt      time.Time
}

// Measured says whether the audit can grade security.
//
// A single failed scanner is enough to remove the signal from the score. The rule is
// deliberately strict: a scanner that did not run does not prove no vulnerability exists,
// and a score computed on the scanners that did conclude would display "clean" on a
// partial audit — the most expensive possible error on this signal.
func (a SecurityAudit) Measured() bool { return len(a.FailedScanners) == 0 }

// SecurityFinding is a normalised finding.
type SecurityFinding struct {
	ID                 int64
	TestResultID       int64
	Fingerprint        string
	RunID              string
	TargetKind         string
	TargetName         string
	Tool               string
	Category           string
	RuleID             string
	Severity           string
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

// ErrAuditNotFound signale un audit absent.
var ErrAuditNotFound = errors.New("security audit not found")

// SaveSecurityAudit records an audit and its findings.
//
// In one transaction: an audit missing half its findings would produce a false verdict in
// the most dangerous direction — fewer critical findings than reality.
func (db *DB) SaveSecurityAudit(
	ctx context.Context, audit SecurityAudit, findings []SecurityFinding,
) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("security audit: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// `text[] not null`: a nil would become NULL. Normalised here and not in the caller — a
	// column constraint is defended in the package that owns the column, otherwise the second
	// caller rediscovers it in production.
	if audit.FailedScanners == nil {
		audit.FailedScanners = []string{}
	}

	status, err := json.Marshal(audit.ScannerStatus)
	if err != nil {
		return fmt.Errorf("security audit: %w", err)
	}
	policy, err := json.Marshal(audit.Policy)
	if err != nil {
		return fmt.Errorf("security audit: %w", err)
	}

	_, err = transaction.Exec(ctx,
		`insert into security_audits (
		     test_result_id, run_id, generated_at, total, critical, high, medium, low,
		     suppressed, delta_new, delta_existing, delta_resolved,
		     failed_scanners, scanner_status, policy, previous_run_id
		 ) values ($1, $2, nullif($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12,
		           $13, $14, $15, nullif($16, ''))
		 on conflict (test_result_id) do nothing`,
		audit.TestResultID, audit.RunID, audit.GeneratedAt, audit.Total,
		audit.Critical, audit.High, audit.Medium, audit.Low, audit.Suppressed,
		audit.DeltaNew, audit.DeltaExisting, audit.DeltaResolved,
		audit.FailedScanners, status, policy, audit.PreviousRunID)
	if err != nil {
		return fmt.Errorf("security audit %q: %w", audit.RunID, err)
	}

	for _, finding := range findings {
		_, err := transaction.Exec(ctx,
			`insert into security_findings (
			     test_result_id, fingerprint, run_id, target_kind, target_name, tool,
			     category, rule_id, severity, cvss, title, description, location,
			     package_name, package_installed, package_fixed, fixable, status, first_seen,
			     suppressed, suppression_reason, suppression_expires
			 ) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, 0::double precision),
			           $11, nullif($12, ''), nullif($13, ''), nullif($14, ''), nullif($15, ''),
			           nullif($16, ''), $17, $18, nullif($19, ''), $20, nullif($21, ''), nullif($22, ''))`,
			audit.TestResultID, finding.Fingerprint, finding.RunID,
			finding.TargetKind, finding.TargetName, finding.Tool, finding.Category,
			finding.RuleID, finding.Severity, finding.CVSS, finding.Title,
			finding.Description, finding.Location, finding.PackageName,
			finding.PackageInstalled, finding.PackageFixed,
			finding.Fixable, finding.Status, finding.FirstSeen,
			finding.Suppressed, finding.SuppressionReason, finding.SuppressionExpires)
		if err != nil {
			return fmt.Errorf("finding %q: %w", finding.Fingerprint, err)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("security audit: %w", err)
	}
	return nil
}

// LatestSecurityAudit returns a release's most recent audit.
func (db *DB) LatestSecurityAudit(ctx context.Context, releaseID int64) (SecurityAudit, error) {
	var audit SecurityAudit
	var status, policy []byte
	var generatedAt, previousRunID *string

	err := db.pool.QueryRow(ctx,
		`select a.test_result_id, a.run_id, a.generated_at, a.total,
		        a.critical, a.high, a.medium, a.low, a.suppressed,
		        a.delta_new, a.delta_existing, a.delta_resolved,
		        a.failed_scanners, a.scanner_status, a.policy, a.previous_run_id, a.created_at,
		        coalesce(p.pipelinerun_name, '')
		 from security_audits a
		 join test_results r on r.id = a.test_result_id
		 -- The run's name, to send the reader back to the run that produced the
		 -- audit. A loose join: the report outlives the purge of its projection,
		 -- and an audit with no run stays readable.
		 left join runs p on p.id = r.run_id
		 where r.release_id = $1
		 order by a.created_at desc
		 limit 1`, releaseID).
		Scan(&audit.TestResultID, &audit.RunID, &generatedAt, &audit.Total,
			&audit.Critical, &audit.High, &audit.Medium, &audit.Low, &audit.Suppressed,
			&audit.DeltaNew, &audit.DeltaExisting, &audit.DeltaResolved,
			&audit.FailedScanners, &status, &policy, &previousRunID, &audit.CreatedAt,
			&audit.RunName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit, ErrAuditNotFound
		}
		return audit, fmt.Errorf("audit of release %d: %w", releaseID, err)
	}

	if generatedAt != nil {
		audit.GeneratedAt = *generatedAt
	}
	if previousRunID != nil {
		audit.PreviousRunID = *previousRunID
	}
	if len(status) > 0 {
		_ = json.Unmarshal(status, &audit.ScannerStatus)
	}
	if len(policy) > 0 {
		_ = json.Unmarshal(policy, &audit.Policy)
	}
	return audit, nil
}

// ListSecurityFindings returns an audit's findings.
//
// Sorted by descending severity then by title: that is the order they are handled in, and
// sorting in the database avoids fetching thousands of rows to order them afterwards.
func (db *DB) ListSecurityFindings(
	ctx context.Context, resultID int64, includeSuppressed bool,
) ([]SecurityFinding, error) {
	query := `select id, test_result_id, fingerprint, run_id, target_kind, target_name,
	                 tool, category, rule_id, severity, coalesce(cvss, 0),
	                 title, coalesce(description, ''), coalesce(location, ''),
	                 coalesce(package_name, ''), coalesce(package_installed, ''),
	                 coalesce(package_fixed, ''),
	                 fixable, status, coalesce(first_seen, ''), suppressed,
	                 coalesce(suppression_reason, ''), coalesce(suppression_expires, '')
	          from security_findings where test_result_id = $1`
	if !includeSuppressed {
		query += ` and not suppressed`
	}
	// Severity order is not alphabetical: CRITICAL precedes HIGH, which precedes MEDIUM. A
	// textual `order by severity` would put HIGH after LOW and MEDIUM.
	query += ` order by case severity
	               when 'CRITICAL' then 0 when 'HIGH' then 1 when 'MEDIUM' then 2
	               when 'LOW' then 3 else 4 end, title`

	rows, err := db.pool.Query(ctx, query, resultID)
	if err != nil {
		return nil, fmt.Errorf("findings of audit %d: %w", resultID, err)
	}
	defer rows.Close()

	var findings []SecurityFinding
	for rows.Next() {
		var finding SecurityFinding
		if err := rows.Scan(&finding.ID, &finding.TestResultID, &finding.Fingerprint,
			&finding.RunID, &finding.TargetKind, &finding.TargetName, &finding.Tool,
			&finding.Category, &finding.RuleID, &finding.Severity, &finding.CVSS,
			&finding.Title, &finding.Description, &finding.Location,
			&finding.PackageName, &finding.PackageInstalled, &finding.PackageFixed,
			&finding.Fixable,
			&finding.Status, &finding.FirstSeen, &finding.Suppressed,
			&finding.SuppressionReason, &finding.SuppressionExpires); err != nil {
			return nil, fmt.Errorf("findings of audit %d: %w", resultID, err)
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}
