package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/gitea"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The pull-request probe.
 *
 * The one that motivated the request: a Gitea plugin could declare an event when it detects a
 * new PR.
 *
 * It persists nothing and deduplicates nothing — the loop takes care of that (see `probes.go`).
 * What it must hold fits in two points:
 *
 *   - **the deduplication key**, derived from the request's number in its repository. It alone
 *     knows what identifies its facts, and the loop re-reads an overlap window at each pass;
 *   - **the fact's date**, that of the request's creation, never that of the read. A probe
 *     catching up three days must produce facts dated across those three days.
 */

// giteaPullProbe observes a repository's open pull requests.
type giteaPullProbe struct {
	timeout time.Duration
}

// NewGiteaPullProbe builds the pull-request probe.
func NewGiteaPullProbe(timeout time.Duration) Probe {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return giteaPullProbe{timeout: timeout}
}

func (p giteaPullProbe) PluginID() string { return "gitea" }

// Schedule: the deployment's cadence, and no pass straight away.
//
// This probe fills no screen — it observes. A restart loop must not turn into a burst of
// calls towards a third-party forge.
func (p giteaPullProbe) Schedule() Schedule { return Schedule{} }

// Source names the cursor in `sync_state`.
//
// Specific to the probe and not to the plugin: Gitea could carry others — tags set, requests
// closed — and a shared cursor would have one advance the other's.
func (p giteaPullProbe) Source() string { return "gitea.pull_requests" }

// pullRequestOpened is the type of fact emitted, prefixed as the rule requires.
const pullRequestOpened = "gitea.pull_request.opened"

// Poll returns the requests opened since the cursor.
func (p giteaPullProbe) Poll(
	ctx context.Context, project store.Project, config map[string]string, cursor string,
) ([]event.Event, string, error) {
	url := strings.TrimSpace(config["url"])
	token := strings.TrimSpace(config["token"])
	if url == "" || token == "" {
		// Named rather than left to the client: "invalid URL" would send someone hunting for a typo
		// where it is a setting that was never filled in.
		return nil, "", fmt.Errorf(
			"forge not configured: set the URL and the token of plugin %q", p.PluginID())
	}
	if strings.TrimSpace(project.Repository) == "" {
		return nil, "", fmt.Errorf("the project declares no repository")
	}

	client, err := gitea.New(gitea.Options{
		URL: url, Token: token, Repository: project.Repository, Timeout: p.timeout,
	})
	if err != nil {
		return nil, "", err
	}

	since := parseCursor(cursor)
	pulls, err := client.ListOpenPullRequests(ctx, since)
	if err != nil {
		return nil, "", err
	}

	facts := make([]event.Event, 0, len(pulls))
	newest := since
	for _, pull := range pulls {
		facts = append(facts, event.Event{
			Type:    pullRequestOpened,
			Subject: "#" + strconv.FormatInt(pull.Number, 10),
			Payload: map[string]string{
				"number":        strconv.FormatInt(pull.Number, 10),
				"title":         pull.Title,
				"author":        pull.Author,
				"source_branch": pull.SourceBranch,
				"target_branch": pull.TargetBranch,
				"url":           pull.URL,
			},
			// The **fact's** date, that of the request's opening.
			OccurredAt: pull.CreatedAt,
			// The key carries the repository and the number: a number alone would repeat from one
			// repository to the next, and two tracked projects would steal each other's facts.
			DedupKey: pullDedupKey(project.Repository, pull.Number),
		})
		if pull.CreatedAt.After(newest) {
			newest = pull.CreatedAt
		}
	}

	return facts, formatCursor(newest, since), nil
}

// pullDedupKey identifies a pull request.
//
// The repository and the number, and nothing else: an opened request is a single fact, and
// reading it ten times must not produce ten.
func pullDedupKey(repository string, number int64) string {
	return pullRequestOpened + ":" + repository + ":" + strconv.FormatInt(number, 10)
}

// parseCursor reads the cursor, or returns zero.
//
// An unreadable cursor is treated as an absent one: the same rule as for the tracker
// synchronisation, and it leads to a bootstrap pass — which emits nothing — rather than to a
// resume from a wrong instant.
func parseCursor(cursor string) time.Time {
	if strings.TrimSpace(cursor) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// formatCursor composes the next cursor.
//
// It **never moves backwards**: a pass that sees nothing new returns the previous cursor,
// rather than a zero that would restart a bootstrap and lose the whole deduplication history.
func formatCursor(newest, previous time.Time) string {
	if newest.IsZero() {
		newest = previous
	}
	if newest.IsZero() {
		// No request, ever. The cursor carries the instant of the read so that the next pass stops
		// being a bootstrap: without that, a repository with no pull request would stay in bootstrap
		// forever, and the first request opened would emit nothing.
		newest = time.Now().UTC()
	}
	return newest.UTC().Format(time.RFC3339)
}
