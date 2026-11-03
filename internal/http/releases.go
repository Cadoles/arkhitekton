package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	imagenames "forge.cadoles.com/vcarroy/arkhitekton/internal/images"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// ReleaseStore is what the release handlers need from the database.
type ReleaseStore interface {
	EnsureVersion(ctx context.Context, projectID int64, name string) (int64, error)
	CreateRelease(ctx context.Context, release store.Release) (int64, error)
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	LaunchRelease(ctx context.Context, id int64, to, commit, actor string) error
	UpdateDraftRelease(ctx context.Context, id int64, gitRef string) error
	DeleteDraftRelease(ctx context.Context, id int64) error
	ListReleases(ctx context.Context, projectID int64, limit int) ([]store.Release, error)
	SetManualActions(ctx context.Context, tag, markdown, author string) error
	ListReleaseJobs(ctx context.Context, releaseID int64) ([]store.ReleaseJob, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	// RecordEvent records an observed fact. Returns false when it was already known,
	// which is not an error.
	RecordEvent(ctx context.Context, fact event.Event) (bool, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	// GetProject says which images a release builds and which a re-tag propagates. Read
	// on every enqueue rather than memorised at start-up: the stages are edited from
	// administration, and a release launched after the change must build what is
	// declared now.
	//
	// By identifier, the one the route carries: looking for "the buildable project"
	// assumed there was only one, which a second declared project makes false.
	GetProject(ctx context.Context, id int64) (store.Project, error)
	SetReleaseState(ctx context.Context, id int64, state string) error
	MarkReleased(ctx context.Context, id int64, from string) (bool, error)
	AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error)
	ReleaseBadgesByTag(ctx context.Context, buildKind string) (map[string]store.ReleaseBadges, error)
	ListNoteEntries(ctx context.Context, releaseID int64) ([]store.NoteEntry, error)
	VersionName(ctx context.Context, versionID int64) (string, error)
	ListReleaseVersionIssues(ctx context.Context, releaseID int64) ([]store.NoteEntry, error)
	LinkNoteIssue(ctx context.Context, releaseID int64, reference, source, pullRequest, actor string) error
	UnlinkNoteIssue(ctx context.Context, releaseID int64, reference, actor string) error
	Directory(ctx context.Context) (map[string]string, error)
	// The two score reads. Here because the release list shows the same figure as a
	// release's screen, and two separate computations would eventually diverge.
	ListReleaseTestResults(ctx context.Context, releaseID int64) ([]store.TestResult, error)
	LatestSecurityAudit(ctx context.Context, releaseID int64) (store.SecurityAudit, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
}

// ReleasePlanner opens a release's plan.
//
// The interface is declared here rather than imported from internal/worker: the two
// packages are siblings, joined by main, and making the api depend on the worker
// would have it carry the Tekton observer, the scheduler and the probes for one
// method.
type ReleasePlanner interface {
	// OpenReleasePlan composes a release's plan and sets it going. Returns how many steps
	// left, or 1 when the opening is queued for the worker; zero when nothing could leave.
	OpenReleasePlan(ctx context.Context, project store.Project,
		record store.Release, revision string) int
}

type ReleasesHandler struct {
	store      ReleaseStore
	catalog    catalog.Source
	authorizer *auth.Authorizer
	// plans opens a release's plan. Nil: no build leaves, and creation says so as a
	// warning rather than announcing a release as building when it is not.
	plans ReleasePlanner
	// images resolves the digests. Nil when the registry is not configured: the
	// dashboard stays usable without it, the screen then shows tags without their
	// digests and says so.
	images Images
	// logger serves to say what fails without failing the screen. Nil-safe:
	// `slog.Default()` by default, so tests need not supply one.
	logger *slog.Logger
	// trackers serves the links to a note's issues. Nil, or a project with no tracker, and the
	// links are omitted rather than rendered dead.
	trackers Trackers
	// registryProject prefixes the published images. It serves to name, to the audit,
	// the ones it must analyse on a re-run — it analyses an image already published,
	// never a rebuild.
	registryProject string
	// forges resolves a project's forge, to anchor a release on a commit and tag it. Nil when
	// no forge is wired.
	forges Forges
	// artifactReader reads what a release's automation produced, and the frozen plan
	// that gives the steps their names. Nil: the section stays empty rather than
	// preventing the page.
	artifactReader ArtifactReader
	// deployments returns, by tag, the environments where a release runs. Nil: the badge
	// is never shown. Supplied by the extensions, which own those tables.
	deployments func(context.Context, int64) (map[string][]string, error)
	// promotions returns a release's requested promotions. Nil: none.
	promotions func(context.Context, int64, int64) ([]ReleasePromotion, error)
	// scoreCatalog serves the score, and for the same reason as in the tests handler: to
	// name the family of a step that produced nothing. The list's badge and a release's
	// screen must show the same figure, so they must start from the same catalogue.
	scoreCatalog catalog.Source
}

// WithTrackers wires the links to the issues, which the project's tracker plugin composes.
func (h *ReleasesHandler) WithTrackers(trackers Trackers) *ReleasesHandler {
	h.trackers = trackers
	return h
}

// ArtifactReader returns what a release produced, and the plan that produced it.
//
// A narrow interface rather than the whole store: the screen needs only those two
// answers.
type ArtifactReader interface {
	ReleaseArtifacts(ctx context.Context, releaseID int64) ([]store.PlanArtifact, error)
	PlanRunByID(ctx context.Context, id int64) (store.PlanRun, error)
}

// WithArtifactReader wires the reading of a release's artifacts.
func (h *ReleasesHandler) WithArtifactReader(reader ArtifactReader) *ReleasesHandler {
	h.artifactReader = reader
	return h
}

// ReleasePromotion is a requested promotion, as a release's screen displays it and
// as its artifacts compare it with the registry.
type ReleasePromotion struct {
	Environment string
	Image       string
	Tag         string
	Digest      string
	JobID       *int64
	PushedAt    time.Time
}

// WithDeployments wires the "deployed on" badge.
func (h *ReleasesHandler) WithDeployments(deployments func(ctx context.Context, projectID int64) (map[string][]string, error)) *ReleasesHandler {
	h.deployments = deployments
	return h
}

// WithPromotions wires the reading of requested promotions.
func (h *ReleasesHandler) WithPromotions(promotions func(ctx context.Context, projectID, releaseID int64) ([]ReleasePromotion, error)) *ReleasesHandler {
	h.promotions = promotions
	return h
}

// deployedOn returns the environments where the releases run, or nothing.
//
// A failed read does not fail the screen: the badge is a landmark, not information
// any decision taken here depends on.
func (h *ReleasesHandler) deployedOn(ctx context.Context, projectID int64) map[string][]string {
	if h.deployments == nil {
		return nil
	}
	deployed, err := h.deployments(ctx, projectID)
	if err != nil {
		h.logger.Error("releases' environments", "error", err)
		return nil
	}
	return deployed
}

// releasePromotions returns a release's requested promotions.
func (h *ReleasesHandler) releasePromotions(ctx context.Context, projectID, releaseID int64) ([]ReleasePromotion, error) {
	if h.promotions == nil {
		return nil, nil
	}
	return h.promotions(ctx, projectID, releaseID)
}

// WithScoreCatalog wires the catalogue the score uses.
//
// The weighting no longer comes from configuration but from the automation's steps,
// frozen in each release's plan. The catalogue is still needed to name what a step
// would have measured had it run.
func (h *ReleasesHandler) WithScoreCatalog(registry catalog.Source) *ReleasesHandler {
	h.scoreCatalog = registry
	return h
}

// WithForge wires commit resolution and tag setting.
//
// Both together because they are one gesture: resolving a branch into a commit then
// setting the tag on that commit. Setting a tag on a branch would place it on
// whatever the branch names at that instant, and two releases created a minute apart
// could aim at different commits.
func (h *ReleasesHandler) WithForges(forges Forges) *ReleasesHandler {
	h.forges = forges
	return h
}

// Images is what the handler needs from the registry: reading, never writing. The
// re-tag is done by a Task with its own credentials.
type Images interface {
	Digest(ctx context.Context, repository, tag string) (string, error)
}

// CommitResolver is what the handler needs from the forge to anchor a release.
//
// Optional: without it, the release is created from the reference typed in and the
type CommitResolver interface {
	ResolveCommit(ctx context.Context, ref string) (string, error)
}

// Tagger sets a release's git tag.
//
// Separate from [CommitResolver]: reading the forge and writing to it are two
// permissions, carried by two clients. Nil when the token cannot write.
type Tagger interface {
	CreateTag(ctx context.Context, tag, commit, message string) error
}

// ReleaseNotesJobKind is the contract with the scheduler. Written here rather than imported from
// `worker`: this package does not depend on the scheduler, and the reverse holds — depending on it
// would create a cycle with the worker's tests, which read this package. A consistency test keeps
// the two aligned.
const ReleaseNotesJobKind = "release-notes"

// WithPlans wires the opening of a release's plan.
func (h *ReleasesHandler) WithPlans(plans ReleasePlanner) *ReleasesHandler {
	h.plans = plans
	return h
}

func NewReleasesHandler(
	releases ReleaseStore, pipelines catalog.Source,
	authorizer *auth.Authorizer, images Images, registryProject string,
) *ReleasesHandler {
	return &ReleasesHandler{
		store: releases, catalog: pipelines, authorizer: authorizer,
		images: images, registryProject: registryProject,
		logger: slog.Default(),
	}
}

type releasePayload struct {
	Tag   string `json:"tag"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	// ImageVersion differs from the tag for every final release produced by re-tag: the
	// image carries the approved candidate's version, including the version baked into
	// it. It is that value, and not the tag, that must be compared with what the
	// application reports.
	ImageVersion string `json:"image_version"`
	GitRef       string `json:"git_ref,omitempty"`
	// GitCommit is what was actually built. Distinct from GitRef, which only says where
	// the release started from: a branch moves, a commit does not.
	GitCommit  string              `json:"git_commit,omitempty"`
	SourceTag  string              `json:"source_tag,omitempty"`
	CreatedBy  string              `json:"created_by"`
	CreatedAt  time.Time           `json:"created_at"`
	LaunchedBy string              `json:"launched_by,omitempty"`
	LaunchedAt *time.Time          `json:"launched_at,omitempty"`
	ReleasedAt *time.Time          `json:"released_at,omitempty"`
	Promotions []promotionPayload  `json:"promotions,omitempty"`
	Jobs       []releaseJobPayload `json:"jobs,omitempty"`
	// Artifacts lists what the automation produced.
	//
	// It replaces the "images" section, which broke down a single repository — the one
	// the configuration names — into its tags. A project publishing three saw only one,
	// and a project producing a binary saw nothing.
	Artifacts []planArtifactPayload `json:"artifacts,omitempty"`
	// ArtifactsNote says why the digests are missing. An absent digest and an unreadable
	// one do not look alike: the first reports an image that does not exist, the second
	// an unreachable or unconfigured registry. Confusing them would send someone hunting
	// for a build failure.
	ArtifactsNote string          `json:"artifacts_note,omitempty"`
	ManualAction  *manualActionVw `json:"manual_actions,omitempty"`
	// What a screen shows beside the name, without having to unfold the release.
	//
	// BuildFailed is about the last build: a release retried after an infrastructure
	// failure would otherwise keep a badge nothing justifies.
	BuildFailed bool `json:"build_failed,omitempty"`
	// DeployedOn lists the environments running this release. Empty as soon as another
	// has been deployed there since: the list comes from what each environment reports,
	// not from the history of promotions.
	DeployedOn []string     `json:"deployed_on,omitempty"`
	Score      *scoreDigest `json:"score,omitempty"`
}

// scoreDigest is the score without its breakdown.
//
// The detail lives on a release's screen; a badge needs only the figure and its
// verdict. Measured separates "not evaluated" from "zero", which the figure alone
// would conflate.
type scoreDigest struct {
	Value    int    `json:"value"`
	Health   string `json:"health"`
	Measured bool   `json:"measured"`
}

type releaseJobPayload struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	// Label and Role come from the catalogue, so the interface names no pipeline itself. Role is
	// `build`, `verification`, or empty.
	Label       string     `json:"label,omitempty"`
	Role        string     `json:"role,omitempty"`
	State       string     `json:"state"`
	Environment string     `json:"environment,omitempty"`
	RunName     string     `json:"run_name,omitempty"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// jobRole names a job's pipeline and says what it does for the release.
func (h *ReleasesHandler) jobRole(kind string) (label, role string) {
	if h.catalog == nil {
		return "", ""
	}
	if pipeline, found := catalog.Of(h.catalog).Find(kind); found {
		label, kind = pipeline.Label, pipeline.Kind
	}
	switch {
	case kind == catalog.KindBuild:
		role = "build"
	case catalog.Of(h.catalog).IsVerification(kind):
		role = "verification"
	}
	return label, role
}

type manualActionVw struct {
	Markdown  string     `json:"markdown"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type promotionPayload struct {
	Environment string    `json:"environment"`
	Image       string    `json:"image"`
	Tag         string    `json:"tag"`
	Digest      string    `json:"digest,omitempty"`
	JobID       *int64    `json:"job_id,omitempty"`
	PushedAt    time.Time `json:"pushed_at"`
}

func toReleasePayload(release store.Release, sourceTag string, who actors) releasePayload {
	return releasePayload{
		Tag: release.Tag, Kind: release.Kind, State: release.State,
		ImageVersion: release.ImageVersion, GitRef: release.GitRef,
		GitCommit: release.GitCommit, SourceTag: sourceTag,
		CreatedBy: who.name(release.CreatedBy),
		CreatedAt: release.CreatedAt, ReleasedAt: release.ReleasedAt,
		LaunchedBy: who.name(release.LaunchedBy), LaunchedAt: release.LaunchedAt,
	}
}

// actorsFor loads the directory for the duration of a request.
func (h *ReleasesHandler) actorsFor(ctx context.Context) actors {
	return resolveActors(ctx, h.store, h.logger)
}

// List returns the most recent releases.
func (h *ReleasesHandler) List(w http.ResponseWriter, r *http.Request) {
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	currentID := project.ID

	releases, err := h.store.ListReleases(r.Context(), currentID, 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the releases", "")
		return
	}

	badges, err := h.store.ReleaseBadgesByTag(r.Context(), catalog.KindBuild)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the releases", "")
		return
	}

	who := h.actorsFor(r.Context())
	deployed := h.deployedOn(r.Context(), currentID)

	payload := make([]releasePayload, 0, len(releases))
	for _, item := range releases {
		entry := toReleasePayload(item, "", who)
		entry.BuildFailed = badges[item.Tag].BuildFailed
		entry.DeployedOn = deployed[item.Tag]
		// The score is computed release by release, hence two reads each. That is accepted:
		// the list is bounded to fifty entries, the queries are indexed, and the same figure
		// must come out here and on a release's screen — an approximate computation for the
		// list would have let them diverge.
		entry.Score = h.scoreDigest(r.Context(), item.ID)
		payload = append(payload, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"releases": payload,
		// The cycle comes with the list: the creation screen used to be driven by a
		// hard-coded list, which would have offered nothing to a project using other kinds.
		// It now reads it.
		"kinds": kindsPayload(project.ReleaseKinds),
	})
}

// releaseKindPayload describes a declared kind, as the creation screen offers it.
//
// Mode and Source appear because the form uses them: a re-tag kind asks for a source
// release and not a git reference, and without that information the screen would ask
// for both.
type releaseKindPayload struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Mode  string `json:"mode"`
	// Source names the kind this one takes its image from. Empty: any earlier release
	// will do.
	Source string `json:"source,omitempty"`
}

func kindsPayload(cycle release.Cycle) []releaseKindPayload {
	payload := make([]releaseKindPayload, 0, len(cycle))
	for _, spec := range cycle {
		label := spec.Label
		if label == "" {
			label = spec.ID
		}
		payload = append(payload, releaseKindPayload{
			ID: spec.ID, Label: label, Mode: string(spec.Mode), Source: spec.Source,
		})
	}
	return payload
}

// scoreDigest returns a release's score, or nothing.
//
// A failed read does not fail the screen: the list stays useful without a score
// badge, and refusing to display it over a secondary signal would be
// disproportionate. The case is silent because it calls for no action.
func (h *ReleasesHandler) scoreDigest(ctx context.Context, releaseID int64) *scoreDigest {
	reports, err := h.store.ListReleaseTestResults(ctx, releaseID)
	if err != nil {
		return nil
	}
	breakdown, err := computeScore(ctx, h.store, releaseID, reports, catalog.Of(h.scoreCatalog))
	if err != nil {
		return nil
	}
	return &scoreDigest{
		Value:    breakdown.Score,
		Health:   results.Health(breakdown),
		Measured: breakdown.Measured,
	}
}

// Get returns a release and its promotions. Its targets are served by the extension that
// deploys, which alone can judge them.
func (h *ReleasesHandler) Get(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	item, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	who := h.actorsFor(r.Context())
	sourceTag := ""
	if item.SourceReleaseID != nil {
		if source, err := h.store.GetReleaseByID(r.Context(), *item.SourceReleaseID); err == nil {
			sourceTag = source.Tag
		}
	}
	payload := toReleasePayload(item, sourceTag, who)

	promotions, err := h.releasePromotions(r.Context(), currentID, item.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the promotions", "")
		return
	}
	for _, promotion := range promotions {
		payload.Promotions = append(payload.Promotions, promotionPayload{
			Environment: promotion.Environment, Image: promotion.Image,
			Tag: promotion.Tag, Digest: promotion.Digest,
			JobID: promotion.JobID, PushedAt: promotion.PushedAt,
		})
	}

	jobs, err := h.store.ListReleaseJobs(r.Context(), item.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the runs", "")
		return
	}
	for _, job := range jobs {
		label, role := h.jobRole(job.Kind)
		payload.Jobs = append(payload.Jobs, releaseJobPayload{
			ID: job.ID, Kind: job.Kind, Label: label, Role: role,
			State: job.State, Environment: job.EnvironmentID,
			RunName: job.RunName, RequestedBy: who.name(job.RequestedBy),
			RequestedAt: job.RequestedAt, FinishedAt: job.FinishedAt,
		})
	}

	// The same badges as in the list, and by the same path: two derivations of "did the
	// build fail" would have ended up contradicting each other from one screen to the
	// next.
	if badges, err := h.store.ReleaseBadgesByTag(r.Context(), catalog.KindBuild); err == nil {
		payload.BuildFailed = badges[item.Tag].BuildFailed
	}
	payload.DeployedOn = h.deployedOn(r.Context(), currentID)[item.Tag]
	payload.Score = h.scoreDigest(r.Context(), item.ID)

	if item.ManualActions != "" || item.ManualActionsUpdatedAt != nil {
		payload.ManualAction = &manualActionVw{
			Markdown:  item.ManualActions,
			UpdatedBy: who.name(item.ManualActionsUpdatedBy),
			UpdatedAt: item.ManualActionsUpdatedAt,
		}
	}

	payload.Artifacts = h.releaseArtifacts(r.Context(), item.ID, promotions)
	if h.images == nil && len(payload.Artifacts) > 0 {
		payload.ArtifactsNote = "The registry is not configured: the digests cannot be read."
	}

	writeJSON(w, http.StatusOK, payload)
}

type createReleaseRequest struct {
	Tag  string `json:"tag"`
	Kind string `json:"kind"`
	// TrackedVersion is the name of the version aimed at. Optional: it is derived from
	// the tag. Supplied, it is checked — the only way to catch a release attached to the
	// wrong version before the release note compares unrelated scopes.
	TrackedVersion string `json:"tracked_version,omitempty"`
	GitRef         string `json:"git_ref,omitempty"`
	// SourceTag is the pre-release being re-tagged. Required for a final release.
	SourceTag string `json:"source_tag,omitempty"`
	// TestsGreen says whether the candidate's results are green. False does not forbid
	// the release — that decision belongs to the deployment — but it is flagged.
	TestsGreen bool `json:"tests_green,omitempty"`
}

// Create records a release.
//
// Validation is done by internal/release, a pure package, and never here: the version
// invariant is checkable without a database or a cluster, and that is what makes it
// testable.
func (h *ReleasesHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	var request createReleaseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	// The project decides the shape of its tags and its kinds: both are read before any
	// validation, since it is the project that says what is valid.
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Unreadable version scheme", "")
		return
	}

	parsed, err := scheme.Parse(request.Tag)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid tag", err.Error())
		return
	}

	spec, err := project.ReleaseKinds.Find(request.Kind)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Unknown release kind", err.Error())
		return
	}
	if err := release.CheckTag(spec, parsed); err != nil {
		writeProblem(w, http.StatusBadRequest, "Inconsistent release kind", err.Error())
		return
	}

	// The permission is the one the kind declares, and not the one a name suggests.
	// Shipping commits what reaches the user: the route requires `release:create`, the
	// kind may require more.
	if spec.Permission != "" && !h.authorizer.Can(identity, auth.Permission(spec.Permission)) {
		writeProblem(w, http.StatusForbidden, "Insufficient permission",
			fmt.Sprintf("creating a %q release requires the %q permission", spec.ID, spec.Permission))
		return
	}

	if request.TrackedVersion != "" {
		if err := release.CheckTrackedVersion(parsed, request.TrackedVersion); err != nil {
			writeProblem(w, http.StatusBadRequest, "Inconsistent tracker version", err.Error())
			return
		}
	}

	record := store.Release{
		// The project, without which the foreign key refuses the insert: an int64's zero
		// value is not null, it is identifier 0, which names no project.
		//
		// It was missing after the migration attached releases to their project. Nobody saw
		// it because the failure read as "this release may already exist": the handler
		// translated every database error into a conflict, and the message sent someone
		// looking for a duplicate.
		ProjectID: project.ID,
		Tag:       parsed.Raw, Kind: spec.ID, GitRef: strings.TrimSpace(request.GitRef),
		// Every release starts as a draft: nothing leaves until it is launched, which leaves
		// time to attach issues to it.
		State:     string(release.StateDraft),
		CreatedBy: identity.Subject,
	}
	var warnings []string

	if spec.Mode == release.ModeBuild {
		// The image does not exist yet: it will carry the tag itself.
		//
		// The branch is on the mode and not on a kind's name. That is what made two kinds ask
		// for two branches for a single behaviour — building from code — and a fourth kind
		// would have asked for a third.
		record.ImageVersion = parsed.Raw
		if record.GitRef == "" {
			warnings = append(warnings,
				"the draft names no Git reference: give it one before launching it")
		}
	} else {
		source, err := h.store.GetRelease(r.Context(), project.ID, request.SourceTag)
		if err != nil {
			if errors.Is(err, store.ErrReleaseNotFound) {
				writeProblem(w, http.StatusBadRequest,
					"Source pre-release not found", request.SourceTag)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Cannot read the source", "")
			return
		}

		// Checked as soon as the draft is created, and again at launch: the source may be
		// cancelled meanwhile.
		imageVersion, sourceWarnings, err := release.FinalFromSource(release.Source{
			Tag:          source.Tag,
			ImageVersion: source.ImageVersion,
			State:        release.State(source.State),
			TestsGreen:   request.TestsGreen,
		})
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Cannot re-tag", err.Error())
			return
		}
		record.ImageVersion = imageVersion
		record.SourceReleaseID = &source.ID
		warnings = sourceWarnings
	}

	line := versionLine(parsed)
	versionID, err := h.store.EnsureVersion(r.Context(), project.ID, line)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Version unavailable", "")
		return
	}
	record.VersionID = versionID

	releaseID, err := h.store.CreateRelease(r.Context(), record)
	if err != nil {
		// The duplicate is told apart from the rest. Translating every database error into a
		// conflict cost dearly: a violated foreign key read as "this release may already
		// exist", which sends someone looking for a duplicate that does not exist while the
		// real cause stays invisible.
		if isDuplicate(err) {
			writeProblem(w, http.StatusConflict, "Release already created",
				fmt.Sprintf("tag %q already exists in this project", record.Tag))
			return
		}
		h.logger.Error("creating a release",
			"tag", record.Tag, "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot create", "")
		return
	}

	// The identifier returned by the insert is carried back onto the record.
	//
	// Without it `record.ID` stays zero — it was composed before being written — and
	// everything that follows names the release by an identifier that does not exist.
	// Observed in production: opening the plan failed on the release foreign key, with a
	// fact key ending in `:0`, and the release stayed a draft with no build leaving.
	record.ID = releaseID

	h.audit(r, store.AuditReleaseCreate, record.Tag, map[string]any{
		"kind": record.Kind, "image_version": record.ImageVersion, "source": request.SourceTag,
	})
	h.emit(r, event.ReleaseDraftedAt(
		time.Now(), project.ID, releaseID,
		record.Tag, record.Kind, record.GitRef, line))

	// The release note is detected on the draft: what it finds is a proposal, completed or
	// corrected by hand before the launch. In the background, since detection queries up
	// to two hundred pull requests.
	if err := h.enqueueNoteDetection(r.Context(), releaseID, identity.Subject); err != nil {
		warnings = append(warnings,
			"the release note could not be queued: "+err.Error())
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"release":  toReleasePayload(record, request.SourceTag, h.actorsFor(r.Context())),
		"warnings": warnings,
	})
}

// versionLine is the version line of a tag, when the scheme knows it. On an opaque scheme it
// is empty, and the whole tag then serves as the name: it is what the project wrote, and
// inventing one would be worse than reusing it.
func versionLine(parsed version.Version) string {
	if parsed.Base == "" {
		return parsed.Raw
	}
	return parsed.Base
}

func (h *ReleasesHandler) enqueueNoteDetection(ctx context.Context, releaseID int64, actor string) error {
	_, err := h.store.Enqueue(ctx, store.JobRequest{
		Kind:        ReleaseNotesJobKind,
		Params:      map[string]string{},
		RequestedBy: actor,
		ReleaseID:   &releaseID,
	})
	return err
}

// draft reads the release a draft route names, and refuses one that is no longer a draft.
func (h *ReleasesHandler) draft(w http.ResponseWriter, r *http.Request, project store.Project) (store.Release, bool) {
	tag := r.PathValue("tag")
	record, err := h.store.GetRelease(r.Context(), project.ID, tag)
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", tag)
			return record, false
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return record, false
	}
	if record.State != string(release.StateDraft) {
		writeProblem(w, http.StatusConflict, "Not a draft",
			fmt.Sprintf("release %q is in state %q: it was launched already", tag, record.State))
		return record, false
	}
	return record, true
}

// kindAllowed checks the permission the release's kind declares, as creation does.
func (h *ReleasesHandler) kindAllowed(
	w http.ResponseWriter, identity auth.Identity, project store.Project, record store.Release,
) (release.KindSpec, bool) {
	spec, err := project.ReleaseKinds.Find(record.Kind)
	if err != nil {
		writeProblem(w, http.StatusConflict, "Unknown release kind", err.Error())
		return spec, false
	}
	if spec.Permission != "" && !h.authorizer.Can(identity, auth.Permission(spec.Permission)) {
		writeProblem(w, http.StatusForbidden, "Insufficient permission",
			fmt.Sprintf("a %q release requires the %q permission", spec.ID, spec.Permission))
		return spec, false
	}
	return spec, true
}

// Launch launches a draft: what follows a release leaves now, and only now.
//
// A built release is anchored on the commit its reference names at this instant, tagged, then
// its plan opens. A re-tag goes to `ready` and its re-tag is queued. Both emit
// `release.created`, the fact the reactions listen to.
func (h *ReleasesHandler) Launch(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	record, ok := h.draft(w, r, project)
	if !ok {
		return
	}
	spec, ok := h.kindAllowed(w, identity, project, record)
	if !ok {
		return
	}
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Unreadable version scheme", "")
		return
	}
	parsed, err := scheme.Parse(record.Tag)
	if err != nil {
		writeProblem(w, http.StatusConflict, "Invalid tag", err.Error())
		return
	}

	var warnings []string
	to := release.StateBuilding
	sourceTag := ""
	if spec.Mode == release.ModeBuild {
		// Nothing builds without a reference: the plan would refuse it, and the release would
		// fail for a reason the draft could still fix.
		if record.GitRef == "" {
			writeProblem(w, http.StatusUnprocessableEntity, "Cannot launch",
				"the draft names no Git reference to build")
			return
		}
		// Anchoring on a commit before the launch is recorded, so that what is recorded names
		// an immutable point. Optional and said so: with no forge configured, the release
		// still launches, with a caveat.
		commit, resolveWarnings := h.anchorCommit(r.Context(), project.ID, record.GitRef)
		record.GitCommit = commit
		warnings = append(warnings, resolveWarnings...)
	} else {
		to = release.StateReady
		if record.SourceReleaseID == nil {
			writeProblem(w, http.StatusConflict, "Cannot re-tag", release.ErrSourceRequired.Error())
			return
		}
		source, err := h.store.GetReleaseByID(r.Context(), *record.SourceReleaseID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the source", "")
			return
		}
		// The candidate's results were flagged when the draft was created: the check here is
		// that it is still approved.
		if _, _, err := release.FinalFromSource(release.Source{
			Tag: source.Tag, ImageVersion: source.ImageVersion,
			State: release.State(source.State), TestsGreen: true,
		}); err != nil {
			writeProblem(w, http.StatusConflict, "Cannot re-tag", err.Error())
			return
		}
		sourceTag = source.Tag
	}

	switch err := h.store.LaunchRelease(r.Context(), record.ID, string(to), record.GitCommit, identity.Subject); {
	case errors.Is(err, store.ErrNotADraft):
		writeProblem(w, http.StatusConflict, "Not a draft",
			"the release was launched or deleted in the meantime, reload the page")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Cannot launch", "")
		return
	}
	now := time.Now()
	record.State, record.LaunchedBy, record.LaunchedAt = string(to), identity.Subject, &now

	if spec.Mode == release.ModeBuild && record.GitCommit != "" {
		// The tag is set for humans and for traceability; the build starts from the commit.
		// Its failure is therefore a caveat and does not prevent building.
		_, tagWarnings := h.tagRelease(r.Context(), project.ID, record.Tag, record.GitCommit)
		warnings = append(warnings, tagWarnings...)
	}

	h.audit(r, store.AuditReleaseLaunch, record.Tag, map[string]any{
		"kind": record.Kind, "git_ref": record.GitRef, "git_commit": record.GitCommit,
	})
	// Emitted as soon as the launch is recorded, before the enqueues that follow: those can
	// fail without the launch being undone, and a fact waiting for their success would miss
	// precisely the launches that went wrong.
	h.emit(r, event.ReleaseCreatedAt(
		now, project.ID, record.ID, record.Tag, record.Kind, record.GitRef, versionLine(parsed)))

	response := map[string]any{}
	if spec.Mode == release.ModeBuild {
		// Never from a branch when the commit is known: rebuilding the same release six
		// months later must produce the same image.
		started := 0
		if h.plans != nil {
			started = h.plans.OpenReleasePlan(r.Context(), project, record, buildReference(record, record.GitRef))
		}
		if started == 0 {
			// Nothing left: the release says so by failing, and "Rebuild" relaunches it. Left
			// `building`, it would wait for a pipeline that never comes.
			warnings = append(warnings, "the release is launched but its plan could not be opened")
			if err := h.store.SetReleaseState(r.Context(), record.ID, string(release.StateFailed)); err == nil {
				record.State = string(release.StateFailed)
			}
		} else {
			response["build_jobs"] = started
		}
	}

	// A final release must exist under its own tag in the registry, not only under its
	// candidate's. Without that re-tag, the final tag names nothing: no image policy can
	// select it, and the release is promotable nowhere although its image is ready.
	if record.ImageVersion != record.Tag {
		jobIDs, err := h.enqueueRetag(r.Context(), project.ID, record.ID, record, identity.Subject)
		if err != nil {
			// The release is launched: say so, rather than suggest a failure. The re-tag can be
			// run again.
			warnings = append(warnings,
				"the release is launched but the re-tag to its own tag could not be queued")
		} else {
			response["retag_job_ids"] = jobIDs
		}
	}

	response["release"] = toReleasePayload(record, sourceTag, h.actorsFor(r.Context()))
	response["warnings"] = warnings
	writeJSON(w, http.StatusOK, response)
}

type updateDraftRequest struct {
	GitRef string `json:"git_ref"`
}

// UpdateDraft changes the Git reference a draft will build. The tag and the kind do not
// change: deleting the draft and creating another says the same thing more plainly.
func (h *ReleasesHandler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	var request updateDraftRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	record, ok := h.draft(w, r, project)
	if !ok {
		return
	}
	if spec, err := project.ReleaseKinds.Find(record.Kind); err == nil && spec.Mode != release.ModeBuild {
		writeProblem(w, http.StatusConflict, "Nothing to edit",
			fmt.Sprintf("a %q release takes its source's image: it names no Git reference", spec.ID))
		return
	}

	record.GitRef = strings.TrimSpace(request.GitRef)
	switch err := h.store.UpdateDraftRelease(r.Context(), record.ID, record.GitRef); {
	case errors.Is(err, store.ErrNotADraft):
		writeProblem(w, http.StatusConflict, "Not a draft",
			"the release was launched or deleted in the meantime, reload the page")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Cannot save", "")
		return
	}
	h.audit(r, store.AuditReleaseDraftUpdate, record.Tag, map[string]any{"git_ref": record.GitRef})
	writeJSON(w, http.StatusOK, map[string]any{
		"release": toReleasePayload(record, "", h.actorsFor(r.Context())),
	})
}

// DeleteDraft deletes a draft nobody will launch, which frees its tag. Its
// `release.drafted` fact stays in the history: a fact is not withdrawn, and the audit says
// what became of it.
func (h *ReleasesHandler) DeleteDraft(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	record, ok := h.draft(w, r, project)
	if !ok {
		return
	}
	if _, ok := h.kindAllowed(w, identity, project, record); !ok {
		return
	}

	switch err := h.store.DeleteDraftRelease(r.Context(), record.ID); {
	case errors.Is(err, store.ErrNotADraft):
		writeProblem(w, http.StatusConflict, "Not a draft",
			"the release was launched or deleted in the meantime, reload the page")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Cannot delete", "")
		return
	}
	h.audit(r, store.AuditReleaseDraftDelete, record.Tag, map[string]any{"kind": record.Kind})
	w.WriteHeader(http.StatusNoContent)
}

// DetectNotes detects a draft's note again. The detection only adds: what was linked stays,
// and what someone removed is set aside.
func (h *ReleasesHandler) DetectNotes(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	record, ok := h.draft(w, r, project)
	if !ok {
		return
	}
	if err := h.enqueueNoteDetection(r.Context(), record.ID, identity.Subject); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot queue the detection", "")
		return
	}
	h.audit(r, store.AuditReleaseNoteDetect, record.Tag, nil)
	w.WriteHeader(http.StatusAccepted)
}

// Release approves a release and allows it to go to an environment.
//
// This is the human decision of the documented cycle: the tests concluded, someone
// settles it. Nothing automates it, and that is the point — a green pipeline says the
// code works, not that anyone wants to ship it.
//
// `release:create:final` and not `release:edit`: approving commits what will reach the
// user, as much as producing a final release does.
func (h *ReleasesHandler) Release(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	tag := r.PathValue("tag")
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	record, err := h.store.GetRelease(r.Context(), currentID, tag)
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", tag)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	// The transition is checked before the write, and by the package that owns its table:
	// approving from `building` or from `cancelled` makes no sense, and the message must
	// say which of the two applies.
	from := release.State(record.State)
	if !release.CanTransition(from, release.StateReleased) {
		writeProblem(w, http.StatusConflict, "Cannot validate",
			fmt.Sprintf("release %q is in state %q: only a %q release is validated",
				tag, record.State, release.StateReady))
		return
	}

	validated, err := h.store.MarkReleased(r.Context(), record.ID, record.State)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot validate", "")
		return
	}
	if !validated {
		// The release moved between the read and the write. A 409 rather than a silent
		// success: the caller made their decision on a state that is no longer the database's.
		writeProblem(w, http.StatusConflict, "Cannot validate",
			"the release's state changed in the meantime, reload the page")
		return
	}

	h.audit(r, store.AuditReleaseRelease, tag, map[string]any{
		"from": record.State, "image_version": record.ImageVersion,
	})
	// After the conditional write: it is what makes the approval unique, and the fact
	// inherits that. A second attempt receives a 409 and emits nothing.
	h.emit(r, event.ReleaseReleasedAt(
		time.Now(), currentID, record.ID, tag, identity.Subject))

	record.State = string(release.StateReleased)
	writeJSON(w, http.StatusOK, map[string]any{
		"release": toReleasePayload(record, "", h.actorsFor(r.Context())),
	})
}

// Rebuild relaunches the build of a release's images.
//
// It serves after a failure whose cause is outside the code — a badly declared deploy
// key, an unreachable registry, a saturated node. Creating one more candidate to work
// around an infrastructure incident would make the version number carry the history of
// the cluster's outages.
//
// The git reference is not asked for again: it is the release's, decided at its
// creation. Reopening it would allow rebuilding a given tag from another commit, and
// two images would then carry the same number without being the same thing.
func (h *ReleasesHandler) Rebuild(w http.ResponseWriter, r *http.Request) {
	if _, ok := IdentityFrom(r.Context()); !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	tag := r.PathValue("tag")
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	record, err := h.store.GetRelease(r.Context(), currentID, tag)
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", tag)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	// A release in re-tag mode has nothing to build: its image is its source's. The rule
	// is on the mode and not on a kind's name — that is how it holds for any kind
	// declaring `retag`, whatever the project's vocabulary.
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	if spec, err := project.ReleaseKinds.Find(record.Kind); err == nil &&
		spec.Mode == release.ModeRetag {
		writeProblem(w, http.StatusConflict, "Cannot rebuild",
			fmt.Sprintf("a %q release takes its source's image: rebuilding it "+
				"would produce an image nobody has tested", spec.ID))
		return
	}

	if record.State == string(release.StateDraft) {
		writeProblem(w, http.StatusConflict, "Cannot rebuild",
			fmt.Sprintf("release %q is a draft: launching it builds it", tag))
		return
	}
	from := release.State(record.State)
	if !release.CanTransition(from, release.StateBuilding) {
		writeProblem(w, http.StatusConflict, "Cannot rebuild",
			fmt.Sprintf("release %q is in state %q, from which no rebuild is allowed",
				tag, record.State))
		return
	}

	// The rebuild opens the same plan as the creation, and that is necessary: enqueueing
	// outside a plan would leave the release building for ever, since it is the stage
	// settling that advances it now.
	project, projectErr := h.store.GetProject(r.Context(), currentID)
	if projectErr != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot rebuild", "")
		return
	}
	started := 0
	if h.plans != nil {
		started = h.plans.OpenReleasePlan(r.Context(), project, record, record.GitRef)
	}
	if started == 0 {
		writeProblem(w, http.StatusUnprocessableEntity, "Cannot rebuild",
			"no build could be queued")
		return
	}

	// The state follows the enqueue, as at creation: `building` means "build requested".
	// The write is conditioned on the state that was read — two simultaneous relaunches
	// must not produce two runs.
	advanced, err := h.store.AdvanceReleaseState(r.Context(), record.ID, record.State, string(release.StateBuilding))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot rebuild", "")
		return
	}
	if !advanced {
		writeProblem(w, http.StatusConflict, "Cannot rebuild",
			"the release's state changed in the meantime, reload the page")
		return
	}

	h.audit(r, store.AuditReleaseRebuild, tag, map[string]any{
		"from": record.State, "git_ref": record.GitRef, "jobs": started,
	})

	record.State = string(release.StateBuilding)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"release": toReleasePayload(record, "", h.actorsFor(r.Context())), "build_jobs": started,
	})
}

// verifiable says whether a family can be re-run on its own.
//
// The catalogue settles it, and being in it is not enough: `build` and `retag` are in
// it too, and a rebuild disguised as a test re-run would do far more than the user
// asked. The `verification` flag is what distinguishes them.
//
// That could not be inferred while the flag did not exist, hence a hard-coded list
// here, twin to the scheduler's and kept aligned by a test. Two lists that diverge
// give a button that launches what the chain does not, or a chained family that cannot
// be caught up without rebuilding the whole release.
func (h *ReleasesHandler) verifiable(kind string) bool {
	return h.catalog != nil && catalog.Of(h.catalog).IsVerification(kind)
}

// Verify re-runs a single verification family on a release.
//
// The gesture was missing: a verification that failed for an outside cause — an
// unreachable registry, a saturated node, a momentarily inaccessible repository —
// required nothing less than a complete rebuild, which republishes the images and
// replays every verification. Replaying the one that failed costs a fraction of that
// and does not touch the artifact.
//
// The release's state is not modified. A re-run says nothing new about the release
// until it has concluded, and dropping it back to `verifying` from `ready` would undo
// a decision already taken on the other families.
func (h *ReleasesHandler) Verify(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	tag := r.PathValue("tag")
	kind := r.PathValue("kind")
	if !h.verifiable(kind) {
		writeProblem(w, http.StatusBadRequest, "Unknown kind",
			fmt.Sprintf("%q is not a re-runnable verification kind", kind))
		return
	}

	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	record, err := h.store.GetRelease(r.Context(), currentID, tag)
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", tag)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	if record.State == string(release.StateDraft) {
		writeProblem(w, http.StatusConflict, "Cannot rerun",
			fmt.Sprintf("release %q is a draft: nothing is built to verify yet", tag))
		return
	}

	// The revision verified is the commit that was built, never the branch it came from:
	// that may have moved, and the tests would then be about something other than the
	// image.
	revision := record.GitCommit
	if revision == "" {
		revision = record.GitRef
	}
	if revision == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Cannot rerun",
			"this release names no Git reference: there is nothing to verify")
		return
	}

	pipeline, found := catalog.Of(h.catalog).Find(kind)
	if !found {
		writeProblem(w, http.StatusUnprocessableEntity, "Cannot rerun",
			fmt.Sprintf("the catalogue declares no %q pipeline", kind))
		return
	}

	params, missing := pipeline.VerificationInputs(revision, record.Tag, h.registryProject)
	if missing != "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Cannot rerun",
			fmt.Sprintf("parameter %q cannot be set for this release", missing))
		return
	}

	jobID, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind:        pipeline.Kind,
		Params:      params,
		RequestedBy: identity.Subject,
		ReleaseID:   &record.ID,
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot rerun", "")
		return
	}

	h.audit(r, store.AuditReleaseVerify, tag, map[string]any{
		"kind": kind, "revision": revision, "job": jobID,
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"kind": kind, "job_id": jobID, "revision": revision,
	})
}

func imageReference(registryProject, name, tag string) string {
	return imagenames.Reference(registryProject, name, tag)
}

// notePayload is one line of a release note.
type notePayload struct {
	// ExternalID is the issue's identifier in the tracker, as text: the same name and the
	// same shape as on a version.
	ExternalID string `json:"external_id"`
	Subject    string `json:"subject,omitempty"`
	Tracker    string `json:"tracker,omitempty"`
	Status     string `json:"status,omitempty"`
	// Anomaly files the line: anomalies go to the fixes, the rest to the new features. The
	// tracker and not the status: the nature of the issue decides, not its progress.
	Anomaly bool `json:"anomaly"`
	// Known is false while tracker synchronisation has not seen the issue. The line stays
	// displayed: hiding it would make a manual addition disappear.
	Known       bool   `json:"known"`
	Source      string `json:"source"`
	PullRequest string `json:"pull_request,omitempty"`
	AddedBy     string `json:"added_by"`
	// URL to the issue in the tracker. Empty when no tracker is configured: a dead link is
	// worth less than no link.
	URL string `json:"url,omitempty"`
}

// Notes returns a release's note.
func (h *ReleasesHandler) Notes(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	record, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	entries, err := h.store.ListNoteEntries(r.Context(), record.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the note", "")
		return
	}

	who := h.actorsFor(r.Context())
	tracker := trackerOf(r.Context(), h.trackers, currentID)

	payload := make([]notePayload, 0, len(entries))
	linked := make(map[string]bool, len(entries))
	for _, entry := range entries {
		linked[entry.ExternalID] = true
		payload = append(payload, notePayload{
			ExternalID: entry.ExternalID, Subject: entry.Subject, Tracker: entry.Tracker,
			Status: entry.StatusName, Anomaly: entry.Anomaly, Known: entry.Known,
			Source: entry.Source, PullRequest: entry.PullRequest,
			AddedBy: who.name(entry.AddedBy),
			URL:     tracker.issueURL(entry.ExternalID),
		})
	}

	// The pool to complete the note by hand: the version's issues, minus those already
	// linked. Returned with the note rather than through a second call — the two are read
	// together, and the screen need not compose a difference the server already knows.
	//
	// A failed read does not prevent displaying the note — linking by number stays
	// possible — but it is said. Hiding it made an empty list indistinguishable from a
	// version with no issues, and the screen then announced that everything was already
	// linked when it had been unable to read anything. Same rule as the images error:
	// absence and an unreadable source do not look alike.
	available := make([]notePayload, 0)
	var availableError string

	issues, err := h.store.ListReleaseVersionIssues(r.Context(), record.ID)
	if err != nil {
		availableError = "The version's requests could not be read."
		h.logger.Error("requests of a release's version",
			"release", record.Tag, "error", err)
	}
	for _, issue := range issues {
		if linked[issue.ExternalID] {
			continue
		}
		available = append(available, notePayload{
			ExternalID: issue.ExternalID, Subject: issue.Subject, Tracker: issue.Tracker,
			Status: issue.StatusName, Anomaly: issue.Anomaly, Known: true,
			URL: tracker.issueURL(issue.ExternalID),
		})
	}

	response := map[string]any{"notes": payload, "available": available}
	if availableError != "" {
		response["available_error"] = availableError
	}
	writeJSON(w, http.StatusOK, response)
}

// issueReference reads the issue identifier the URL carries.
//
// The identifier comes from the tracker and the dashboard does not make it up: one
// tracker numbers, another writes `MSE-1234`. An integer therefore cannot be required
// — but what resembles no identifier at all is refused, so the mistake shows here
// rather than in the database as an orphaned row.
var issueReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func issueReference(w http.ResponseWriter, r *http.Request) (string, bool) {
	reference := r.PathValue("issue")
	if !issueReferencePattern.MatchString(reference) {
		writeProblem(w, http.StatusBadRequest, "Invalid request",
			"the request identifier is empty or carries forbidden characters")
		return "", false
	}
	return reference, true
}

// AddNote links an issue by hand.
//
// The gesture exists because detection cannot see everything: a pull request whose
// title does not follow the convention, an issue reviewed after the release was
// created. Without it, the only way out would be to recreate the release.
func (h *ReleasesHandler) AddNote(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	record, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	reference, ok := issueReference(w, r)
	if !ok {
		return
	}

	switch err := h.store.LinkNoteIssue(r.Context(), record.ID, reference,
		store.NoteSourceManual, "", identity.Subject); {
	case errors.Is(err, store.ErrNoteAlreadyLinked):
		writeProblem(w, http.StatusConflict, "Request already attached",
			fmt.Sprintf("request %s already appears in the note of %s", reference, record.Tag))
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Cannot attach", "")
		return
	}

	h.audit(r, store.AuditReleaseNoteAdd, record.Tag, map[string]any{"issue": reference})
	writeJSON(w, http.StatusCreated, map[string]any{"issue": reference})
}

// RemoveNote removes an issue from the note.
func (h *ReleasesHandler) RemoveNote(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	record, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	reference, ok := issueReference(w, r)
	if !ok {
		return
	}

	if err := h.store.UnlinkNoteIssue(r.Context(), record.ID, reference, identity.Subject); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot remove", "")
		return
	}

	h.audit(r, store.AuditReleaseNoteRemove, record.Tag, map[string]any{"issue": reference})
	w.WriteHeader(http.StatusNoContent)
}

// anchorCommit resolves a reference into a commit, and says why it did not.
//
// A release naming a branch is not reproducible: the branch moves, and rebuilding
// would produce a different image. Resolving at creation freezes the starting point,
// even when the tag cannot be set.
func (h *ReleasesHandler) anchorCommit(ctx context.Context, projectID int64, ref string) (string, []string) {
	if strings.TrimSpace(ref) == "" {
		return "", []string{"no Git reference supplied: the release is attached to no commit"}
	}
	var forge CommitResolver
	if h.forges != nil {
		resolved, err := h.forges.Forge(ctx, projectID)
		switch {
		case err == nil:
			forge = resolved
		case !notConfigured(err):
			return "", []string{"forge unreachable: the release keeps the reference " + ref +
				" without resolving it to a commit: " + err.Error()}
		}
	}
	if forge == nil {
		return "", []string{
			"forge not configured: the release keeps the reference " + ref +
				" without resolving it to a commit, and is therefore not reproducible",
		}
	}

	commit, err := forge.ResolveCommit(ctx, ref)
	if err != nil {
		return "", []string{"reference " + ref + " could not be resolved to a commit: " + err.Error()}
	}
	return commit, nil
}

// tagRelease sets the release's git tag on its commit.
//
// This is the segment the dashboard takes over and which used to be manual. A failure
// is a caveat and not a refusal: the tag can be set again, whereas a refused release
// forces starting over.
func (h *ReleasesHandler) tagRelease(ctx context.Context, projectID int64, tag, commit string) (bool, []string) {
	var tagger Tagger
	if h.forges != nil {
		if writer, err := h.forges.TagWriter(ctx, projectID); err == nil {
			tagger = writer
		}
	}
	if tagger == nil {
		return false, []string{
			"no token able to write to the forge: tag " + tag +
				" was not set, the release stays identified by its commit",
		}
	}

	err := tagger.CreateTag(ctx, tag, commit, "Release "+tag)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, provider.ErrTagExists):
		// Setting a tag again after a retry is normal. It already names what it should, and
		// reporting it is enough.
		return true, []string{"tag " + tag + " already existed on the forge and was not modified"}
	default:
		return false, []string{"tag " + tag + " could not be set: " + err.Error()}
	}
}

// buildReference chooses what the build will clone.
//
// The commit first, not the tag. Building from `refs/tags/<tag>` is what used to be
// done here: the clone succeeded, then tag generation failed on a pathspec that did
// not match. The cause is that the clone Task works at `--depth=1`: a shallow clone
// positions the tree on the requested revision but does not create the tag's local
// reference, and the checkout that follows finds nothing. A SHA is always present —
// it is HEAD.
//
// The tag stays set on the forge, where it serves humans, and the version keeps
// carrying it through to the image: the invariant is intact, only the way of naming
// the starting point changes.
//
// Order: the resolved commit, then the typed reference for want of better. The latter
// is not immutable and should serve only when the forge is unreachable.
func buildReference(record store.Release, requested string) string {
	if record.GitCommit != "" {
		return record.GitCommit
	}
	return requested
}

// enqueueRetag queues the re-tag of a release towards its own tag.
//
// One run per promoted image — the development image is not one: no environment of the
// promotion flow deploys it.
func (h *ReleasesHandler) enqueueRetag(
	ctx context.Context, projectID, releaseID int64, record store.Release, subject string,
) ([]int64, error) {
	pipeline, found := catalog.Of(h.catalog).Find(retagJobKind)
	if !found {
		return nil, errors.New("catalogue with no re-tag pipeline")
	}

	project, err := h.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}

	targets := project.Promotable()
	jobs := make([]int64, 0, len(targets))
	for _, target := range targets {
		jobID, err := h.store.Enqueue(ctx, store.JobRequest{
			Kind: pipeline.Kind,
			Params: map[string]string{
				"image-name": target.Image,
				"old-tag":    record.ImageVersion,
				"new-tag":    record.Tag,
			},
			RequestedBy: subject,
			ReleaseID:   &releaseID,
		})
		if err != nil {
			return jobs, fmt.Errorf("re-tag of %s: %w", target.Image, err)
		}
		jobs = append(jobs, jobID)
	}
	return jobs, nil
}

// retagJobKind is the job kind that sets an additional tag.
//
// It used to be called `promote`, which described the intent rather than the act: the
// pipeline promotes nothing, it re-tags, and the dashboard also uses it outside
// promotion — to give a final release its own tag from its source candidate's.
const retagJobKind = catalog.KindRetag

type manualActionsRequest struct {
	Markdown string `json:"markdown"`
}

// UpdateActions records the account of a delivery's manual steps.
//
// A free-text field and not a list of checkboxes: a delivery's manual steps do not
// repeat enough from one version to the next for a checklist to cover them, and an
// incomplete checklist gets filled in anyway — it would suggest a control that did not
// happen.
func (h *ReleasesHandler) UpdateActions(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	var request manualActionsRequest
	// 256 KiB: the text is free, but it ends up in a delivery document.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	tag := r.PathValue("tag")
	if err := h.store.SetManualActions(r.Context(), tag, request.Markdown, identity.Subject); err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot save", "")
		return
	}

	h.audit(r, store.AuditReleaseActions, tag, map[string]any{"length": len(request.Markdown)})

	// The release is read back rather than recomposed: the timestamp is set by the
	// database, and returning it from a value computed here would let the two diverge.
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	item, err := h.store.GetRelease(r.Context(), currentID, tag)
	if err != nil {
		writeJSON(w, http.StatusNoContent, nil)
		return
	}
	writeJSON(w, http.StatusOK, manualActionVw{
		Markdown:  item.ManualActions,
		UpdatedBy: h.actorsFor(r.Context()).name(item.ManualActionsUpdatedBy),
		UpdatedAt: item.ManualActionsUpdatedAt,
	})
}

// audit records an outgoing action, without ever failing it.
//
// The log is a record, not a transaction: refusing a promotion because recording it
// failed would be an availability regression for no gain — the promotion did happen.
// The failure goes to the application log.
func (h *ReleasesHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}

// emit records a fact without failing the request that produced it.
//
// The user's gesture succeeded: the release is created, or approved. Returning an error
// because the fact could not be written would suggest the action failed, and invite
// redoing it — on a release that already exists.
//
// There is no safety net here, unlike in the worker: the observer redelivers finished
// runs and therefore re-emits what it lost, whereas a human gesture is not replayed. A
// lost fact is lost for good. Acceptable while nothing reacts to it; to be handled when
// the engine arrives.
func (h *ReleasesHandler) emit(r *http.Request, fact event.Event) {
	recorded, err := h.store.RecordEvent(r.Context(), fact)
	if err != nil {
		slog.Default().Error("emitting a fact",
			"type", fact.Type, "subject", fact.Subject, "err", err)
		return
	}
	if !recorded {
		slog.Default().Debug("fact already known, not re-emitted",
			"type", fact.Type, "subject", fact.Subject)
	}
}

// isDuplicate recognises PostgreSQL's uniqueness violation.
//
// The code rather than the message: the latter is localised by the server, and matching
// English text would break on a database configured otherwise.
func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
