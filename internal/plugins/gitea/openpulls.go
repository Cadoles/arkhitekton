package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

/*
 * The **open** pull requests, for the probe.
 *
 * Distinct from `ListMergedPullRequests`, which serves the release note and looks only at what
 * was merged. Here the opposite is sought: what has just opened, so that a reaction can hook onto
 * it.
 *
 * The date kept is that of **creation**, never that of update: a request opened a month ago and
 * commented on this morning is not a new fact, and treating it as one would re-notify at every
 * comment.
 */

// OpenPullRequest is an open pull request, reduced to what a fact carries.
type OpenPullRequest struct {
	Number       int64
	Title        string
	Author       string
	SourceBranch string
	TargetBranch string
	URL          string
	CreatedAt    time.Time
}

// maxOpenPulls bounds the read.
//
// An active repository carries a few dozen open requests. Two hundred leaves room and
// bounds the cost: the probe runs at every interval, for every project, and an
// unbounded read would pay for the whole history on each pass.
const maxOpenPulls = 200

// ListOpenPullRequests returns the open requests created after `since`.
//
// A nil `since` reads up to the bound: that is the priming pass, whose loop emits
// nothing — it only establishes the cursor.
//
// The forge sorts on the update date, not the creation date: one cannot therefore
// stop at the first item that is too old, and the page bound is what holds the cost.
func (c *Client) ListOpenPullRequests(ctx context.Context, since time.Time) ([]OpenPullRequest, error) {
	var pulls []OpenPullRequest

	for page := 1; len(pulls) < maxOpenPulls; page++ {
		query := url.Values{}
		query.Set("state", "open")
		query.Set("sort", "recentupdate")
		query.Set("page", strconv.Itoa(page))
		query.Set("limit", strconv.Itoa(pageSize))

		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/pulls", query)
		if err != nil {
			return nil, err
		}

		var batch []struct {
			Number  int64  `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("gitea: pull requests unreadable: %w", err)
		}

		for _, entry := range batch {
			// The creation date decides, never the update date: a request opened a month ago and
			// commented this morning is not a new fact.
			if !since.IsZero() && !entry.CreatedAt.After(since) {
				continue
			}
			pulls = append(pulls, OpenPullRequest{
				Number: entry.Number, Title: entry.Title, Author: entry.User.Login,
				SourceBranch: entry.Head.Ref, TargetBranch: entry.Base.Ref,
				URL: entry.HTMLURL, CreatedAt: entry.CreatedAt,
			})
		}

		if len(batch) < pageSize {
			break
		}
	}

	return pulls, nil
}
