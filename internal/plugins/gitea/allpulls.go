package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Every pull request, for following them: open, closed and merged, with the commit at their head.
 *
 * The forge sorts on the update date, newest first, which is exactly what following needs: a
 * request opened, pushed to, closed or merged is updated, and the read stops at the first one the
 * previous pass already saw.
 */

var _ provider.PullRequestLister = (*Client)(nil)

// maxFollowedPulls bounds one read. The first pass of a repository stops there, the following
// passes read only what changed since the previous one, a handful.
const maxFollowedPulls = 200

// ListPullRequests returns the requests updated after `since`, the most recently updated first,
// and says whether the bound was reached before `since`.
func (c *Client) ListPullRequests(ctx context.Context, since time.Time) ([]provider.PullRequest, bool, error) {
	var pulls []provider.PullRequest

	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("state", "all")
		query.Set("sort", "recentupdate")
		query.Set("limit", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))

		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/pulls", query)
		if err != nil {
			return nil, false, err
		}

		var batch []struct {
			Number  int64  `json:"number"`
			Title   string `json:"title"`
			Body    string `json:"body"`
			Draft   bool   `json:"draft"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Merged    bool       `json:"merged"`
			MergedAt  *time.Time `json:"merged_at"`
			ClosedAt  *time.Time `json:"closed_at"`
			CreatedAt time.Time  `json:"created_at"`
			UpdatedAt time.Time  `json:"updated_at"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, false, fmt.Errorf("gitea: pull requests unreadable: %w", err)
		}

		for _, entry := range batch {
			if !since.IsZero() && !entry.UpdatedAt.After(since) {
				return pulls, false, nil
			}
			if len(pulls) >= maxFollowedPulls {
				return pulls, true, nil
			}
			state := provider.PullOpen
			switch {
			case entry.Merged:
				state = provider.PullMerged
			case entry.State == "closed":
				state = provider.PullClosed
			}
			pulls = append(pulls, provider.PullRequest{
				Number: entry.Number, Title: entry.Title, Body: entry.Body, Draft: entry.Draft,
				State: state, Author: entry.User.Login,
				SourceBranch: entry.Head.Ref, TargetBranch: entry.Base.Ref, HeadSHA: entry.Head.SHA,
				URL: entry.HTMLURL, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
				ClosedAt: entry.ClosedAt, MergedAt: entry.MergedAt,
			})
		}

		if len(batch) < pageSize {
			return pulls, false, nil
		}
	}
}
