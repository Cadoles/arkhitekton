package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// reviewTask is the pipeline's task whose exit code says whether the review was written.
const reviewTask = "review"

// review is the part of Conclave's consolidated review the dashboard keeps.
type review struct {
	Summary  string `json:"summary"`
	Verdict  string `json:"verdict"`
	Findings []struct {
		Category    string   `json:"category"`
		Severity    string   `json:"severity"`
		File        string   `json:"file"`
		StartLine   int      `json:"start_line"`
		EndLine     int      `json:"end_line"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Evidence    string   `json:"evidence"`
		Suggestion  string   `json:"suggestion"`
		ReportedBy  []string `json:"reported_by"`
		OutOfScope  bool     `json:"out_of_scope"`
	} `json:"findings"`
	Questions       []json.RawMessage `json:"questions"`
	FailedReviewers []struct {
		ID string `json:"id"`
	} `json:"failed_reviewers"`
	Meta struct {
		HeadSHA            string            `json:"head_sha"`
		ReviewersTotal     int               `json:"reviewers_total"`
		ReviewersSucceeded int               `json:"reviewers_succeeded"`
		Models             map[string]string `json:"models"`
	} `json:"meta"`
}

// verdicts maps Conclave's verdict onto a set's: a request for changes fails the review, a comment
// warns.
var verdicts = map[string]facts.Status{
	"approve": facts.StatusPass, "comment": facts.StatusWarn, "request_changes": facts.StatusFail,
}

// reviews reads a review into findings, with the review as written kept beside them.
type reviews struct{}

func (reviews) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	set := facts.Set{Shape: facts.ShapeFindings, Group: Group, Tool: "conclave"}
	var report *review
	for _, record := range records {
		if record.Err != nil {
			return nil, fmt.Errorf("review %s unreadable: %w", record.Member, record.Err)
		}
		switch payload := record.Payload.(type) {
		case parse.Attached:
			set.Attachments = append(set.Attachments, facts.Attachment{
				Name: path.Base(record.Member), MediaType: payload.MediaType,
				Object: payload.Object, Size: payload.Size,
			})
		case parse.Bytes:
			if len(bytes.TrimSpace(payload)) == 0 {
				// The task stopped before writing: no report, which is not an unreadable one.
				continue
			}
			var read review
			if err := json.Unmarshal(payload, &read); err != nil {
				return nil, fmt.Errorf("review %s: %w", record.Member, err)
			}
			report, set.RawObject = &read, record.Object
		}
	}

	if report == nil {
		// No review: the task stopped before writing one, and what it left is the agents' trace at
		// most. Not a review that found nothing.
		set.Status = facts.StatusNA
		summary, ran := interpret.NotConcluded(run, reviewTask)
		if !ran {
			summary = "la revue n'a pas été écrite"
		}
		set.Summary = summary
		return []facts.Set{set}, nil
	}

	status, known := verdicts[report.Verdict]
	if !known {
		return nil, fmt.Errorf("review: unknown verdict %q", report.Verdict)
	}
	set.Status, set.Summary, set.Findings = status, report.Summary, findingsOf(report)
	set.Attributes = attributesOf(report)
	return []facts.Set{set}, nil
}

func findingsOf(report *review) *facts.Findings {
	findings := &facts.Findings{BySeverity: map[facts.Severity]int{}, Items: []facts.Finding{}}
	for _, found := range report.Findings {
		severity := facts.Severity(strings.ToUpper(found.Severity))
		finding := facts.Finding{
			Fingerprint: fingerprint(found.File, found.Title), Tool: strings.Join(found.ReportedBy, ", "),
			Category: "review", RuleID: found.Category, Severity: severity, Title: found.Title,
			Description: describe(found.Description, found.Evidence, found.Suggestion),
			Location:    location(found.File, found.StartLine, found.EndLine), Status: "open",
		}
		if found.OutOfScope {
			// Kept, for what it says of the code around, but not held against the request.
			finding.Suppressed, finding.SuppressionReason = true, "hors du périmètre de la pull request"
			findings.Suppressed++
		} else {
			findings.BySeverity[severity]++
		}
		findings.Items = append(findings.Items, finding)
		findings.Total++
	}
	return findings
}

// fingerprint identifies a finding across pushes: the same remark on the same file is the same
// finding, whatever line it moved to.
func fingerprint(file, title string) string {
	sum := sha256.Sum256([]byte(file + "\x00" + strings.ToLower(strings.TrimSpace(title))))
	return hex.EncodeToString(sum[:8])
}

func location(file string, start, end int) string {
	switch {
	case file == "":
		return ""
	case start <= 0:
		return file
	case end <= start:
		return file + ":" + strconv.Itoa(start)
	default:
		return fmt.Sprintf("%s:%d-%d", file, start, end)
	}
}

func describe(description, evidence, suggestion string) string {
	parts := []string{strings.TrimSpace(description)}
	if evidence = strings.TrimSpace(evidence); evidence != "" {
		parts = append(parts, "**Preuve** : "+evidence)
	}
	if suggestion = strings.TrimSpace(suggestion); suggestion != "" {
		parts = append(parts, "**Suggestion** : "+suggestion)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// maxAttribute keeps an attribute within what a set may carry.
const maxAttribute = 1024

func attributesOf(report *review) map[string]string {
	attributes := map[string]string{"verdict": report.Verdict}
	if report.Meta.HeadSHA != "" {
		attributes["head"] = report.Meta.HeadSHA
	}
	if report.Meta.ReviewersTotal > 0 {
		attributes["reviewers"] = fmt.Sprintf("%d/%d", report.Meta.ReviewersSucceeded, report.Meta.ReviewersTotal)
	}
	if len(report.Questions) > 0 {
		attributes["questions"] = strconv.Itoa(len(report.Questions))
	}
	if len(report.FailedReviewers) > 0 {
		failed := make([]string, 0, len(report.FailedReviewers))
		for _, reviewer := range report.FailedReviewers {
			failed = append(failed, reviewer.ID)
		}
		attributes["failed"] = cut(strings.Join(failed, ", "))
	}
	if len(report.Meta.Models) > 0 {
		agents := make([]string, 0, len(report.Meta.Models))
		for agent, model := range report.Meta.Models {
			agents = append(agents, agent+": "+model)
		}
		sort.Strings(agents)
		attributes["models"] = cut(strings.Join(agents, ", "))
	}
	return attributes
}

func cut(value string) string {
	if len(value) <= maxAttribute {
		return value
	}
	return strings.ToValidUTF8(value[:maxAttribute-len("…")], "") + "…"
}
