package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// pullFixture is a project with two pull requests and what ran on the first one.
type pullFixture struct {
	jobs     []store.JobRequest
	listed   string
	results  []store.TestResult
	attached store.ResultAttachment
	failed   *store.ReleaseJob
}

var openPull = store.PullRequest{
	ID: 9, ProjectID: 7, Number: 12, Title: "Feature", State: "open", HeadSHA: "abcdef0123456789",
	SourceBranch: "feature", TargetBranch: "main",
}

func (f *pullFixture) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "shop" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 7, Slug: "shop"}, nil
}

func (f *pullFixture) BuildableProject(context.Context) (store.Project, error) {
	return store.Project{ID: 7, Slug: "shop"}, nil
}

func (f *pullFixture) ListPullRequests(_ context.Context, _ int64, state string, _ int) ([]store.PullRequest, error) {
	f.listed = state
	return []store.PullRequest{openPull}, nil
}

func (f *pullFixture) PullRequestByNumber(_ context.Context, projectID, number int64) (store.PullRequest, error) {
	if projectID != 7 || number != 12 {
		return store.PullRequest{}, store.ErrPullRequestNotFound
	}
	return openPull, nil
}

// PullRequestImages are what the request's build published.
func (f *pullFixture) PullRequestImages(context.Context, int64) ([]store.ReleaseImage, error) {
	return []store.ReleaseImage{{Name: "portal", Reference: "reg.example/team/portal:pr-12-abcdef0"}}, nil
}

func (f *pullFixture) ListPullRequestJobs(context.Context, int64) ([]store.ReleaseJob, error) {
	jobs := []store.ReleaseJob{{ID: 3, Kind: "docker/build", State: "succeeded", RequestedBy: "system:plan"}}
	if f.failed != nil {
		jobs = append(jobs, *f.failed)
	}
	return jobs, nil
}

func (f *pullFixture) ListPullRequestPlanRuns(context.Context, int64, int) ([]store.PlanRunSummary, error) {
	return nil, nil
}

func (f *pullFixture) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	f.jobs = append(f.jobs, request)
	return int64(len(f.jobs)), nil
}

func (f *pullFixture) Directory(context.Context) (map[string]string, error) { return nil, nil }

func (f *pullFixture) ListTestResults(_ context.Context, scope store.Scope) ([]store.TestResult, error) {
	if scope != store.PullRequestScope(9) {
		return nil, nil
	}
	return f.results, nil
}

func (f *pullFixture) LatestSecurityAuditIn(context.Context, store.Scope) (store.SecurityAudit, error) {
	return store.SecurityAudit{}, store.ErrAuditNotFound
}

func (f *pullFixture) ListLoadResults(context.Context, store.Scope) ([]store.LoadResult, error) {
	return nil, nil
}

func (f *pullFixture) DiffLatestRuns(context.Context, store.Scope, string) ([]store.CaseDiff, error) {
	return nil, store.ErrNothingToCompare
}

func pullCatalogue() *catalog.Catalog {
	input := func(required bool, suggest string) catalog.Param {
		return catalog.Param{Source: catalog.SourceInput, Required: required, Suggest: suggest}
	}
	portal := input(true, catalog.SuggestImage)
	portal.Image = "portal"
	return &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "docker/build", PipelineRef: "build-image", Params: map[string]catalog.Param{
			"revision": input(true, catalog.SuggestGitRef), "tag-name": input(true, catalog.SuggestReleaseTag),
			"image-name": input(true, catalog.SuggestImageName),
		}},
		{Kind: "review/pull-request", PipelineRef: "review", Params: map[string]catalog.Param{
			"number": input(true, catalog.SuggestPullRequest),
		}},
		{Kind: "mstore/integration-tests", PipelineRef: "integration", Verification: true, Params: map[string]catalog.Param{
			"mstore-revision": input(true, catalog.SuggestGitRef), "portal-image": portal,
		}},
	}}
}

func pullRouter(fixture *pullFixture) http.Handler {
	mux := http.NewServeMux()
	handler := NewPullsHandler(fixture, pullCatalogue(), quietLogger())
	mux.HandleFunc("GET /projects/{project}/pulls", handler.List)
	mux.HandleFunc("GET /projects/{project}/pulls/{number}", handler.Get)
	mux.HandleFunc("POST /projects/{project}/pulls/{number}/launch", handler.Launch)
	return mux
}

func TestPullRequestsAreListedByState(t *testing.T) {
	fixture := &pullFixture{}
	recorder := httptest.NewRecorder()
	pullRouter(fixture).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/projects/shop/pulls?state=closed", nil))
	if recorder.Code != http.StatusOK || fixture.listed != store.PullRequestsClosed {
		t.Fatalf("status %d, listed %q: %s", recorder.Code, fixture.listed, recorder.Body)
	}
	var body struct {
		Pulls []pullPayload `json:"pulls"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || len(body.Pulls) != 1 || body.Pulls[0].Tag != "pr-12-abcdef0" {
		t.Errorf("body = %s, %v", recorder.Body, err)
	}
}

// A launch fills what the head decides, whatever the request says, and asks for the rest.
func TestALaunchOnAPullRequestRunsOnItsHead(t *testing.T) {
	fixture := &pullFixture{}
	launch := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		pullRouter(fixture).ServeHTTP(recorder, asAdmin(httptest.NewRequest(http.MethodPost,
			"/projects/shop/pulls/12/launch", strings.NewReader(body))))
		return recorder
	}

	if recorder := launch(`{"kind":"docker/build","params":{"revision":"main"}}`); recorder.Code != http.StatusBadRequest ||
		!strings.Contains(recorder.Body.String(), "image-name") {
		t.Errorf("a build without its image name: %d %s", recorder.Code, recorder.Body)
	}
	if recorder := launch(`{"kind":"docker/build","params":{"revision":"main","image-name":"portal"}}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("build: %d %s", recorder.Code, recorder.Body)
	}
	if recorder := launch(`{"kind":"mstore/integration-tests"}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("integration: %d %s", recorder.Code, recorder.Body)
	}
	if len(fixture.jobs) != 2 {
		t.Fatalf("jobs = %+v", fixture.jobs)
	}
	build, tests := fixture.jobs[0], fixture.jobs[1]
	if build.Params["revision"] != "abcdef0123456789" || build.Params["tag-name"] != "pr-12-abcdef0" ||
		build.PullRequestID == nil || *build.PullRequestID != 9 || build.ProjectID == nil || *build.ProjectID != 7 {
		t.Errorf("build = %+v", build)
	}
	if tests.Params["portal-image"] != "reg.example/team/portal:pr-12-abcdef0" || tests.Params["mstore-revision"] != "abcdef0123456789" {
		t.Errorf("integration params = %v", tests.Params)
	}

	recorder := httptest.NewRecorder()
	pullRouter(fixture).ServeHTTP(recorder, asAdmin(httptest.NewRequest(http.MethodPost,
		"/projects/shop/pulls/13/launch", strings.NewReader(`{"kind":"docker/build"}`))))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("an unknown pull request: %d", recorder.Code)
	}
}

// A review launched from a pull request's page reviews that pull request: its number is the
// page's, never asked for.
func TestALaunchOnAPullRequestGivesItsNumber(t *testing.T) {
	fixture := &pullFixture{}
	recorder := httptest.NewRecorder()
	pullRouter(fixture).ServeHTTP(recorder, asAdmin(httptest.NewRequest(http.MethodPost,
		"/projects/shop/pulls/12/launch", strings.NewReader(`{"kind":"review/pull-request","params":{"number":"7"}}`))))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("%d: %s", recorder.Code, recorder.Body)
	}
	if len(fixture.jobs) != 1 || fixture.jobs[0].Params["number"] != "12" {
		t.Errorf("jobs = %+v", fixture.jobs)
	}
}

// The results routes read the request's results, and refuse a file of another subject.
func TestAPullRequestsResultsAreItsOwn(t *testing.T) {
	fixture := &pullFixture{results: []store.TestResult{{ID: 4, Group: "static", Tool: "phpstan", Shape: "cases", Status: "pass"}}}
	results := NewTestsHandler(&pullTestStore{fixture: fixture}, pullCatalogue()).WithPullRequests(fixture)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects/{project}/pulls/{number}/tests", results.PullTests)
	mux.HandleFunc("GET /projects/{project}/pulls/{number}/attachments/{id}", results.PullAttachment)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/projects/shop/pulls/12/tests", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"phpstan"`) {
		t.Fatalf("tests: %d %s", recorder.Code, recorder.Body)
	}

	other := int64(1)
	fixture.attached = store.ResultAttachment{ID: 5, ReleaseID: &other, Object: "x"}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/projects/shop/pulls/12/attachments/5", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("a release's file served under a pull request: %d", recorder.Code)
	}
}

// pullTestStore is the release side of the results handler, which a pull request's routes use
// for the project and the attachments only.
type pullTestStore struct {
	TestStore
	fixture *pullFixture
}

func (s *pullTestStore) ProjectBySlug(ctx context.Context, slug string) (store.Project, error) {
	return s.fixture.ProjectBySlug(ctx, slug)
}

func (s *pullTestStore) ListJobsIn(ctx context.Context, _ store.Scope) ([]store.ReleaseJob, error) {
	return s.fixture.ListPullRequestJobs(ctx, 0)
}

func (s *pullTestStore) LatestFindingsOf(ctx context.Context, scope store.Scope, _ string) (store.SecurityAudit, error) {
	return s.fixture.LatestSecurityAuditIn(ctx, scope)
}

func (s *pullTestStore) ListLoadResults(ctx context.Context, scope store.Scope) ([]store.LoadResult, error) {
	return s.fixture.ListLoadResults(ctx, scope)
}

func (s *pullTestStore) ScoreWeights(context.Context) (map[string]float64, error) { return nil, nil }

func (s *pullTestStore) GetAttachment(context.Context, int64) (store.ResultAttachment, error) {
	return s.fixture.attached, nil
}

func (s *pullTestStore) ListSecurityFindings(_ context.Context, resultID int64, _ bool) ([]store.SecurityFinding, error) {
	if resultID != 6 {
		return nil, nil
	}
	return []store.SecurityFinding{{Fingerprint: "f", Severity: "HIGH", Title: "Fuite", Tool: "pi",
		RuleID: "security", Location: "app/main.go:12-14"}}, nil
}

func (s *pullTestStore) ResultAttachments(_ context.Context, resultID int64) ([]store.ResultAttachment, error) {
	if resultID != 6 {
		return nil, nil
	}
	return []store.ResultAttachment{{ID: 8, Name: "review.md", MediaType: "text/markdown"}}, nil
}

// The review is the latest result of its group, its findings beside it: the security audit,
// which shares their shape, is not it.
func TestAPullRequestsReviewIsItsReviewGroup(t *testing.T) {
	fixture := &pullFixture{}
	results := NewTestsHandler(&pullTestStore{fixture: fixture}, pullCatalogue()).WithPullRequests(fixture)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects/{project}/pulls/{number}/review", results.PullReview)
	read := func() reviewPayload {
		t.Helper()
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/projects/shop/pulls/12/review", nil))
		var payload reviewPayload
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &payload) != nil {
			t.Fatalf("review: %d %s", recorder.Code, recorder.Body)
		}
		return payload
	}

	fixture.results = []store.TestResult{{ID: 4, Group: "security", Tool: "trivy", Shape: "findings", Status: "pass"}}
	if review := read(); review.Reviewed || len(review.Findings) != 0 {
		t.Errorf("the audit read as a review: %+v", review)
	}

	fixture.results = []store.TestResult{
		{ID: 6, Group: ReviewGroup, Tool: "conclave", Shape: "findings", Status: "fail", Summary: "Une fuite."},
		{ID: 2, Group: ReviewGroup, Tool: "conclave", Shape: "findings", Status: "pass"},
		{ID: 4, Group: "security", Tool: "trivy", Shape: "findings", Status: "pass"},
	}
	review := read()
	if !review.Reviewed || review.Status != "fail" || review.Summary != "Une fuite." {
		t.Errorf("review = %+v", review)
	}
	if len(review.Findings) != 1 || review.Findings[0].Location != "app/main.go:12-14" {
		t.Errorf("findings = %+v", review.Findings)
	}
	if len(review.Attachments) != 1 || review.Attachments[0].Name != "review.md" {
		t.Errorf("attachments = %+v", review.Attachments)
	}
}

// A job that failed before any run says why: without it, the page shows a bare "failed".
func TestAFailedJobSaysWhy(t *testing.T) {
	fixture := &pullFixture{failed: &store.ReleaseJob{ID: 4, Kind: "docker/build", State: "failed",
		Error: "the project has no active deploy key"}}
	recorder := httptest.NewRecorder()
	pullRouter(fixture).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/projects/shop/pulls/12", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"error":"the project has no active deploy key"`) {
		t.Errorf("%d: %s", recorder.Code, recorder.Body)
	}
}
