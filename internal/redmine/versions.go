package redmine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
)

/*
 * Reading the versions and the requests.
 *
 * The synchronisation is a periodic incremental query, and it is the definitive path and not a
 * fallback: no webhook plugin can be deployed on the instance.
 */

// redminePageSize is the page size asked of Redmine.
//
// A hundred is the API's ceiling: asking for more is silently brought back to twenty-five, which
// would multiply the number of round trips by four with no message at all.
const redminePageSize = 100

// ListVersions returns a project's versions.
//
// Not paginated on Redmine's side, which returns all of a project's versions in one response. The
// body's reading bound protects against the case where that stops being true.
func (c *Client) ListVersions(ctx context.Context, project string) ([]provider.Version, error) {
	if project == "" {
		return nil, fmt.Errorf("redmine: project not set")
	}

	body, err := c.get(ctx, "/projects/"+url.PathEscape(project)+"/versions.json", nil)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Versions []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      string `json:"status"`
			DueDate     string `json:"due_date"`
			UpdatedOn   string `json:"updated_on"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("redmine: list of versions unreadable: %w", err)
	}

	versions := make([]provider.Version, 0, len(payload.Versions))
	for _, raw := range payload.Versions {
		versions = append(versions, provider.Version{
			ID: strconv.FormatInt(raw.ID, 10), Name: raw.Name, Description: raw.Description,
			Status:    raw.Status,
			DueDate:   parseDate(raw.DueDate),
			UpdatedOn: parseTime(raw.UpdatedOn),
		})
	}
	return versions, nil
}

// ListIssues returns the requests matching the query, all pages together.
//
// `status_id=*` is essential: without it, Redmine returns only the open requests, and a finished
// version would look empty. It is the kind of silent defect that reads as data loss.
//
// The sort is forced on `updated_on` ascending. With no stable order, a request modified during
// the pagination can change page and escape the read — and it is precisely the request that has
// just moved that is being looked for.
func (c *Client) ListIssues(ctx context.Context, query provider.IssueQuery) ([]provider.Issue, error) {
	if query.Project == "" {
		return nil, fmt.Errorf("redmine: project not set")
	}

	var issues []provider.Issue
	for offset := 0; ; {
		params := url.Values{}
		params.Set("project_id", query.Project)
		params.Set("status_id", "*")
		params.Set("sort", "updated_on")
		params.Set("limit", strconv.Itoa(redminePageSize))
		params.Set("offset", strconv.Itoa(offset))
		if !query.Since.IsZero() {
			// Redmine expects an operator in the value; `url.Values` encodes the `>=` correctly.
			params.Set("updated_on", ">="+query.Since.UTC().Format(time.RFC3339))
		}

		body, err := c.get(ctx, "/issues.json", params)
		if err != nil {
			return nil, err
		}

		page, total, err := decodeIssues(body)
		if err != nil {
			return nil, err
		}
		issues = append(issues, page...)

		// Stop on an empty page as much as on the total being reached: an empty page while the total
		// announces more would signal an inconsistency on Redmine's side, and looping on it would run
		// forever.
		offset += len(page)
		if len(page) == 0 || offset >= total {
			return issues, nil
		}
	}
}

func decodeIssues(body []byte) ([]provider.Issue, int, error) {
	var payload struct {
		TotalCount int `json:"total_count"`
		Issues     []struct {
			ID      int64  `json:"id"`
			Subject string `json:"subject"`
			Tracker struct {
				Name string `json:"name"`
			} `json:"tracker"`
			Status struct {
				Name     string `json:"name"`
				IsClosed bool   `json:"is_closed"`
			} `json:"status"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			AssignedTo struct {
				Name string `json:"name"`
			} `json:"assigned_to"`
			FixedVersion *struct {
				ID int64 `json:"id"`
			} `json:"fixed_version"`
			DoneRatio int    `json:"done_ratio"`
			UpdatedOn string `json:"updated_on"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, fmt.Errorf("redmine: list of requests unreadable: %w", err)
	}

	issues := make([]provider.Issue, 0, len(payload.Issues))
	for _, raw := range payload.Issues {
		issue := provider.Issue{
			// Redmine numbers; the boundary speaks in text, so that a tool writing `MSE-1234` can cross it
			// too.
			ID: strconv.FormatInt(raw.ID, 10), Subject: raw.Subject,
			Tracker:      raw.Tracker.Name,
			Status:       raw.Status.Name,
			StatusClosed: raw.Status.IsClosed,
			Priority:     raw.Priority.Name,
			Assignee:     raw.AssignedTo.Name,
			DoneRatio:    raw.DoneRatio,
			UpdatedOn:    parseTime(raw.UpdatedOn),
		}
		// A request with no version is the common case: attaching to a version is an act of planning that
		// has not always happened.
		if raw.FixedVersion != nil {
			id := strconv.FormatInt(raw.FixedVersion.ID, 10)
			issue.VersionID = &id
		}
		issues = append(issues, issue)
	}
	return issues, payload.TotalCount, nil
}

func parseTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// An unreadable timestamp must not fail the whole synchronisation: its absence translates into a
		// complete resynchronisation at the next pass, which is expensive but correct.
		return nil
	}
	return &parsed
}

func parseDate(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil
	}
	return &parsed
}
