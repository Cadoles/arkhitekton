package gitea

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
 * Merged pull requests, for the release note.
 *
 * Only the merged ones count: a request that is open or refused delivered nothing, and putting it
 * in a note would announce to the client a fix that is not in the image.
 */

// maxPulls bounds how many pull requests are examined.
//
// A release note covers what was merged since the previous one — a few dozen requests
// per cycle. Two hundred leaves comfortable room while bounding the cost: each
// request kept costs a second query for its commits, and walking the repository's
// whole history would cost thousands for requests delivered two years ago.
const maxPulls = 200

// ListMergedPullRequests returns merged requests, newest first.
//
// `since` bounds the read: requests merged before it are ignored. Passing the zero
// value reads up to `maxPulls`. The bound is applied here rather than by the API — the
// forge cannot filter requests on their merge date — hence a read that stops as soon
// as it has passed the date.
func (c *Client) ListMergedPullRequests(ctx context.Context, since time.Time) ([]provider.PullRequest, error) {
	var pulls []provider.PullRequest

	for page := 1; len(pulls) < maxPulls; page++ {
		query := url.Values{}
		query.Set("state", "closed")
		// The forge sorts by descending update date: recent merges come first, which allows
		// stopping early.
		query.Set("sort", "recentupdate")
		query.Set("limit", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))

		body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/pulls", query)
		if err != nil {
			return nil, err
		}

		var decoded []struct {
			Number   int64      `json:"number"`
			Title    string     `json:"title"`
			Body     string     `json:"body"`
			Merged   bool       `json:"merged"`
			MergedAt *time.Time `json:"merged_at"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("gitea: pull requests response unreadable: %w", err)
		}
		if len(decoded) == 0 {
			break
		}

		for _, item := range decoded {
			// Closed without merging: the request delivered nothing.
			if !item.Merged {
				continue
			}
			if !since.IsZero() && item.MergedAt != nil && !item.MergedAt.After(since) {
				continue
			}
			pulls = append(pulls, provider.PullRequest{
				Number: item.Number, Title: item.Title, Body: item.Body,
				MergedAt: item.MergedAt,
			})
			if len(pulls) >= maxPulls {
				break
			}
		}

		if len(decoded) < pageSize {
			break
		}
	}
	return pulls, nil
}

// LoadCommitMessages completes a request with its commit messages.
//
// Separate from the listing because the forge does not return them with it, and
// because the convention allows the issue number in the title or in a commit: going
// without would miss requests whose title cites nothing.
//
// A failure is returned as is rather than ignored: a request whose commits could not
// be read may hide an issue, and silence would make an incomplete note pass for an
// exhaustive one.
func (c *Client) LoadCommitMessages(ctx context.Context, pull *provider.PullRequest) error {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(pageSize))

	body, err := c.get(ctx,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/commits", c.owner, c.repo, pull.Number), query)
	if err != nil {
		return err
	}

	var decoded []struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("gitea: commits of pull request %d unreadable: %w", pull.Number, err)
	}

	pull.Commits = make([]string, 0, len(decoded))
	for _, item := range decoded {
		pull.Commits = append(pull.Commits, item.Commit.Message)
	}
	return nil
}
