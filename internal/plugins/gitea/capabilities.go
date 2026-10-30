package gitea

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// options reads the connection from the plugin's settings and the project's repository.
//
// A missing setting is named rather than left to the client: "invalid URL" would send someone
// hunting for a typo where it is a setting that was never filled in.
func options(target capability.Target) (Options, error) {
	url := strings.TrimSpace(target.Settings["url"])
	token := strings.TrimSpace(target.Settings["token"])
	if url == "" || token == "" {
		return Options{}, fmt.Errorf("%w: set the URL and the token of plugin %q", ErrNotConfigured, ID)
	}
	if strings.TrimSpace(target.Repository) == "" {
		return Options{}, fmt.Errorf("%w: project %q declares no repository", ErrNotConfigured, target.Project)
	}
	return Options{URL: url, Token: token, Repository: target.Repository, Timeout: target.Timeout}, nil
}

// forge connects the dashboard to a Gitea repository.
type forge struct{}

func (forge) Connect(target capability.Target) (provider.Forge, error) {
	opts, err := options(target)
	if err != nil {
		return nil, err
	}
	return New(opts)
}

func (forge) Writer(target capability.Target) (provider.TagWriter, error) {
	opts, err := options(target)
	if err != nil {
		return nil, err
	}
	return NewWriter(opts)
}

func (forge) Prober(target capability.Target) (provider.RepositoryProber, error) {
	url := strings.TrimSpace(target.Settings["url"])
	token := strings.TrimSpace(target.Settings["token"])
	if url == "" || token == "" {
		return nil, fmt.Errorf("%w: set the URL and the token of plugin %q", ErrNotConfigured, ID)
	}
	return Prober{URL: url, Token: token, Timeout: target.Timeout}, nil
}

/*
 * The pull-request probe.
 *
 * It persists nothing and deduplicates nothing: the dashboard does. What it must hold fits in two
 * points:
 *
 *   - **the key**, derived from the request's number in its repository: a number alone would repeat
 *     from one repository to the next, and two projects would steal each other's facts;
 *   - **the fact's date**, that of the request's creation, never that of the read. A probe catching
 *     up three days must produce facts dated across those three days.
 */

// pullRequestOpened is the suffix of the fact the probe produces.
const pullRequestOpened = "pull_request.opened"

// pullProbe observes a repository's open pull requests.
type pullProbe struct{}

func (pullProbe) Name() string { return "pull_requests" }

// Schedule takes the deployment's cadence: a pull request is not urgent to the minute.
func (pullProbe) Schedule() capability.Schedule { return capability.Schedule{} }

// Poll returns the requests opened since the cursor.
func (pullProbe) Poll(
	ctx context.Context, target capability.Target, cursor string,
) ([]capability.Observation, string, error) {
	opts, err := options(target)
	if err != nil {
		return nil, "", err
	}
	client, err := New(opts)
	if err != nil {
		return nil, "", err
	}

	since := parseCursor(cursor)
	pulls, err := client.ListOpenPullRequests(ctx, since)
	if err != nil {
		return nil, "", err
	}

	observed := make([]capability.Observation, 0, len(pulls))
	newest := since
	for _, pull := range pulls {
		observed = append(observed, opened(target, pull))
		if pull.CreatedAt.After(newest) {
			newest = pull.CreatedAt
		}
	}
	return observed, formatCursor(newest, since), nil
}

// opened is the fact of a pull request opening, polled or received.
func opened(target capability.Target, pull OpenPullRequest) capability.Observation {
	number := strconv.FormatInt(pull.Number, 10)
	return capability.Observation{
		Suffix:  pullRequestOpened,
		Subject: "#" + number,
		Payload: map[string]string{
			"number":        number,
			"title":         pull.Title,
			"author":        pull.Author,
			"source_branch": pull.SourceBranch,
			"target_branch": pull.TargetBranch,
			"url":           pull.URL,
		},
		OccurredAt: pull.CreatedAt,
		Key:        target.Repository + ":" + number,
	}
}

// parseCursor reads the cursor, or returns zero.
//
// An unreadable cursor is treated as an absent one: it leads to a bootstrap pass, which emits
// nothing, rather than to a resume from a wrong instant.
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
// It **never moves backwards**: a pass that sees nothing new returns the previous cursor, rather
// than a zero that would restart a bootstrap and lose the whole deduplication history.
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

// executor carries out the forge's actions: a commit status, a comment on a pull request.
type executor struct{}

func (executor) Execute(
	ctx context.Context, target capability.Target, suffix string, params map[string]string,
) error {
	opts, err := options(target)
	if err != nil {
		return err
	}
	writer, err := NewWriter(opts)
	if err != nil {
		return err
	}

	switch suffix {
	case "status":
		commit := params["commit"]
		if commit == "" {
			return fmt.Errorf("the commit targeted is empty after interpolation: " +
				"the triggering fact does not carry the named variable")
		}
		return writer.SetCommitStatus(ctx, commit, params["state"],
			params["context"], params["description"], params["target_url"])

	case "comment":
		raw := strings.TrimSpace(params["number"])
		number, err := strconv.Atoi(raw)
		if err != nil {
			// The common case, and it is worth saying this way: a reaction wired to `release.built`
			// has no `{number}`, the template stays literal, and "invalid" without quoting the value
			// would send someone hunting elsewhere.
			return fmt.Errorf("the pull request number is %q after "+
				"interpolation: does this fact really carry the named variable?", raw)
		}
		return writer.CommentOnPullRequest(ctx, number, params["body"])

	default:
		return fmt.Errorf("action %q unknown to plugin %q", suffix, ID)
	}
}
