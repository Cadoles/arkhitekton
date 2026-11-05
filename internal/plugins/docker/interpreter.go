package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// scanTask is the pipeline's task whose exit code says whether the report is whole.
const scanTask = "run-trivy"

// trivyTool names the scanner in what the dashboard records.
const trivyTool = "trivy"

type trivyCVSS struct {
	V3Score float64 `json:"V3Score"`
}

type trivyReport struct {
	ArtifactName string `json:"ArtifactName"`
	Results      []struct {
		Target          string `json:"Target"`
		Vulnerabilities []struct {
			VulnerabilityID  string               `json:"VulnerabilityID"`
			PkgName          string               `json:"PkgName"`
			InstalledVersion string               `json:"InstalledVersion"`
			FixedVersion     string               `json:"FixedVersion"`
			Severity         string               `json:"Severity"`
			Title            string               `json:"Title"`
			Description      string               `json:"Description"`
			CVSS             map[string]trivyCVSS `json:"CVSS"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// Trivy's severities that are not the dashboard's own: it knows no `UNKNOWN`.
var trivySeverities = map[string]facts.Severity{
	"CRITICAL": facts.SeverityCritical, "HIGH": facts.SeverityHigh, "MEDIUM": facts.SeverityMedium,
	"LOW": facts.SeverityLow,
}

// scanned reads Trivy's report into a security audit. A critical or high finding fails it, as it
// lowers the score; the run itself stays green, the image being already published.
type scanned struct{}

func (scanned) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	audit := facts.Set{Shape: facts.ShapeFindings, Group: "security", Tool: trivyTool}
	if summary, stopped := unfinished(run); stopped {
		// What a tool killed on its way left is partial: reading it would pass a crash for a result.
		audit.Status, audit.Summary = facts.StatusNA, summary
		return []facts.Set{audit}, nil
	}
	for _, record := range records {
		raw, ok := record.Payload.(parse.Bytes)
		if record.Err != nil || !ok {
			return nil, fmt.Errorf("trivy report %s unreadable: %v", record.Member, record.Err)
		}
		findings, err := readFindings(raw)
		if err != nil {
			return nil, err
		}
		blocking := findings.BySeverity[facts.SeverityCritical] + findings.BySeverity[facts.SeverityHigh]
		audit.RawObject, audit.Findings, audit.Status = record.Object, findings, facts.StatusPass
		if blocking > 0 {
			audit.Status = facts.StatusFail
		}
		audit.Summary = fmt.Sprintf("%d vulnérabilité(s) critique(s) ou élevée(s), %d au total", blocking, findings.Total)
		return []facts.Set{audit}, nil
	}
	return nil, fmt.Errorf("no trivy report")
}

func unfinished(run capability.RunState) (string, bool) {
	for _, state := range run.Tasks {
		if state.Name != scanTask {
			continue
		}
		for _, step := range state.Steps {
			if step.ExitCode != nil && *step.ExitCode != 0 {
				return interpret.NotConcluded(run, scanTask)
			}
		}
	}
	return "", false
}

func readFindings(raw []byte) (*facts.Findings, error) {
	var report trivyReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("trivy report: %w", err)
	}

	findings := &facts.Findings{BySeverity: map[facts.Severity]int{}}
	seen := map[string]bool{}
	for _, result := range report.Results {
		for _, found := range result.Vulnerabilities {
			// The same package may be reported by several targets of the image: one thing to upgrade.
			fingerprint := found.VulnerabilityID + ":" + found.PkgName + ":" + found.InstalledVersion
			if seen[fingerprint] {
				continue
			}
			seen[fingerprint] = true

			severity, known := trivySeverities[found.Severity]
			if !known {
				severity = facts.SeverityInfo
			}
			title := found.Title
			if title == "" {
				title = found.VulnerabilityID
			}
			findings.Items = append(findings.Items, facts.Finding{
				Fingerprint: fingerprint, TargetKind: "image", TargetName: report.ArtifactName,
				Tool: trivyTool, Category: "vulnerability", RuleID: found.VulnerabilityID,
				Severity: severity, CVSS: score(found.CVSS), Title: title, Description: found.Description,
				Location: result.Target, PackageName: found.PkgName, PackageInstalled: found.InstalledVersion,
				PackageFixed: found.FixedVersion, Fixable: found.FixedVersion != "", Status: "open",
			})
			findings.BySeverity[severity]++
			findings.Total++
		}
	}
	return findings, nil
}

// score is the highest score a source gives: Trivy reports one by database.
func score(sources map[string]trivyCVSS) float64 {
	best := 0.0
	for _, source := range sources {
		if source.V3Score > best {
			best = source.V3Score
		}
	}
	return best
}
