package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

/*
 * What the forge pushes.
 *
 * Two deliveries are read, and they produce the facts the plugin already knows how to name:
 *
 *   - a **pull request opened** gives the same fact as the probe, under the same key: received and
 *     polled, it is written once, whichever arrives first;
 *   - a **push** gives a fact the probe cannot see, since a forge keeps no list of pushes to read
 *     back. It carries the pushed commit as `revision`, which a pipeline step takes as its revision
 *     without any declaration.
 *
 * Everything else the hook is set for is acknowledged and ignored. The dashboard has checked the
 * signature before this file reads a byte.
 */

const (
	// push is the suffix of the fact a push delivery produces.
	push = "push"
	// webhookSecret is the setting holding the hook's shared secret.
	webhookSecret = "webhook_secret"
	// zeroCommit is the `after` of a push that deleted its branch or tag.
	zeroCommit = "0000000000000000000000000000000000000000"
)

// receiver reads the deliveries of a Gitea webhook.
type receiver struct {
	now func() time.Time
}

func (r receiver) Receive(
	_ context.Context, target capability.Target, delivery capability.Delivery,
) ([]capability.Observation, error) {
	switch delivery.Event {
	case "push", "pull_request":
	default:
		return nil, nil
	}
	if !bytes.HasPrefix(bytes.TrimSpace(delivery.Body), []byte("{")) {
		return nil, fmt.Errorf("gitea: the hook must send application/json")
	}
	if delivery.Event == "push" {
		return r.push(target, delivery.Body)
	}
	return r.pullRequest(target, delivery.Body)
}

type repositoryVw struct {
	FullName string `json:"full_name"`
}

// sameRepository refuses a delivery about another repository than the project's. A secret set for
// the instance signs every repository's hook alike: the signature does not say which project a
// delivery is for, the repository does, and a hook pointed at another project's address must not
// build that project.
func sameRepository(target capability.Target, delivered repositoryVw) error {
	if !strings.EqualFold(delivered.FullName, target.Repository) {
		return fmt.Errorf("gitea: the delivery is about %q, project %q builds %q",
			delivered.FullName, target.Project, target.Repository)
	}
	return nil
}

func (r receiver) push(target capability.Target, body []byte) ([]capability.Observation, error) {
	var delivered struct {
		Ref        string       `json:"ref"`
		Before     string       `json:"before"`
		After      string       `json:"after"`
		CompareURL string       `json:"compare_url"`
		Total      int          `json:"total_commits"`
		Repository repositoryVw `json:"repository"`
		Pusher     struct {
			Login string `json:"login"`
		} `json:"pusher"`
		HeadCommit *struct {
			Message string `json:"message"`
		} `json:"head_commit"`
	}
	if err := json.Unmarshal(body, &delivered); err != nil {
		return nil, fmt.Errorf("gitea: push delivery unreadable: %w", err)
	}
	if err := sameRepository(target, delivered.Repository); err != nil {
		return nil, err
	}
	// A deleted branch or tag pushed nothing anybody could build.
	if delivered.After == "" || delivered.After == zeroCommit {
		return nil, nil
	}

	payload := map[string]string{
		"revision": delivered.After, "ref": delivered.Ref, "before": delivered.Before,
		"pusher": delivered.Pusher.Login, "commits": strconv.Itoa(delivered.Total),
		"compare_url": delivered.CompareURL,
	}
	subject := delivered.Ref
	// Absent rather than empty: a reaction matching `branch` must not leave on a tag.
	if branch, found := strings.CutPrefix(delivered.Ref, "refs/heads/"); found {
		payload["branch"], subject = branch, branch
	} else if tag, found := strings.CutPrefix(delivered.Ref, "refs/tags/"); found {
		payload["tag"], subject = tag, tag
	}
	if delivered.HeadCommit != nil {
		payload["message"], _, _ = strings.Cut(delivered.HeadCommit.Message, "\n")
	}

	return []capability.Observation{{
		Suffix: push, Subject: subject, Payload: payload,
		// The forge does not date a push; it delivers it as it happens, so the reception is the
		// push's date. The commit's date would not do: pushing an old commit is a new fact.
		OccurredAt: r.now(),
		Key:        target.Repository + ":" + delivered.Ref + ":" + delivered.After,
	}}, nil
}

func (r receiver) pullRequest(target capability.Target, body []byte) ([]capability.Observation, error) {
	var delivered struct {
		Action      string       `json:"action"`
		Repository  repositoryVw `json:"repository"`
		PullRequest struct {
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
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &delivered); err != nil {
		return nil, fmt.Errorf("gitea: pull request delivery unreadable: %w", err)
	}
	if err := sameRepository(target, delivered.Repository); err != nil {
		return nil, err
	}
	// Only the opening is a fact the plugin names. A reopened request was already opened once.
	if delivered.Action != "opened" {
		return nil, nil
	}
	pull := delivered.PullRequest
	return []capability.Observation{opened(target, OpenPullRequest{
		Number: pull.Number, Title: pull.Title, Author: pull.User.Login,
		SourceBranch: pull.Head.Ref, TargetBranch: pull.Base.Ref,
		URL: pull.HTMLURL, CreatedAt: pull.CreatedAt,
	})}, nil
}
