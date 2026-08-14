package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SecurityAudit est l'agrégat d'un audit, tel que `summary.json` le porte.
type SecurityAudit struct {
	TestResultID  int64
	RunID         string
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
	// FailedScanners nomme les scanners qui n'ont pas conclu. C'est ce qui
	// distingue « aucune vulnérabilité » de « on n'a pas pu regarder », et donc
	// ce qui décide si le signal de sécurité entre dans le score.
	FailedScanners []string
	ScannerStatus  map[string]string
	Policy         map[string]string
	PreviousRunID  string
	CreatedAt      time.Time
}

// Measured dit si l'audit peut noter la sécurité.
//
// Un seul scanner en échec suffit à retirer le signal du score. La règle est
// volontairement stricte : Trivy qui n'a pas tourné ne prouve pas qu'aucune
// vulnérabilité n'existe, et un score calculé sur les seuls scanners ayant
// abouti afficherait « conforme » sur un audit partiel — l'erreur la plus chère
// possible sur ce signal.
func (a SecurityAudit) Measured() bool { return len(a.FailedScanners) == 0 }

// SecurityFinding est un finding normalisé.
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
var ErrAuditNotFound = errors.New("audit de sécurité introuvable")

// SaveSecurityAudit enregistre un audit et ses findings.
//
// En une transaction : un audit dont la moitié des findings manque produirait un
// verdict faux dans le sens le plus dangereux — moins de critiques que la
// réalité.
func (db *DB) SaveSecurityAudit(
	ctx context.Context, audit SecurityAudit, findings []SecurityFinding,
) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("audit de sécurité: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// `text[] not null` : un nil deviendrait NULL. Normalisé ici et non chez
	// l'appelant — une contrainte de colonne se défend dans le paquet qui
	// possède la colonne, sinon le second appelant la redécouvre en production.
	if audit.FailedScanners == nil {
		audit.FailedScanners = []string{}
	}

	status, err := json.Marshal(audit.ScannerStatus)
	if err != nil {
		return fmt.Errorf("audit de sécurité: %w", err)
	}
	policy, err := json.Marshal(audit.Policy)
	if err != nil {
		return fmt.Errorf("audit de sécurité: %w", err)
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
		return fmt.Errorf("audit de sécurité %q: %w", audit.RunID, err)
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
		return fmt.Errorf("audit de sécurité: %w", err)
	}
	return nil
}

// LatestSecurityAudit rend l'audit le plus récent d'une release.
func (db *DB) LatestSecurityAudit(ctx context.Context, releaseID int64) (SecurityAudit, error) {
	var audit SecurityAudit
	var status, policy []byte
	var generatedAt, previousRunID *string

	err := db.pool.QueryRow(ctx,
		`select a.test_result_id, a.run_id, a.generated_at, a.total,
		        a.critical, a.high, a.medium, a.low, a.suppressed,
		        a.delta_new, a.delta_existing, a.delta_resolved,
		        a.failed_scanners, a.scanner_status, a.policy, a.previous_run_id, a.created_at
		 from security_audits a
		 join test_results r on r.id = a.test_result_id
		 where r.release_id = $1
		 order by a.created_at desc
		 limit 1`, releaseID).
		Scan(&audit.TestResultID, &audit.RunID, &generatedAt, &audit.Total,
			&audit.Critical, &audit.High, &audit.Medium, &audit.Low, &audit.Suppressed,
			&audit.DeltaNew, &audit.DeltaExisting, &audit.DeltaResolved,
			&audit.FailedScanners, &status, &policy, &previousRunID, &audit.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit, ErrAuditNotFound
		}
		return audit, fmt.Errorf("audit de la release %d: %w", releaseID, err)
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

// ListSecurityFindings rend les findings d'un audit.
//
// Triés par sévérité décroissante puis par titre : c'est l'ordre dans lequel on
// les traite, et le tri en base évite de rapatrier des milliers de lignes pour
// les ordonner ensuite.
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
	// L'ordre de sévérité n'est pas alphabétique : CRITICAL précède HIGH, qui
	// précède MEDIUM. Un `order by severity` textuel mettrait CRITICAL après
	// aucun autre, mais HIGH après LOW et MEDIUM.
	query += ` order by case severity
	               when 'CRITICAL' then 0 when 'HIGH' then 1 when 'MEDIUM' then 2
	               when 'LOW' then 3 else 4 end, title`

	rows, err := db.pool.Query(ctx, query, resultID)
	if err != nil {
		return nil, fmt.Errorf("findings de l'audit %d: %w", resultID, err)
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
			return nil, fmt.Errorf("findings de l'audit %d: %w", resultID, err)
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}
