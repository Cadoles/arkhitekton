package http

import (
	"net/http"
	"time"
)

// ReviewGroup is the group a review's findings are read in. It stays out of the score and of the
// security tile: a reviewer's remark is an opinion to weigh, not a measure.
const ReviewGroup = "review"

// reviewPayload is a pull request's latest review.
type reviewPayload struct {
	Reviewed   bool       `json:"reviewed"`
	Status     string     `json:"status,omitempty"`
	Summary    string     `json:"summary,omitempty"`
	Tool       string     `json:"tool,omitempty"`
	Producer   string     `json:"producer,omitempty"`
	RunName    string     `json:"run_name,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	// Attributes say what the review stands on: its verdict, the head it read, how many of its
	// reviewers answered, and their models.
	Attributes  map[string]string   `json:"attributes,omitempty"`
	Findings    []findingPayload    `json:"findings"`
	Attachments []attachmentPayload `json:"attachments"`
}

// PullReview returns a pull request's latest review: its verdict, its findings, and the review as
// written, kept as a file.
func (h *TestsHandler) PullReview(w http.ResponseWriter, r *http.Request) {
	source, ok := h.pullSource(w, r)
	if !ok {
		return
	}
	reports, err := source.results(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the results", "")
		return
	}

	response := reviewPayload{Findings: []findingPayload{}, Attachments: []attachmentPayload{}}
	for _, report := range reports {
		if report.Group != ReviewGroup {
			continue
		}
		findings, err := h.store.ListSecurityFindings(r.Context(), report.ID, true)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the findings", "")
			return
		}
		kept, err := h.store.ResultAttachments(r.Context(), report.ID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the attachments", "")
			return
		}
		for _, finding := range findings {
			response.Findings = append(response.Findings, findingPayload{
				Fingerprint: finding.Fingerprint, Severity: finding.Severity, Title: finding.Title,
				Tool: finding.Tool, Category: finding.Category, RuleID: finding.RuleID,
				Location: finding.Location, Description: finding.Description,
				Suppressed: finding.Suppressed, SuppressionReason: finding.SuppressionReason,
			})
		}
		for _, attachment := range kept {
			response.Attachments = append(response.Attachments, attachmentPayloadOf(attachment))
		}
		reviewedAt := report.CreatedAt
		response.Reviewed, response.Status, response.Summary = true, report.Status, report.Summary
		response.Tool, response.Producer, response.RunName = report.Tool, report.Producer, report.RunName
		response.ReviewedAt, response.Attributes = &reviewedAt, report.Attributes
		// The most recent review is the one that speaks of the head.
		break
	}
	writeJSON(w, http.StatusOK, response)
}
