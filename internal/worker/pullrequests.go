package worker

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Following a project's pull requests.
 *
 * A probe of the core, one per forge plugin, run by the common loop like the tracker's: it
 * resolves the plugin's settings for the project, unseals them, keeps the cursor and isolates the
 * failures. Each pass reads the requests updated since the previous one, records them, and turns
 * what changed into the core's facts: opened, pushed to, closed, merged, reopened.
 *
 * The first pass of a project imports without a fact: following a repository does not announce
 * its history. A forge that cannot list its requests is not followed, and nothing fails.
 */

// pullRequestInterval spaces two passes. A webhook delivery asks for an earlier one; this is what
// catches up a delivery that was lost, or that could not reach the dashboard at all.
const pullRequestInterval = time.Minute

// PullRequestStore is what following the pull requests needs from the database.
type PullRequestStore interface {
	UpsertPullRequest(ctx context.Context, pull store.PullRequest) (store.PullRequest, *store.PullRequest, error)
}

type pullRequestProbe struct {
	plugin   string
	forge    capability.ForgeCapability
	store    PullRequestStore
	timeout  time.Duration
	registry string
	logger   *slog.Logger
	now      func() time.Time
}

// PullRequestProbes returns a probe for every plugin bringing a forge. `registry` is the registry
// project a pull request's images are named in, as a release's are.
func PullRequestProbes(
	registry *capabilities.Registry, db PullRequestStore, timeout time.Duration, registryProject string, logger *slog.Logger,
) []Probe {
	if logger == nil {
		logger = slog.Default()
	}
	var probes []Probe
	for _, plugin := range registry.Plugins() {
		if plugin.Forge == nil {
			continue
		}
		probes = append(probes, pullRequestProbe{
			plugin: plugin.ID, forge: plugin.Forge, store: db, timeout: timeout,
			registry: registryProject, logger: logger, now: time.Now,
		})
	}
	return probes
}

func (p pullRequestProbe) PluginID() string { return p.plugin }

func (p pullRequestProbe) Source() string { return store.PullRequestSyncSource(p.plugin) }

func (p pullRequestProbe) Schedule() Schedule {
	return Schedule{Interval: pullRequestInterval, Eager: true}
}

func (p pullRequestProbe) Poll(
	ctx context.Context, project store.Project, config map[string]string, cursor string,
) ([]event.Event, string, error) {
	client, err := p.forge.Connect(capability.Target{
		Project: project.Slug, Repository: project.Repository, Settings: capability.Settings(config), Timeout: p.timeout,
	})
	if err != nil {
		return nil, "", err
	}
	lister, ok := client.(provider.PullRequestLister)
	if !ok {
		return nil, cursor, nil
	}

	startedAt := p.now().UTC()
	pulls, truncated, err := lister.ListPullRequests(ctx, cursorSince(cursor))
	if errors.Is(err, errors.ErrUnsupported) {
		return nil, cursor, nil
	}
	if err != nil {
		return nil, "", err
	}
	if truncated && cursor != "" {
		p.logger.Warn("more pull requests changed than one pass reads: the oldest changes are caught up next pass",
			"plugin", p.plugin, "project", project.Slug)
	}

	priming := cursor == ""
	next := cursorSince(cursor)
	var facts []event.Event
	// The oldest change first, so that the facts are recorded in the order they happened.
	for _, pull := range slices.Backward(pulls) {
		recorded, previous, err := p.store.UpsertPullRequest(ctx, recordOf(project.ID, pull))
		if err != nil {
			return nil, "", err
		}
		if pull.UpdatedAt.After(next) {
			next = pull.UpdatedAt
		}
		if !priming {
			facts = append(facts, p.transitions(project, previous, recorded)...)
		}
	}

	if next.IsZero() {
		// An empty repository: the cursor still marks that it was read, so that its first request
		// is a fact.
		next = startedAt
	}
	return facts, next.Format(time.RFC3339), nil
}

func recordOf(projectID int64, pull provider.PullRequest) store.PullRequest {
	record := store.PullRequest{
		ProjectID: projectID, Number: pull.Number, Title: pull.Title, Author: pull.Author,
		SourceBranch: pull.SourceBranch, TargetBranch: pull.TargetBranch, HeadSHA: pull.HeadSHA,
		State: pull.State, URL: pull.URL, ClosedAt: pull.ClosedAt, MergedAt: pull.MergedAt,
		Draft: pull.Draft, Body: pull.Body,
	}
	if record.State == "" {
		record.State = provider.PullOpen
	}
	if !pull.CreatedAt.IsZero() {
		created := pull.CreatedAt
		record.CreatedAt = &created
	}
	if !pull.UpdatedAt.IsZero() {
		updated := pull.UpdatedAt
		record.UpdatedAt = &updated
	}
	return record
}

// transitions returns the facts of what changed between two readings of a request.
func (p pullRequestProbe) transitions(project store.Project, previous *store.PullRequest, current store.PullRequest) []event.Event {
	at := p.now().UTC()
	if current.UpdatedAt != nil {
		at = *current.UpdatedAt
	}
	fact := func(kind event.Type, occurrence string) event.Event {
		return event.PullRequestAt(kind, at, project.ID, current.Number, occurrence, PullRequestPayload(current, p.registry))
	}
	instant := strconv.FormatInt(at.Unix(), 10)

	switch {
	case previous == nil:
		// First seen after the import: opened since the previous pass, possibly already concluded.
		switch current.State {
		case provider.PullMerged:
			return []event.Event{fact(event.PullRequestMerged, "")}
		case provider.PullClosed:
			return []event.Event{fact(event.PullRequestClosed, instant)}
		}
		return []event.Event{fact(event.PullRequestOpened, "")}
	case previous.State != current.State:
		switch current.State {
		case provider.PullMerged:
			return []event.Event{fact(event.PullRequestMerged, "")}
		case provider.PullClosed:
			return []event.Event{fact(event.PullRequestClosed, instant)}
		}
		return []event.Event{fact(event.PullRequestReopened, instant)}
	case current.State == provider.PullOpen && previous.Draft && !current.Draft:
		// Out of draft: what was held back for it starts now, on the head as it stands.
		return []event.Event{fact(event.PullRequestReady, current.HeadSHA+"-"+instant)}
	case current.State == provider.PullOpen && previous.HeadSHA != current.HeadSHA && current.HeadSHA != "":
		return []event.Event{fact(event.PullRequestSynchronized, current.HeadSHA)}
	}
	return nil
}

// PullRequestPayload is what a request's facts carry. `revision`, `tag` and `registry` are the keys
// a plan's steps read a release's commit, tag and registry from: a plan opened by such a fact builds
// and tests the request's head the way a release's plan builds the release.
//
// `draft` says whether the request is still a draft, and each `Key: value` line of its body becomes
// `trailer.<key>`: a reaction matches `draft: "false"`, a step reads `{trailer.mse-kustom}`.
func PullRequestPayload(pull store.PullRequest, registry string) map[string]string {
	payload := map[string]string{
		"number": strconv.FormatInt(pull.Number, 10), "title": pull.Title, "author": pull.Author,
		"source_branch": pull.SourceBranch, "target_branch": pull.TargetBranch, "url": pull.URL,
		contextRevision: pull.HeadSHA, contextTag: pull.Tag(), contextRegistry: registry,
		ContextPullRequest: strconv.FormatInt(pull.ID, 10),
		"draft":            strconv.FormatBool(pull.Draft),
	}
	for key, value := range Trailers(pull.Body) {
		payload["trailer."+key] = value
	}
	return payload
}

var trailerLine = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_-]{0,63})\s*:\s*(\S.{0,255}?)\s*$`)

// Trailers reads the `Key: value` lines of a text, keys lowercased. The first line of a key wins.
func Trailers(text string) map[string]string {
	found := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		match := trailerLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if match == nil {
			continue
		}
		key := strings.ToLower(match[1])
		if _, seen := found[key]; !seen {
			found[key] = match[2]
		}
	}
	return found
}

// ContextPullRequest is the key a pull request's facts name their request under.
const ContextPullRequest = "pull_request_id"
