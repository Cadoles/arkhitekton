package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"forge.cadoles.com/CNOUS/mse-tekton/images/tekton-mse-manager/pkg/security"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
Ingestion des audits de sécurité.

Le dashboard **ne renormalise rien**. Le contrat de `pkg/security` est verrouillé
par P7 et importé tel quel : mêmes types, mêmes empreintes, même échelle de
sévérité. Réimplémenter ici la fusion des sévérités ou la déduplication donnerait
deux vérités sur les mêmes findings — celle du pipeline, qui décide si le build
échoue, et celle de l'interface, qui l'affiche. Elles divergeraient au premier
scanner ajouté, et c'est l'interface qu'on croirait.

Deux fichiers sont lus, et les deux comptent. `findings.jsonl` porte le détail ;
`summary.json` porte ce que le détail ne peut pas dire — quels scanners ont
échoué. Un audit dont Trivy n'a pas tourné produit zéro vulnérabilité d'image,
exactement comme un audit propre.
*/

// SecurityStore est ce que l'ingesteur attend de la base.
type SecurityStore interface {
	SaveTestResult(ctx context.Context, result store.TestResult, cases []store.TestCase) (int64, error)
	TestResultExists(ctx context.Context, runID int64, tool string) (bool, error)
	SaveSecurityAudit(ctx context.Context, audit store.SecurityAudit, findings []store.SecurityFinding) error
}

// SecurityIngester enregistre les audits publiés par un run.
type SecurityIngester struct {
	store   SecurityStore
	objects Objects
	logger  *slog.Logger
}

func NewSecurityIngester(audits SecurityStore, objects Objects, logger *slog.Logger) *SecurityIngester {
	return &SecurityIngester{store: audits, objects: objects, logger: logger}
}

// securityTool est le nom sous lequel l'audit est enregistré dans `test_results`.
//
// Un seul nom pour tous les scanners : ce qui est enregistré est le rapport
// **normalisé**, pas la sortie de chaque outil. Les scanners individuels sont
// nommés dans `scanner_status`.
const securityTool = "security-audit"

// Ingest lit le rapport normalisé d'un run terminé.
func (i *SecurityIngester) Ingest(ctx context.Context, run LoadRun) error {
	prefix := "raw/" + run.Name + "/"
	objects, err := i.objects.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("artefacts du run %q: %w", run.Name, err)
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
		// Pas d'audit dans ce run : ce n'est pas une anomalie, la plupart des
		// pipelines n'en produisent pas.
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

	// Le statut du rapport suit la politique appliquée par le manager, et non un
	// seuil réinventé ici : c'est lui qui a décidé si le pipeline échouait.
	status := results.ReportPass
	switch {
	case !audit.Measured():
		// Un scanner en échec ne permet aucun verdict. `na` et non `fail` : rien
		// ne dit que le code est mauvais, seulement qu'on n'a pas pu regarder.
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

	i.logger.Info("audit de sécurité enregistré",
		"run", run.Name, "findings", len(stored), "critiques", audit.Critical,
		"hauts", audit.High, "scanners en échec", audit.FailedScanners)
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
		return summary, fmt.Errorf("résumé d'audit %q illisible: %w", key, err)
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

	// `UnmarshalFindings` est celui du manager : le format JSONL et sa tolérance
	// aux lignes vides sont son affaire, pas la nôtre.
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

	// Les compteurs viennent de la table du manager, indexée par le libellé de
	// sévérité. Les recompter depuis les findings donnerait un autre résultat :
	// `BySeverity` exclut déjà les supprimés et les résolus.
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
		// Ne devrait pas arriver : `pkg/security` ramène tous les outils à son
		// échelle. Le cas est traité pour que l'audit ne soit pas perdu en entier
		// — la contrainte de la table refuserait la chaîne vide — et `INFO` est
		// retenu faute de mieux : inventer `HIGH` serait aussi faux, dans l'autre
		// sens.
		stored.Severity = string(security.SeverityInfo)
	}
	return stored
}

func summarizeAudit(audit store.SecurityAudit) string {
	if !audit.Measured() {
		return fmt.Sprintf("audit incomplet : %s n'a pas conclu",
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
		parts = append(parts, fmt.Sprintf("%d supprimé(s) par exception", audit.Suppressed))
	}
	return strings.Join(parts, ", ")
}
