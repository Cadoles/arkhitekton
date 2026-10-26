package worker

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Redmine synchronisation.
 *
 * Periodic incremental querying. It is the definitive path and not a fallback: no webhook
 * plugin can be deployed on the instance, so nothing can warn us of a change.
 *
 * # What stays here, and what left
 *
 * The **projection** stays: reading the versions, depositing them, adopting the provisional
 * ones, projecting the requests onto their buckets. It is this package's own work, and it has
 * not moved.
 *
 * The **loop** left. It lives in `probes.go`, with the forge's: resolving the plugin's
 * settings, unsealing the key, reading and advancing the cursor, isolating a project's failure.
 * Both sources did exactly that, each in its own way — and the second version was a copy of the
 * first, written six months later without knowing it.
 */

// syncInterval bounds how often the tracker is queried.
//
// Five minutes: versions and requests move at the pace of human typing, and querying more often
// would load an instance that is not ours for identical data. Less often would make the
// versions screen visibly behind Redmine, where the user has just typed.
const syncInterval = 5 * time.Minute

// TrackerSource names the tracker plugin a synchronisation reads.
//
// The facts it observes are two, and no more. They have in common that they are **events** —
// something happened once. A request changing status is not one: it changes ten times, and one
// fact per change would turn the fact feed into a typing log.
//
// Only one of the two carries the plugin's prefix. The version is a **core** fact —
// `version.created` — because every tracker deposits its versions in the same table and a
// reaction declared on it must not be rewritten when the client changes tool. The request has no
// core equivalent: it stays the plugin's fact, under the type its declaration gives it.
type TrackerSource struct {
	// Tool is the plugin's identifier: the `tool` column of versions and issues, and the
	// synchronisation's cursor in `sync_state`. Redmine's is `redmine`, the name the cursors have
	// carried from the start.
	Tool string
	// ClosedType is the type of the closed-issue fact. Empty when the plugin declares none: the
	// issue is still projected, and nothing is observed.
	ClosedType string
}

// cursorOverlap is the margin taken off the cursor at each read.
//
// Redmine filters on `updated_on` with second precision, and its clock is not ours. Starting
// again from exactly the last instant read would miss any request modified in the same second
// as the end of the previous read. A minute of overlap makes a few requests be read again — the
// write being idempotent — rather than losing one for good.
const cursorOverlap = time.Minute

// TrackerReader is what the synchronisation needs from the client.
type TrackerReader interface {
	ListVersions(ctx context.Context, project string) ([]provider.Version, error)
	ListIssues(ctx context.Context, query provider.IssueQuery) ([]provider.Issue, error)
}

// TrackerConnector builds a client for one project's connection: the tracker plugin's settings,
// resolved and unsealed, with `project` naming the project **in the tracker**.
//
// A function rather than an already built client: the connection is not the same for every
// project, and a single client built at start-up could serve only one.
type TrackerConnector func(capability.Target) (TrackerReader, error)

// TrackerStore is what the projection needs from the database.
//
// **Four methods, against nine before the migration.** The five that are missing — the tracked
// projects, the plugin's settings, the cursor, its two outcomes — moved to the probe loop,
// which holds them for every source. It is the clearest measure of what the contract absorbed:
// what remains here is the work specific to the tracker, and nothing else.
type TrackerStore interface {
	UpsertRedmineVersion(ctx context.Context, version store.TrackedVersion) (int64, error)
	UpsertRedmineIssue(ctx context.Context, issue store.TrackedIssue) error
	AdoptPendingVersions(ctx context.Context) ([]store.Adoption, error)
	StatusMapping(ctx context.Context, projectID int64) (map[string]string, error)
}

// SecretOpener unseals the settings' sensitive fields.
//
// A narrow interface rather than the whole cipher: the synchronisation needs only to open,
// never to seal, and giving it the sealing would invite it to write secrets it has no reason to
// write.
type SecretOpener interface {
	Open(sealed string) (string, error)
	Configured() bool
}

// TrackerSync projects the versions and the requests into the database.
type TrackerSync struct {
	store  TrackerStore
	source TrackerSource
	logger *slog.Logger
	now    func() time.Time
}

func NewTrackerSync(db TrackerStore, source TrackerSource, logger *slog.Logger) *TrackerSync {
	if logger == nil {
		logger = slog.Default()
	}
	return &TrackerSync{store: db, source: source, logger: logger, now: time.Now}
}

// Observe runs a complete pass for one project, and returns what it observes.
//
// Called by the probe, which brings it the already connected client and the cursor. It decides
// neither its moment nor what becomes of its facts.
//
// The order is imposed: the versions first, the requests after. A request attaches to its
// version by its external identifier, and reading the requests first would leave unattached
// every request targeting a newly created version.
func (s *TrackerSync) Observe(
	ctx context.Context, projectID int64, trackerProject string,
	client TrackerReader, cursor string,
) ([]event.Event, string, error) {
	// The instant is captured **before** the read, and that is what makes the cursor safe: taking
	// it afterwards would make the reading time a blind spot, the requests modified during it being
	// neither in this pass nor in the next.
	startedAt := s.now().UTC()

	// The versions are re-read whole at each pass. The API does not filter them on `updated_on`,
	// and their number is of the order of twenty: a complete read costs one request.
	//
	// It is also what makes this probe a **snapshot** rather than a stream, and what revealed the
	// bootstrap defect fixed in `probes.go`.
	versions, err := client.ListVersions(ctx, trackerProject)
	if err != nil {
		return nil, "", err
	}

	facts := make([]event.Event, 0, len(versions))
	for _, version := range versions {
		versionID, err := s.store.UpsertRedmineVersion(ctx, store.TrackedVersion{
			ProjectID: projectID,
			// The tool and the identifier it uses, rather than an integer whose name already said which
			// product it came from.
			Tool:        s.source.Tool,
			ExternalID:  version.ID,
			Name:        version.Name,
			Normalized:  release.NormalizeVersionName(version.Name),
			Description: version.Description,
			Status:      version.Status,
			DueDate:     version.DueDate,
			UpdatedOn:   version.UpdatedOn,
		})
		if err != nil {
			return nil, "", err
		}
		facts = append(facts, versionCreatedFact(versionID, version, s.source.Tool, startedAt))
	}

	// The adoption comes after **all** the versions are deposited: that is what makes it
	// independent of the reading order when two names reduce to the same number.
	adoptions, err := s.store.AdoptPendingVersions(ctx)
	if err != nil {
		return nil, "", err
	}
	s.reportAdoptions(adoptions)

	mapping, err := s.store.StatusMapping(ctx, projectID)
	if err != nil {
		return nil, "", err
	}

	since := cursorSince(cursor)
	issues, err := client.ListIssues(ctx, provider.IssueQuery{Project: trackerProject, Since: since})
	if err != nil {
		return nil, "", err
	}

	unmapped := map[string]int{}
	for _, issue := range issues {
		bucket, known := mapping[issue.Status]
		if !known {
			// The default bucket, and a count for the log. The status will also appear in the
			// administration screen through `UnmappedStatuses`: the instance's statuses change without
			// warning, and an unmapped addition would make the completion rate drift with nothing to report
			// it.
			bucket = "todo"
			unmapped[issue.Status]++
		}

		if err := s.store.UpsertRedmineIssue(ctx, store.TrackedIssue{
			ProjectID:  projectID,
			Tool:       s.source.Tool,
			ExternalID: issue.ID,
			// The version targeted, named by its identifier **in the tool**: the dashboard does not yet
			// know its local key at the moment it projects the request.
			VersionExternalID: issue.VersionID,
			Tracker:           issue.Tracker,
			Subject:           issue.Subject,
			StatusName:        issue.Status,
			StatusBucket:      bucket,
			Priority:          issue.Priority,
			Assignee:          issue.Assignee,
			DoneRatio:         issue.DoneRatio,
			IsClosed:          issue.StatusClosed,
			UpdatedOn:         issue.UpdatedOn,
		}); err != nil {
			return nil, "", err
		}
		if issue.StatusClosed && s.source.ClosedType != "" {
			facts = append(facts, issueClosedFact(s.source.ClosedType, trackerProject, issue, startedAt))
		}
	}

	for status, count := range unmapped {
		s.logger.Warn("tracker status with no declared mapping", "tool", s.source.Tool,
			"project", trackerProject, "status", status,
			"issues", count, "bucket_par_defaut", "todo")
	}

	s.logger.Info("tracker synchronisation",
		"tool", s.source.Tool, "project", trackerProject,
		"versions", len(versions), "requests", len(issues),
		"incremental", !since.IsZero(), "unmapped_statuses", len(unmapped))

	return facts, startedAt.Format(time.RFC3339), nil
}

// versionCreatedFact observes that a version exists in the tracker.
//
// # Why "created" on a read that returns everything
//
// Every pass re-reads all the versions, and therefore produces one fact per version at each
// round. It is no waste: the **deduplication key** carries the version and nothing else, so
// that a version already seen is written only once. Creation is therefore deduced from
// uniqueness, without the store having to say whether its write inserted or updated.
//
// It is this shape — a snapshot returned whole, deduplicated by key — that the probe contract
// did not know before this migration.
//
// # The key carries the local version, and that is what changed
//
// It carried the tracker's project and the Redmine identifier. Now that the fact belongs to the
// core, it carries the **deposited row**: two trackers wired to the same project no longer
// observe the same version twice, and an instance changing tool would not observe its whole
// history again.
func versionCreatedFact(
	versionID int64, version provider.Version, tool string, fallback time.Time,
) event.Event {
	return event.VersionCreatedAt(
		// The update date in the tracker, for want of better: Redmine does not return a version's
		// creation date. It is worth the fact's date on the day it is new, and that is the only day it
		// counts.
		orNow(version.UpdatedOn, fallback),
		versionID, version.Name, version.Status,
		tool, version.ID, version.DueDate)
}

// issueClosedFact observes that a request is closed.
//
// The key does not carry the status: a request reopened then reclosed does not produce a second
// fact. That is deliberate — the fact says "this request has been closed", not "it has just
// been", and a team reopening one to fix a label has no business sending another delivery
// notification.
func issueClosedFact(
	closedType, trackerProject string, issue provider.Issue, fallback time.Time,
) event.Event {
	payload := map[string]string{
		"issue_id": issue.ID,
		"subject":  issue.Subject,
		"tracker":  issue.Tracker,
		"status":   issue.Status,
	}
	if issue.VersionID != nil {
		payload["version_id"] = *issue.VersionID
	}

	return event.Event{
		Type:       event.Type(closedType),
		Subject:    "#" + issue.ID,
		Payload:    payload,
		OccurredAt: orNow(issue.UpdatedOn, fallback),
		DedupKey:   closedType + ":" + trackerProject + ":" + issue.ID,
	}
}

// orNow returns the tracker's date, or the pass's failing that.
//
// A missing date must not produce a fact dated from year zero: the fact feed is ordered by it,
// and such a fact would bury itself under everything else. The pass's instant is an honest
// approximation — it is the moment it was observed, for want of knowing when it happened.
func orNow(moment *time.Time, fallback time.Time) time.Time {
	if moment == nil || moment.IsZero() {
		return fallback
	}
	return *moment
}

// reportAdoptions logs the matching of the provisional versions.
//
// An ambiguity is a warning and not an error: it comes from the naming on the client's side,
// not from a failure of the dashboard, and the synchronisation must complete.
func (s *TrackerSync) reportAdoptions(adoptions []store.Adoption) {
	for _, adoption := range adoptions {
		if adoption.Ambiguous {
			s.logger.Warn("provisional version claimed by several tracker versions",
				"provisional", adoption.PendingName,
				"candidates", adoption.Candidates,
				"consequence", "attachment left pending, to be settled by hand")
			continue
		}
		s.logger.Info("provisional version adopted",
			"provisional", adoption.PendingName,
			"version", adoption.AdoptedInto,
			"releases", adoption.Releases)
	}
}

// cursorSince returns the resume instant, overlap margin included.
//
// An unreadable cursor is treated as an absent cursor: re-reading everything is expensive but
// correct, where starting again from a wrong instant would lose requests silently.
func cursorSince(cursor string) time.Time {
	if cursor == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return time.Time{}
	}
	return parsed.Add(-cursorOverlap)
}

// The same guard as on the HTTP side: what the worker asks of its sources must stay a subset of
// what the boundary announces, otherwise a future decoupling will silently lose the callers
// that overreached.
var (
	_ TrackerReader = provider.Tracker(nil)
	_ PullReader    = provider.Forge(nil)
)

// externalRef translates a numeric identifier into a textual reference.
//
// Null stays null: a request with no version targets none, and `"0"` would name a version that
// does not exist.
func externalRef(id *int64) *string {
	if id == nil {
		return nil
	}
	reference := strconv.FormatInt(*id, 10)
	return &reference
}
