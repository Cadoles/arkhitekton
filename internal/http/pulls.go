package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * A project's pull requests, as the worker follows them from the forge.
 *
 * A pull request is a subject like a release: pipelines are launched on its head, its results and
 * the plans its facts opened show on its page. A launch fills what the head determines (the
 * revision, the request's tag, the images built under it) and asks for the rest.
 */

// PullStore is what the pull request routes need from the database.
type PullStore interface {
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	ListPullRequests(ctx context.Context, projectID int64, state string, limit int) ([]store.PullRequest, error)
	PullRequestByNumber(ctx context.Context, projectID, number int64) (store.PullRequest, error)
	ListPullRequestJobs(ctx context.Context, pullRequestID int64) ([]store.ReleaseJob, error)
	ListPullRequestPlanRuns(ctx context.Context, pullRequestID int64, limit int) ([]store.PlanRunSummary, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	DirectoryReader
}

// PullsHandler serves a project's pull requests.
type PullsHandler struct {
	store   PullStore
	catalog catalog.Source
	// registry is the registry project a request's images are named in, as a release's are.
	registry string
	// results serves the requests' results. Nil: only the requests and their jobs are served.
	results *TestsHandler
	logger  *slog.Logger
}

func NewPullsHandler(pulls PullStore, pipelines catalog.Source, registryProject string, logger *slog.Logger) *PullsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PullsHandler{store: pulls, catalog: pipelines, registry: registryProject, logger: logger}
}

type pullPayload struct {
	Number       int64      `json:"number"`
	Title        string     `json:"title"`
	Author       string     `json:"author"`
	SourceBranch string     `json:"source_branch"`
	TargetBranch string     `json:"target_branch"`
	HeadSHA      string     `json:"head_sha"`
	State        string     `json:"state"`
	URL          string     `json:"url,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
	// Tag is what the request's head is built under.
	Tag string `json:"tag"`
}

func pullPayloadOf(pull store.PullRequest) pullPayload {
	return pullPayload{
		Number: pull.Number, Title: pull.Title, Author: pull.Author, SourceBranch: pull.SourceBranch,
		TargetBranch: pull.TargetBranch, HeadSHA: pull.HeadSHA, State: pull.State, URL: pull.URL,
		CreatedAt: pull.CreatedAt, UpdatedAt: pull.UpdatedAt, ClosedAt: pull.ClosedAt, MergedAt: pull.MergedAt,
		Tag: pull.Tag(),
	}
}

// List returns a project's pull requests: `state=open`, `state=closed` (closed or merged), or all.
func (h *PullsHandler) List(w http.ResponseWriter, r *http.Request) {
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	state := r.URL.Query().Get("state")
	if state != store.PullRequestsOpen && state != store.PullRequestsClosed {
		state = ""
	}
	pulls, err := h.store.ListPullRequests(r.Context(), project.ID, state, 0)
	if err != nil {
		h.logger.Error("pull requests", "project", project.Slug, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot read the pull requests", "")
		return
	}
	payload := make([]pullPayload, 0, len(pulls))
	for _, pull := range pulls {
		payload = append(payload, pullPayloadOf(pull))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pulls": payload, "project": project.Slug})
}

// Get returns a pull request, the jobs launched on it and the plans its facts opened.
func (h *PullsHandler) Get(w http.ResponseWriter, r *http.Request) {
	project, pull, ok := h.pull(w, r)
	if !ok {
		return
	}
	jobs, err := h.store.ListPullRequestJobs(r.Context(), pull.ID)
	if err != nil {
		h.logger.Error("jobs of a pull request", "project", project.Slug, "pull", pull.Number, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot read the jobs", "")
		return
	}
	runs, err := h.store.ListPullRequestPlanRuns(r.Context(), pull.ID, 0)
	if err != nil {
		h.logger.Error("plans of a pull request", "project", project.Slug, "pull", pull.Number, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot read the plans", "")
		return
	}

	who := resolveActors(r.Context(), h.store, h.logger)
	jobViews := make([]releaseJobPayload, 0, len(jobs))
	for _, job := range jobs {
		label, role := jobRoleIn(catalog.Of(h.catalog), job.Kind)
		jobViews = append(jobViews, releaseJobPayload{
			ID: job.ID, Kind: job.Kind, Label: label, Role: role, State: job.State,
			RunName: job.RunName, RequestedBy: who.name(job.RequestedBy),
			RequestedAt: job.RequestedAt, FinishedAt: job.FinishedAt,
		})
	}
	runViews := make([]planRunPayload, 0, len(runs))
	for _, run := range runs {
		runViews = append(runViews, toPlanRunPayload(run, false))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pull": pullPayloadOf(pull), "project": project.Slug, "jobs": jobViews, "plan_runs": runViews,
	})
}

type pullLaunchRequest struct {
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
}

// Launch queues a pipeline on a pull request's head.
//
// What the head determines is filled here and cannot be overridden: the revision, the request's
// tag, the images built under it. The request carries the rest (an image's name and target for a
// build), and what is still missing is refused with its name rather than failing at scheduling.
func (h *PullsHandler) Launch(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}
	project, pull, ok := h.pull(w, r)
	if !ok {
		return
	}
	var request pullLaunchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	pipeline, found := catalog.Of(h.catalog).Find(request.Kind)
	if !found {
		writeProblem(w, http.StatusBadRequest, "Unknown run type", request.Kind)
		return
	}
	if pull.HeadSHA == "" {
		writeProblem(w, http.StatusConflict, "No head commit",
			"the forge gave no commit for this pull request: nothing can be built")
		return
	}
	if len(request.Params) > maxLaunchParams {
		writeProblem(w, http.StatusBadRequest, "Too many parameters", strconv.Itoa(maxLaunchParams)+" at most")
		return
	}

	tag := pull.Tag()
	params := map[string]string{}
	for name, value := range request.Params {
		if !paramName.MatchString(name) {
			writeProblem(w, http.StatusBadRequest, "Invalid parameter name", name)
			return
		}
		params[name] = value
	}
	for _, input := range pipeline.Inputs() {
		if value := input.FromContext(pull.HeadSHA, tag, h.registry); value != "" {
			params[input.Name] = value
		}
	}
	for _, name := range pipeline.RequiredInputs() {
		if params[name] == "" {
			writeProblem(w, http.StatusBadRequest, "Missing required parameter", name)
			return
		}
	}

	projectID, pullID := project.ID, pull.ID
	id, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind: pipeline.Kind, Params: params, RequestedBy: identity.Subject,
		ProjectID: &projectID, PullRequestID: &pullID,
	})
	if err != nil {
		h.logger.Error("queueing on a pull request", "project", project.Slug, "pull", pull.Number, "error", err)
		writeProblem(w, http.StatusInternalServerError, "Cannot enqueue", "")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "kind": pipeline.Kind, "params": params})
}

// pull resolves the project and the pull request a path names.
func (h *PullsHandler) pull(w http.ResponseWriter, r *http.Request) (store.Project, store.PullRequest, bool) {
	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return store.Project{}, store.PullRequest{}, false
	}
	pull, ok := resolvePull(w, r, h.store, project.ID)
	return project, pull, ok
}

// pullReader reads one pull request.
type pullReader interface {
	PullRequestByNumber(ctx context.Context, projectID, number int64) (store.PullRequest, error)
}

func resolvePull(w http.ResponseWriter, r *http.Request, source pullReader, projectID int64) (store.PullRequest, bool) {
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		writeProblem(w, http.StatusBadRequest, "Invalid pull request number", "")
		return store.PullRequest{}, false
	}
	pull, err := source.PullRequestByNumber(r.Context(), projectID, number)
	if errors.Is(err, store.ErrPullRequestNotFound) {
		writeProblem(w, http.StatusNotFound, "Pull request not found", "")
		return store.PullRequest{}, false
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the pull request", "")
		return store.PullRequest{}, false
	}
	return pull, true
}

// jobRoleIn names a job's pipeline and says what it does: `build`, `verification` or nothing.
func jobRoleIn(pipelines *catalog.Catalog, kind string) (label, role string) {
	if pipelines == nil {
		return "", ""
	}
	if pipeline, found := pipelines.Find(kind); found {
		label, kind = pipeline.Label, pipeline.Kind
	}
	switch {
	case kind == catalog.KindBuild:
		role = "build"
	case pipelines.IsVerification(kind):
		role = "verification"
	}
	return label, role
}

// WithResults serves the pull requests' results through the handler that serves releases'.
func (h *PullsHandler) WithResults(tests *TestsHandler) *PullsHandler {
	h.results = tests
	return h
}

// Routes mounts the pull request routes: readable by all like a release, launching under
// `pipeline:run` like the queue.
func (h *PullsHandler) Routes(mux *http.ServeMux, m *Middleware) {
	read := func(handler http.HandlerFunc) http.Handler {
		return Chain(handler, m.Authenticate, m.RequireIn(auth.PermRead))
	}
	mux.Handle("GET /projects/{project}/pulls", read(h.List))
	mux.Handle("GET /projects/{project}/pulls/{number}", read(h.Get))
	mux.Handle("POST /projects/{project}/pulls/{number}/launch", Chain(http.HandlerFunc(h.Launch),
		m.Authenticate, m.CSRF, m.RequireIn(auth.PermPipelineRun)))
	if h.results == nil {
		return
	}
	mux.Handle("GET /projects/{project}/pulls/{number}/tests", read(h.results.PullTests))
	mux.Handle("GET /projects/{project}/pulls/{number}/tests/{group}", read(h.results.PullGroup))
	mux.Handle("GET /projects/{project}/pulls/{number}/security", read(h.results.PullSecurity))
	mux.Handle("GET /projects/{project}/pulls/{number}/attachments/{id}", read(h.results.PullAttachment))
}
