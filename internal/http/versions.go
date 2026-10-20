package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

// VersionStore is what the version routes need from the database.
type VersionStore interface {
	ListVersionSummaries(ctx context.Context, projectID int64) ([]store.VersionSummary, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	GetVersionSummary(ctx context.Context, id int64) (store.VersionSummary, error)
	ListVersionIssues(ctx context.Context, versionID int64) ([]store.VersionIssue, error)
	ListVersionReleases(ctx context.Context, versionID int64) ([]store.VersionRelease, error)
	GetSyncState(ctx context.Context, source string, projectID int64) (store.SyncState, error)
	// EnsureVersion creates a project's version, or returns the one already carrying that
	// name. Idempotent by name, which is the right behaviour for a manual declaration: asking
	// for a version that exists must lead to it.
	EnsureVersion(ctx context.Context, projectID int64, name string) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	// GetProject serves the suggested tag: the project declares its version scheme and its
	// cycle, hence what a form must offer.
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// VersionsHandler serves the tracker's versions and their progress.
type VersionsHandler struct {
	store VersionStore
	// trackers serves the outgoing links and the synchronisation's state. Nil, or a project with
	// no tracker, and the links are omitted rather than rendered broken.
	trackers Trackers
}

func NewVersionsHandler(versions VersionStore, trackers Trackers) *VersionsHandler {
	return &VersionsHandler{store: versions, trackers: trackers}
}

// completionVw carries the two completion measures.
//
// Both are sent because they do not say the same thing: by issue count, readable but a
// large issue counts as much as a small one; weighted by `done_ratio`, finer but dependent
// on how carefully the tracker is filled in.
//
// The computation is done here and not in the browser: it is a business rule, and
// duplicating it client-side would let it diverge — the same mistake as the health
// thresholds, already corrected.
type completionVw struct {
	// Counted is the denominator: the issues other than `rejected`.
	Counted int `json:"counted"`
	Done    int `json:"done"`
	// Ratio is null when nothing is countable: zero per cent would suggest an empty version is
	// behind.
	Ratio         *float64 `json:"ratio"`
	WeightedRatio *float64 `json:"weighted_ratio"`
}

func completionOf(summary store.VersionSummary) completionVw {
	view := completionVw{Counted: summary.Counted, Done: summary.Done}
	if summary.Counted == 0 {
		return view
	}
	ratio := float64(summary.Done) / float64(summary.Counted)
	weighted := float64(summary.WeightedDone) / float64(summary.Counted) / 100
	view.Ratio = &ratio
	view.WeightedRatio = &weighted
	return view
}

type versionVw struct {
	ID         int64      `json:"id"`
	ExternalID string     `json:"external_id,omitempty"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	DueDate    *time.Time `json:"due_date"`
	SyncedAt   time.Time  `json:"synced_at"`
	// Pending marks a provisional version: a release attaches to it, but synchronisation did
	// not find the corresponding version in the tracker.
	Pending    bool         `json:"pending"`
	Completion completionVw `json:"completion"`
	// OpenAnomalies is counted separately and does not enter completion: a version may be
	// functionally complete and carry some.
	OpenAnomalies int    `json:"open_anomalies"`
	Releases      int    `json:"releases"`
	URL           string `json:"url,omitempty"`
}

func (h *VersionsHandler) view(summary store.VersionSummary, tracker projectTracker) versionVw {
	view := versionVw{
		ID: summary.ID, Name: summary.Name, Status: summary.Status,
		DueDate: summary.DueDate, SyncedAt: summary.SyncedAt, Pending: summary.Pending,
		Completion: completionOf(summary), OpenAnomalies: summary.OpenAnomalies,
		Releases: summary.Releases,
	}
	// A provisional version, or one of a project with no tracker, has no external identifier:
	// the client then has no link to build.
	if !summary.Pending && summary.ExternalID != "" {
		view.ExternalID = summary.ExternalID
		view.URL = tracker.versionURL(summary.ExternalID)
	}
	return view
}

// List returns the versions and their progress.
func (h *VersionsHandler) List(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	summaries, err := h.store.ListVersionSummaries(r.Context(), currentID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the versions", "")
		return
	}

	tracker := trackerOf(r.Context(), h.trackers, currentID)
	payload := make([]versionVw, 0, len(summaries))
	for _, summary := range summaries {
		payload = append(payload, h.view(summary, tracker))
	}

	// The age of the data comes with the list: the tracker may not answer, and a screen
	// showing figures without saying when they date from is misleading.
	//
	// The state of the project being consulted, and not the instance's: two tracked projects
	// may target two servers, and showing one's data age on the other's screen would say "up
	// to date" of a tracker that is down.
	var state store.SyncState
	if source := tracker.source(); source != "" {
		state, err = h.store.GetSyncState(r.Context(), source, currentID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the synchronisation state", "")
			return
		}
	}

	tracked := false
	if project, err := h.store.GetProject(r.Context(), currentID); err == nil {
		tracked = strings.TrimSpace(project.TrackerProject) != ""
	}

	sync := syncStateView(state)
	sync.Tracked = tracked

	writeJSON(w, http.StatusOK, map[string]any{
		"versions": payload,
		"sync":     sync,
	})
}

type issueVw struct {
	ExternalID string `json:"external_id"`
	Tracker    string `json:"tracker"`
	Subject    string `json:"subject"`
	StatusName string `json:"status_name"`
	Bucket     string `json:"bucket"`
	Priority   string `json:"priority,omitempty"`
	Assignee   string `json:"assignee,omitempty"`
	DoneRatio  int    `json:"done_ratio"`
	IsClosed   bool   `json:"is_closed"`
	Anomaly    bool   `json:"anomaly"`
	URL        string `json:"url,omitempty"`
}

// Get returns a version's scope.
//
// The issues are split into three buckets and the anomalies listed separately. The
// grouping is done here because it follows the same rule that decides completion:
// separating it from its computation would let them diverge.
func (h *VersionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid version identifier", "")
		return
	}

	summary, err := h.store.GetVersionSummary(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrVersionNotFoundByID) {
			writeProblem(w, http.StatusNotFound, "Version not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the version", "")
		return
	}

	issues, err := h.store.ListVersionIssues(r.Context(), id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the requests", "")
		return
	}

	// Initialised from the store's ordered list: a bucket added there appears here with no
	// edit, and a forgotten bucket would return null where the interface iterates.
	buckets := map[string][]issueVw{store.BucketRejected: {}}
	for _, bucket := range store.Buckets {
		buckets[bucket] = []issueVw{}
	}
	tracker := trackerOf(r.Context(), h.trackers, summary.ProjectID)
	anomalies := make([]issueVw, 0)
	for _, issue := range issues {
		view := issueVw{
			ExternalID: issue.ExternalID, Tracker: issue.Tracker, Subject: issue.Subject,
			StatusName: issue.StatusName, Bucket: issue.Bucket, Priority: issue.Priority,
			Assignee: issue.Assignee, DoneRatio: issue.DoneRatio,
			IsClosed: issue.IsClosed, Anomaly: issue.Anomaly,
		}
		view.URL = tracker.issueURL(issue.ExternalID)

		if bucket, known := buckets[issue.Bucket]; known {
			buckets[issue.Bucket] = append(bucket, view)
		} else {
			// An unknown bucket must not make the issue disappear from the screen. The database
			// constraint forbids it, but an invisible issue would be a silent loss where a misfiled
			// issue shows.
			buckets[store.BucketTodo] = append(buckets[store.BucketTodo], view)
		}

		// Anomalies are also in their bucket: they are part of the scope. The separate list is a
		// second reading angle, not a move — an anomaly removed from the scope would be invisible
		// to whoever looks at the columns.
		//
		// "Open" is read from the mapped bucket and not from the tracker's `is_closed` flag. The
		// two diverge — one status is declared not closed while it maps to `done` — and using two
		// notions of "closed" in the same screen made finished anomalies appear in the open
		// banner.
		if issue.Anomaly && store.BucketIsOpen(issue.Bucket) {
			anomalies = append(anomalies, view)
		}
	}

	releases, err := h.store.ListVersionReleases(r.Context(), id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the releases", "")
		return
	}
	releasePayload := make([]map[string]any, 0, len(releases))
	for _, item := range releases {
		releasePayload = append(releasePayload, map[string]any{
			"tag": item.Tag, "kind": item.Kind, "state": item.State,
			"image_version": item.ImageVersion, "created_at": item.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":   h.view(summary, tracker),
		"buckets":   buckets,
		"anomalies": anomalies,
		"releases":  releasePayload,
		// The tag offered to the creation form, computed here.
		//
		// The interface used to compose it itself: it stripped prefixes — since made
		// configurable — and appended a candidate suffix, one deployment's convention. Two rules
		// the server owns, copied into a form, with no reason left to be right there.
		"suggested_tags": h.suggestedTags(r.Context(), summary, releases),
	})
}

type syncStateVw struct {
	LastRun *time.Time `json:"last_run"`
	LastOK  *time.Time `json:"last_ok"`
	// LastError is the message of the last failed attempt. Non-empty while LastOK is set means
	// "valid but ageing data", which does not call for the same response as no data at all.
	LastError string `json:"last_error,omitempty"`
	// Configured says whether synchronisation has ever run. False with an empty database
	// separates "not synchronised yet" from "synchronised, nothing to show".
	EverRan bool `json:"ever_ran"`
	// Tracked says whether the project declares a tracking tool.
	//
	// Without it, the interface could not tell "the tracker has not run yet" from "this
	// project has none", and announced "no version declared in the tracker project" to a
	// project that never had one. That is exactly the gap the verification milestone forbids:
	// the interface must say it properly.
	Tracked bool `json:"tracked"`
}

func syncStateView(state store.SyncState) syncStateVw {
	return syncStateVw{
		LastRun: state.LastRun, LastOK: state.LastOK,
		LastError: state.LastError, EverRan: state.LastRun != nil,
	}
}

// suggestedTags offers one tag per declared kind.
//
// Empty when the scheme cannot answer — an opaque project has no "next one" — and the
// interface then offers free-text input rather than an invented number.
func (h *VersionsHandler) suggestedTags(
	ctx context.Context, summary store.VersionSummary, releases []store.VersionRelease,
) map[string]string {
	project, err := h.store.GetProject(ctx, summary.ProjectID)
	if err != nil {
		return nil
	}
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		return nil
	}

	// The version line, normalised: `v3.1.0` and `3.1.0` name the same one.
	base := release.NormalizeVersionName(summary.Name)

	existing := make([]string, 0, len(releases))
	for _, item := range releases {
		existing = append(existing, item.Tag)
	}

	suggestions := map[string]string{}
	for _, spec := range project.ReleaseKinds {
		switch spec.Tag {
		case release.TagPrereleaseRequired:
			if next, err := scheme.NextPrerelease(base, existing); err == nil {
				suggestions[spec.ID] = next
			}
		default:
			// The kinds that forbid or require nothing take the line as it is: that is what a final
			// version carries, and the only choice that invents nothing for the others.
			if _, err := scheme.Parse(base); err == nil {
				suggestions[spec.ID] = base
			}
		}
	}
	return suggestions
}

// Create declares a version by hand.
//
// A project with no tracker had no way to start. Its versions were born from
// EnsureVersion when a release was created; but the creation form lives on a version's
// screen, which did not exist. An empty screen, no button, no way out.
//
// It refuses on a tracked project because those versions belong to its tool: that tool is
// authoritative, and the dashboard never writes to it. A version created here would be a
// provisional one that synchronisation would try to adopt without ever succeeding — it
// would stay flagged "pending" indefinitely, which is exactly the anomaly PendingVersions
// exists to surface.
func (h *VersionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	if strings.TrimSpace(project.TrackerProject) != "" {
		writeProblem(w, http.StatusConflict, "Tracked project",
			"this project's versions come from its tracker, which is "+
				"authoritative: declaring one here would stay awaiting an adoption "+
				"that would never come")
		return
	}

	var request struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	name := strings.TrimSpace(request.Name)
	if name == "" {
		writeProblem(w, http.StatusBadRequest, "Empty version name",
			"a version is named by its number, for example 1.8.0")
		return
	}

	// The shape is checked against the project's scheme. An opaque scheme accepts everything,
	// which is its answer: refusing here would impose a convention the project has not
	// declared.
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Unreadable version scheme", "")
		return
	}
	if _, err := scheme.Parse(release.NormalizeVersionName(name)); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid version name", err.Error())
		return
	}

	id, err := h.store.EnsureVersion(r.Context(), project.ID, name)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot create", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditVersionDeclare, name,
		map[string]any{"project": project.Slug, "id": id})

	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name})
}
