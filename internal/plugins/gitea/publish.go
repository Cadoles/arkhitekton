package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CreateRelease publishes a release on a tag already set. A release already published on that tag
// is left as it is: the plan that publishes it may run again after a rebuild.
func (w *Writer) CreateRelease(ctx context.Context, tag, name, body string, prerelease bool) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("gitea: a tag is required to publish a release")
	}
	if strings.TrimSpace(name) == "" {
		name = tag
	}
	geste := "publishing release " + tag
	status, _, err := w.call(ctx, http.MethodPost, "/api/v1/repos/"+w.owner+"/"+w.repo+"/releases",
		map[string]any{"tag_name": tag, "name": name, "body": body, "prerelease": prerelease})
	if err != nil {
		return fmt.Errorf("gitea: %s: %w", geste, err)
	}
	if status == http.StatusConflict {
		return nil
	}
	return judge(status, geste)
}

// markerOf is the hidden line that recognises a comment the dashboard keeps up to date.
func markerOf(marker string) string { return "<!-- arkhitekton:" + marker + " -->" }

// UpsertComment posts a comment on a pull request, or edits the one this account posted with the
// same marker. The marker is a hidden line: the thread shows one summary, kept up to date.
func (w *Writer) UpsertComment(ctx context.Context, number int, marker, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("gitea: an empty comment is not posted")
	}
	tagged := strings.TrimRight(body, "\n") + "\n\n" + markerOf(marker)
	existing, err := w.ownComment(ctx, number, markerOf(marker))
	if err != nil {
		return err
	}
	if existing == 0 {
		return w.CommentOnPullRequest(ctx, number, tagged)
	}
	geste := fmt.Sprintf("updating the comment on pull request #%d", number)
	status, _, err := w.call(ctx, http.MethodPatch,
		fmt.Sprintf("/api/v1/repos/%s/%s/issues/comments/%d", w.owner, w.repo, existing),
		map[string]string{"body": tagged})
	if err != nil {
		return fmt.Errorf("gitea: %s: %w", geste, err)
	}
	return judge(status, geste)
}

// ownComment returns the comment this account posted with the marker, 0 when there is none.
func (w *Writer) ownComment(ctx context.Context, number int, marker string) (int64, error) {
	status, body, err := w.call(ctx, http.MethodGet, "/api/v1/user", nil)
	if err != nil {
		return 0, fmt.Errorf("gitea: reading the token's account: %w", err)
	}
	if err := judge(status, "reading the token's account"); err != nil {
		return 0, err
	}
	var account struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &account); err != nil || account.Login == "" {
		return 0, fmt.Errorf("gitea: the token's account is unreadable")
	}

	var previousFirst int64
	for page := 1; page <= 20; page++ {
		route := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments?limit=50&page=%d", w.owner, w.repo, number, page)
		status, body, err := w.call(ctx, http.MethodGet, route, nil)
		if err != nil {
			return 0, fmt.Errorf("gitea: reading the comments of pull request #%d: %w", number, err)
		}
		if err := judge(status, fmt.Sprintf("reading the comments of pull request #%d", number)); err != nil {
			return 0, err
		}
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(body, &comments); err != nil {
			return 0, fmt.Errorf("gitea: the comments of pull request #%d are unreadable", number)
		}
		// A forge that ignores the paging returns the same comments again: they were all read.
		if len(comments) == 0 || comments[0].ID == previousFirst {
			return 0, nil
		}
		previousFirst = comments[0].ID
		for _, comment := range comments {
			if comment.User.Login == account.Login && strings.Contains(comment.Body, marker) {
				return comment.ID, nil
			}
		}
		if len(comments) < 50 {
			return 0, nil
		}
	}
	return 0, nil
}
