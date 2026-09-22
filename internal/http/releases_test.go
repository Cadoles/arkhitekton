package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/worker"
)

// fakeReleaseStore replaces the database: the decisions tested here are the handler's,
// and making them depend on a PostgreSQL would make them slower without making them
// safer.
type fakeReleaseStore struct {
	// planRuns and planSteps carry the runs opened by creating a release. See below: it is
	// the plan that enqueues now.
	planRuns  map[int64]store.PlanRun
	planSteps map[int64][]store.PlanStep

	releases   map[string]store.Release
	jobs       []store.JobRequest
	promotions []ReleasePromotion
	audit      []string
	// facts carries the emitted facts, keyed by their deduplication key: that is the
	// database's shape, and a slice would not say whether a duplicate was discarded.
	facts     map[string]event.Event
	actions   map[string]string
	artifacts []store.Artifact
	objects   map[string]string
	registry  Images
	states    map[int64]string
	// project describes what the release builds. Nil, the fixture below applies: these
	// tests are about how many runs are queued and with which parameters, not about the
	// table's content.
	project *store.Project
	// produced carries what the automation produced, as the `plan_artifacts` table would
	// return it. Distinct from `artifacts`, which carries the delivery note's exports — two
	// natures, two tables, two names.
	produits []store.PlanArtifact
	// pipelines is the catalogue the handler uses. The store carries it because it must
	// return the project's automation, which is composed from the declared kinds.
	pipelines *catalog.Catalog
}

// fixtureProject reproduces what the bootstrap installs: three stages, one of them
// built without being promoted.
func fixtureProject() store.Project {
	promoted, held := true, false
	return store.Project{
		Repository: "vcarroy/mse",
		Dockerfile: "Dockerfile",
		Targets: []store.Target{
			{Stage: "web", Image: "portal", Promote: &promoted},
			{Stage: "cli", Image: "portal-cli", Promote: &promoted},
			{Stage: "dev", Image: "portal-dev", Promote: &held},
		},
	}
}

// GetProject returns the project the route names. Only one here: these tests are about
// what the handler does with the project, not about choosing it.
func (f *fakeReleaseStore) GetProject(context.Context, int64) (store.Project, error) {
	return fixtureProject(), nil
}

// ReleaseArtifacts returns what the automation produced for this release.
func (f *fakeReleaseStore) ReleaseArtifacts(
	context.Context, int64,
) ([]store.PlanArtifact, error) {
	return f.produits, nil
}

// ReleaseAutomation returns what the catch-up would have composed for this project.
//
// A release's plan is read and no longer composed when it opens. Returning an empty
// plan here would have these tests observe that nothing leaves, with nothing saying it
// is the declaration that is missing.
func (f *fakeReleaseStore) ReleaseAutomation(
	_ context.Context, projectID int64,
) (store.ReleaseAutomation, error) {
	return store.ReleaseAutomation{
		ProjectID: projectID,
		Plan:      worker.DefaultAutomation(f.pipelines, fixtureProject(), nil),
	}, nil
}

func (f *fakeReleaseStore) BuildableProject(context.Context) (store.Project, error) {
	if f.project != nil {
		return *f.project, nil
	}
	return fixtureProject(), nil
}

func (f *fakeReleaseStore) EnsureVersion(context.Context, int64, string) (int64, error) {
	return 1, nil
}

func (f *fakeReleaseStore) CreateRelease(_ context.Context, release store.Release) (int64, error) {
	release.ID = int64(len(f.releases) + 10)
	f.releases[release.Tag] = release
	return release.ID, nil
}

func (f *fakeReleaseStore) GetRelease(_ context.Context, _ int64, tag string) (store.Release, error) {
	release, found := f.releases[tag]
	if !found {
		return store.Release{}, store.ErrReleaseNotFound
	}
	return release, nil
}

func (f *fakeReleaseStore) ListReleases(context.Context, int64, int) ([]store.Release, error) {
	var releases []store.Release
	for _, release := range f.releases {
		releases = append(releases, release)
	}
	return releases, nil
}

func (f *fakeReleaseStore) SetManualActions(_ context.Context, tag, markdown, author string) error {
	release, found := f.releases[tag]
	if !found {
		return store.ErrReleaseNotFound
	}
	release.ManualActions = markdown
	release.ManualActionsUpdatedBy = author
	f.releases[tag] = release
	f.actions[tag] = markdown
	return nil
}

func (f *fakeReleaseStore) ListReleaseJobs(context.Context, int64) ([]store.ReleaseJob, error) {
	return nil, nil
}

func (f *fakeReleaseStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

// RecordEvent keeps the emitted facts, and applies the same deduplication as the
// database: that is what the emission tests exercise.
func (f *fakeReleaseStore) RecordEvent(_ context.Context, fact event.Event) (bool, error) {
	if err := fact.Validate(); err != nil {
		return false, err
	}
	if f.facts == nil {
		f.facts = map[string]event.Event{}
	}
	if _, known := f.facts[fact.DedupKey]; known {
		return false, nil
	}
	f.facts[fact.DedupKey] = fact
	return true, nil
}

func (f *fakeReleaseStore) ListArtifacts(_ context.Context, releaseID int64) ([]store.Artifact, error) {
	var found []store.Artifact
	for _, artifact := range f.artifacts {
		if artifact.ReleaseID == releaseID {
			found = append(found, artifact)
		}
	}
	return found, nil
}

func (f *fakeReleaseStore) GetArtifact(_ context.Context, id int64) (store.Artifact, error) {
	for _, artifact := range f.artifacts {
		if artifact.ID == id {
			return artifact, nil
		}
	}
	return store.Artifact{}, store.ErrArtifactNotFound
}

// Get doubles as the object store: the handler expects only a read from it.
func (f *fakeReleaseStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	content, found := f.objects[key]
	if !found {
		return nil, errors.New("object missing")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeReleaseStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	f.jobs = append(f.jobs, request)
	return int64(len(f.jobs)), nil
}

// states keeps the written states, to check that a pre-release moves to `building`
// after its build is queued.
//
// MarkReleased applies the same condition as the database: the write succeeds only if
// the release is still in the state that was read.
func (f *fakeReleaseStore) MarkReleased(_ context.Context, id int64, from string) (bool, error) {
	for tag, release := range f.releases {
		if release.ID != id {
			continue
		}
		if release.State != from {
			return false, nil
		}
		release.State = "released"
		f.releases[tag] = release
		return true, nil
	}
	return false, nil
}

func (f *fakeReleaseStore) AdvanceReleaseState(_ context.Context, id int64, from, to string) (bool, error) {
	for tag, release := range f.releases {
		if release.ID != id {
			continue
		}
		if release.State != from {
			return false, nil
		}
		release.State = to
		f.releases[tag] = release
		return true, nil
	}
	return false, nil
}

func (f *fakeReleaseStore) SetReleaseState(_ context.Context, id int64, state string) error {
	if f.states == nil {
		f.states = map[int64]string{}
	}
	f.states[id] = state
	return nil
}

// testProjectSlug is the project the routes name in their path.
const testProjectSlug = "test"

func newFakeStore() *fakeReleaseStore {
	return &fakeReleaseStore{
		releases: map[string]store.Release{},
		actions:  map[string]string{},
		objects:  map[string]string{},
	}
}

// fakeForge resolves and sets tags, keeping what it was asked for.
type fakeForge struct {
	commit     string
	resolveErr error
	tagErr     error
	tags       []string
	resolved   []string
}

func (f *fakeForge) ResolveCommit(_ context.Context, ref string) (string, error) {
	f.resolved = append(f.resolved, ref)
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return f.commit, nil
}

func (f *fakeForge) CreateTag(_ context.Context, tag, commit, _ string) error {
	if f.tagErr != nil {
		return f.tagErr
	}
	f.tags = append(f.tags, tag+"@"+commit)
	return nil
}

func newReleasesHandler(t *testing.T, store *fakeReleaseStore) *ReleasesHandler {
	t.Helper()
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "retag", PipelineRef: "retag-image"},
		{
			Kind: "build", PipelineRef: "build-image",
			Produces: []catalog.Product{catalog.ProductImage},
			// The kind's real parameters. A release's plan no longer sets the revision and the tag
			// in the clear: it reads what the catalogue declares it wants. A silent test catalogue
			// would send builds off with no revision, which is exactly what these tests check.
			Params: map[string]catalog.Param{
				"revision": {Source: catalog.SourceInput, Required: true,
					Suggest: catalog.SuggestGitRef},
				"version": {Source: catalog.SourceInput, Required: true,
					Suggest: catalog.SuggestReleaseTag},
				"tag-name": {Source: catalog.SourceInput, Required: true,
					Suggest: catalog.SuggestReleaseTag},
				"image-name": {Source: catalog.SourceInput, Required: true,
					Suggest: catalog.SuggestImageName},
				"target":     {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestBuildTarget},
				"dockerfile": {Source: catalog.SourceInput},
				"context":    {Source: catalog.SourceInput},
			},
		},
		{
			Kind: "unit-tests", PipelineRef: "php-unit-tests", Verification: true,
			Params: map[string]catalog.Param{
				"revision": {
					Source: catalog.SourceInput, Required: true,
					Suggest: catalog.SuggestGitRef},
			},
		},
	}}
	store.pipelines = pipelines
	authorizer := auth.NewAuthorizer(auth.ProjectPolicies{testProjectSlug: {
		"prod": {Sensitive: true}, "preprod": {Sensitive: true},
	}})
	return NewReleasesHandler(store, pipelines, authorizer, store.registry, store,
		"portal", "reg.cadoles.com/vcarroy").
		// The real planner, on the same fake store. A substitute here would make these tests
		// pass while saying nothing about what creation actually queues.
		WithArtifactReader(store).
		// The requested promotions, as the MSE plugin would supply them.
		WithPromotions(func(context.Context, int64, int64) ([]ReleasePromotion, error) {
			return store.promotions, nil
		}).
		WithPlans(worker.NewReleasePlanner(
			worker.NewEngine(store, worker.NewReleaseRunner(pipelines, store, quietLogger()),
				quietLogger()),
			pipelines, store, "reg.cadoles.com/vcarroy", quietLogger()))
}

func asRole(request *http.Request, role auth.Role) *http.Request {
	identity := auth.Identity{Subject: "vcarroy", Username: "vcarroy", Role: role}
	// The project is set here because every request to a project-scoped handler goes
	// through this door: the routes carry it in their path, and a test request that forgot
	// it would fail on "no project named" instead of measuring what it aims at. The tests
	// that exercise the resolution itself replace it afterwards.
	request.SetPathValue("project", testProjectSlug)
	return request.WithContext(context.WithValue(request.Context(), identityKey, identity))
}

// Creating a pre-release tag while declaring it final produces a release production
// will silently ignore. The refusal must be immediate.
func TestCreateRefusesAKindThatContradictsTheTag(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0-rc1","kind":"final"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, expected 400", recorder.Code)
	}
}

func TestCreateRefusesAMismatchedRedmineVersion(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0-rc1","kind":"pre","tracked_version":"1.9.0"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, expected 400: the join with Redmine would be broken", recorder.Code)
	}
}

// The final release inherits its candidate's image, and its own tag must exist in the
// registry for an image policy to select it.
func TestCreateFinalRetagsFromItsSource(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc3"] = store.Release{
		ID: 4, Tag: "1.8.0-rc3", Kind: "pre", State: "released", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0","kind":"final","source_tag":"1.8.0-rc3","tests_green":true}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}

	created := fake.releases["1.8.0"]
	if created.ImageVersion != "1.8.0-rc3" {
		t.Errorf("image version = %q, expected the RC's", created.ImageVersion)
	}
	if created.State != "ready" {
		t.Errorf("state = %q: the image already exists, the release is promotable", created.State)
	}

	retags := fake.jobsOfKind("retag")
	if len(retags) != len(fixtureProject().Promotable()) {
		t.Fatalf("%d re-tag job(s), expected %d", len(retags), len(fixtureProject().Promotable()))
	}
	if retags[0].Params["old-tag"] != "1.8.0-rc3" || retags[0].Params["new-tag"] != "1.8.0" {
		t.Errorf("re-tag = %+v", retags[0].Params)
	}
	// The note is composed for every release, final ones included: what it brings the
	// client does not depend on how its image was produced.
	if len(fake.jobsOfKind(ReleaseNotesJobKind)) != 1 {
		t.Error("composing the note should be queued")
	}
}

// A candidate whose image is not built cannot be re-tagged: there is nothing to
// promote.
func TestCreateFinalRefusesAnUnbuiltSource(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc3"] = store.Release{
		ID: 5, Tag: "1.8.0-rc3", Kind: "pre", State: "draft", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0","kind":"final","source_tag":"1.8.0-rc3"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, expected 400", recorder.Code)
	}
}

// Shipping a final version commits what reaches the user: the kind is in the body, so
// it is for the handler to check it.
func TestCreateFinalRequiresTheReleaserRole(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc3"] = store.Release{
		ID: 6, Tag: "1.8.0-rc3", Kind: "pre", State: "released", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0","kind":"final","source_tag":"1.8.0-rc3"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleOperator))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("code = %d, expected 403", recorder.Code)
	}
}

// fakeRegistry returns a digest per known tag, and fails on the others — a promotion tag
// that has been queued does not exist in the registry yet.
type fakeRegistry map[string]string

func (f fakeRegistry) Digest(_ context.Context, _, tag string) (string, error) {
	digest, found := f[tag]
	if !found {
		return "", errors.New("manifest missing")
	}
	return digest, nil
}

// The digest is what proves a promotion did not rebuild: the suffixed tag and the
// original tag carry the same one.
func TestGetResolvesDigestsForEveryTag(t *testing.T) {
	fake := newFakeStore()
	// The promotion tag carries the same digest: nothing was rebuilt.
	fake.registry = fakeRegistry{"1.8.0-rc3": "sha256:aaa", "1.8.0_prod": "sha256:aaa"}
	fake.releases["1.8.0"] = store.Release{
		ID: 8, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	fake.promotions = []ReleasePromotion{
		{Environment: "prod", Image: "portal", Tag: "1.8.0_prod"},
	}
	fake.produits = []store.PlanArtifact{
		{RunID: 3, Stage: 0, Position: 0, Name: "image", Kind: "image",
			Reference: "reg.example.fr/vcarroy/portal:1.8.0-rc3"},
		{RunID: 3, Stage: 0, Position: 0, Name: "tag", Kind: "image",
			Reference: "1.8.0-rc3"},
	}
	fake.planRuns = map[int64]store.PlanRun{3: {
		ID: 3, Plan: plan.Plan{Stages: []plan.Stage{{
			Role: plan.RoleBuild,
			Gestes: []plan.Geste{{
				Kind: plan.GestePipeline, Ref: "build", Label: "portal",
			}},
		}}},
	}}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0", nil)
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	handler.Get(recorder, asRole(request, auth.RoleReleaser))

	var payload releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if len(payload.Artifacts) != 2 {
		t.Fatalf("%d artifact(s), expected the step's", len(payload.Artifacts))
	}
	// Each carries the name of the step that produced it: that is what tells three builds
	// sharing the same catalogue reference apart.
	for _, artifact := range payload.Artifacts {
		if artifact.Geste != "portal" {
			t.Errorf("artifact %q produced by %q, expected the step", artifact.Name, artifact.Geste)
		}
	}

	image := payload.Artifacts[0]
	if image.Name != "image" || image.Digest != "sha256:aaa" {
		t.Errorf("image = %+v, expected with its digest", image)
	}
	if image.Diverged {
		t.Error("divergence announced although the promotion carries the same digest")
	}
	// The tag is not an image, and therefore has no digest — although `kind` is `image` for
	// everything this pipeline returns.
	if payload.Artifacts[1].Digest != "" {
		t.Errorf("the tag carries a digest: %+v", payload.Artifacts[1])
	}
}

// An image rebuilt between approval and delivery shows.
//
// That is the invariant the old "images" section protected on the single configured
// repository, and which now holds for every image produced.
func TestGetSignalsARebuiltImage(t *testing.T) {
	fake := newFakeStore()
	// The promotion tag carries a different digest: someone rebuilt.
	fake.registry = fakeRegistry{"1.8.0-rc3": "sha256:aaa", "1.8.0_prod": "sha256:bbb"}
	fake.releases["1.8.0"] = store.Release{
		ID: 8, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	fake.promotions = []ReleasePromotion{
		{Environment: "prod", Image: "portal", Tag: "1.8.0_prod"},
	}
	fake.produits = []store.PlanArtifact{{
		RunID: 3, Name: "image", Kind: "image",
		Reference: "reg.example.fr/vcarroy/portal:1.8.0-rc3",
	}}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0", nil)
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	handler.Get(recorder, asRole(request, auth.RoleReleaser))

	var payload releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Artifacts) != 1 || !payload.Artifacts[0].Diverged {
		t.Errorf("artifacts = %+v — the rebuild must show", payload.Artifacts)
	}
}

// A missing registry must not make the page unreadable, but it must say so: "no digest"
// and "cannot read the digest" do not call for the same response.
func TestGetSaysWhyDigestsAreMissing(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc1"] = store.Release{
		ID: 9, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
	}
	fake.produits = []store.PlanArtifact{{
		RunID: 4, Name: "image", Kind: "image",
		Reference: "reg.example.fr/vcarroy/portal:1.8.0-rc1",
	}}
	handler := newReleasesHandler(t, fake)
	// Without a registry: the artifacts stay displayable, without their digest.
	handler.images = nil

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0-rc1", nil)
	request.SetPathValue("tag", "1.8.0-rc1")
	recorder := httptest.NewRecorder()
	handler.Get(recorder, asRole(request, auth.RoleViewer))

	var payload releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("code = %d: a missing registry must not break the page", recorder.Code)
	}
	if payload.ArtifactsNote == "" {
		t.Error("the missing digest must be explained")
	}
	if len(payload.Artifacts) == 0 {
		t.Error("the artifacts stay displayable without their digest")
	}
}

func TestUpdateActionsRecordsTheAuthor(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 10, Tag: "1.8.0", Kind: "final", State: "ready"}
	handler := newReleasesHandler(t, fake)

	body := `{"markdown":"- cache purge"}`
	request := httptest.NewRequest(http.MethodPut, "/releases/1.8.0/actions", strings.NewReader(body))
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()

	handler.UpdateActions(recorder, asRole(request, auth.RoleOperator))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	if fake.actions["1.8.0"] != "- cache purge" {
		t.Errorf("texte = %q", fake.actions["1.8.0"])
	}
	// The author is set by the server, never by the client: this text ends up in a
	// contractual document.
	if fake.releases["1.8.0"].ManualActionsUpdatedBy != "vcarroy" {
		t.Errorf("auteur = %q", fake.releases["1.8.0"].ManualActionsUpdatedBy)
	}
}

func TestUpdateActionsOnAnUnknownRelease(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPut, "/releases/9.9.9/actions", strings.NewReader(`{"markdown":"x"}`))
	request.SetPathValue("tag", "9.9.9")
	recorder := httptest.NewRecorder()

	handler.UpdateActions(recorder, asRole(request, auth.RoleOperator))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code = %d, expected 404", recorder.Code)
	}
}

func TestExportEnqueuesAJobBoundToTheRelease(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 20, Tag: "1.8.0", Kind: "final", State: "ready"}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPost, "/releases/1.8.0/exports", strings.NewReader(`{}`))
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	handler.Export(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.jobs) != 1 {
		t.Fatalf("%d job(s)", len(fake.jobs))
	}
	job := fake.jobs[0]
	if job.Kind != exportJobKind || job.ReleaseID == nil || *job.ReleaseID != 20 {
		t.Errorf("job = %+v", job)
	}
	// The generator starts from the release, not from the tag: without the attachment it
	// would not know what to produce.
	if job.Params[exportParamKind] != store.ArtifactDeliveryNote || job.Params[exportParamFormat] != "pdf" {
		t.Errorf("parameters = %v", job.Params)
	}
}

func TestExportRefusesAnUnknownFormat(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 21, Tag: "1.8.0"}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPost, "/releases/1.8.0/exports", strings.NewReader(`{"format":"docx"}`))
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	handler.Export(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, expected 400", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Error("no job must start for an unknown format")
	}
}

func TestDownloadServesTheArtifact(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 22, Tag: "1.8.0"}
	fake.artifacts = []store.Artifact{
		{ID: 7, ReleaseID: 22, Kind: store.ArtifactDeliveryNote, Format: "pdf",
			Object: "artifacts/releases/1.8.0/delivery-note.pdf"},
	}
	fake.objects["artifacts/releases/1.8.0/delivery-note.pdf"] = "%PDF-1.3 contenu"
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0/exports/7/download", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("artifact", "7")
	recorder := httptest.NewRecorder()
	handler.Download(recorder, asRole(request, auth.RoleViewer))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("type = %q", recorder.Header().Get("Content-Type"))
	}
	if !strings.HasPrefix(recorder.Body.String(), "%PDF-") {
		t.Errorf("corps = %q", recorder.Body.String())
	}
}

// Without that check, a guessed identifier would give another release's delivery note —
// a contractual document that is not its own.
func TestDownloadRefusesAnArtifactOfAnotherRelease(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 23, Tag: "1.8.0"}
	fake.artifacts = []store.Artifact{{ID: 8, ReleaseID: 999, Object: "ailleurs"}}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0/exports/8/download", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("artifact", "8")
	recorder := httptest.NewRecorder()
	handler.Download(recorder, asRole(request, auth.RoleViewer))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code = %d, expected 404", recorder.Code)
	}
}

// The row outlives a purge of the bucket: saying so beats an empty file.
func TestDownloadReportsAMissingObject(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 24, Tag: "1.8.0"}
	fake.artifacts = []store.Artifact{{ID: 9, ReleaseID: 24, Format: "pdf", Object: "purged"}}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0/exports/9/download", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("artifact", "9")
	recorder := httptest.NewRecorder()
	handler.Download(recorder, asRole(request, auth.RoleViewer))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code = %d, expected 404", recorder.Code)
	}
}

// The missing link: creating a pre-release must queue its build and announce it.
// Without that the release stayed `draft` indefinitely, the pipeline existing in the
// cluster but nothing triggering it.
func TestAPreReleaseQueuesItsBuild(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(`{
		"tag": "3.2.0-rc1", "kind": "pre", "git_ref": "develop"
	}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}

	// One run per image: the pipeline builds only one, and it is the dashboard that decides
	// how many it launches.
	builds := fake.jobsOfKind("build")
	if len(builds) != len(fixtureProject().Buildable()) {
		t.Fatalf("%d build(s) queued, expected %d", len(builds), len(fixtureProject().Buildable()))
	}
	for index, target := range fixtureProject().Buildable() {
		if builds[index].Params["image-name"] != target.Image ||
			builds[index].Params["target"] != target.Stage {
			t.Errorf("build %d = %+v, expected %s/%s",
				index, builds[index].Params, target.Image, target.Stage)
		}
	}
	job := builds[0]
	if len(fake.jobsOfKind(ReleaseNotesJobKind)) != 1 {
		t.Error("composing the note should be queued")
	}
	// `version` carries the SemVer tag and prevails over `tag-name`: without it the Task
	// would derive the tag from the branch and the image would not carry the version of the
	// release that asked for it.
	if job.Params["version"] != "3.2.0-rc1" {
		t.Errorf("version transmise %q", job.Params["version"])
	}
	if job.Params["revision"] != "develop" {
		t.Errorf("revision passed %q", job.Params["revision"])
	}
	if job.ReleaseID == nil {
		t.Error("the job must carry its release, otherwise its conclusion will advance nothing")
	}

	var response struct {
		// A count, and not a list of identifiers. Creation now opens a plan: what leaves is a
		// stage, whose steps read on the runs screen rather than in this response.
		BuildJobs int `json:"build_jobs"`
		Release   struct {
			State string `json:"state"`
		} `json:"release"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if response.BuildJobs != len(fixtureProject().Buildable()) {
		t.Errorf("%d build(s) returned, expected %d",
			response.BuildJobs, len(fixtureProject().Buildable()))
	}
	if response.Release.State != "building" {
		t.Errorf("state returned %q, expected building", response.Release.State)
	}
}

// A final release builds nothing: it is a re-tag, and demanding a build of it would run
// a pipeline to produce an image that already exists.
func TestAFinalReleaseDoesNotQueueABuild(t *testing.T) {
	fake := newFakeStore()
	fake.releases["3.2.0-rc1"] = store.Release{
		ID: 1, Tag: "3.2.0-rc1", Kind: "pre", State: "released", ImageVersion: "3.2.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(`{
		"tag": "3.2.0", "kind": "final", "source_tag": "3.2.0-rc1", "tests_green": true
	}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, job := range fake.jobs {
		if job.Kind == "build" {
			t.Error("a final release must not queue a build")
		}
	}
}

// A pre-release with no git reference is created anyway, and the caveat says so: passing
// an empty reference would fail the clone several minutes later, with a message that
// would not name the cause.
func TestAPreReleaseWithNoReferenceIsCreatedWithAReservation(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(`{"tag": "3.2.0-rc9", "kind": "pre"}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if len(response.Warnings) == 0 {
		t.Error("the reservation must be returned: the release exists but nothing is building")
	}
	for _, job := range fake.jobs {
		if job.Kind == "build" {
			t.Error("no build must start without a Git reference")
		}
	}
}

// Approval is the cycle's human decision: it moves `ready` to `released`, which allows
// promotion and re-tagging.
func TestValidatingAReadyReleaseMakesItDeliverable(t *testing.T) {
	fake := newFakeStore()
	fake.releases["3.2.0-rc1"] = store.Release{
		ID: 7, Tag: "3.2.0-rc1", Kind: "pre", State: "ready", ImageVersion: "3.2.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := asRole(httptest.NewRequest(http.MethodPost, "/releases/3.2.0-rc1/release", nil),
		auth.RoleReleaser)
	request.SetPathValue("tag", "3.2.0-rc1")
	recorder := httptest.NewRecorder()
	handler.Release(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	if fake.releases["3.2.0-rc1"].State != "released" {
		t.Errorf("state %q, expected released", fake.releases["3.2.0-rc1"].State)
	}
	// The decision commits what will ship: it is recorded.
	if len(fake.audit) == 0 {
		t.Error("the validation must be written to the audit log")
	}
}

// Approving only makes sense from `ready`: nothing to approve before the tests, nothing
// left to decide after. The refusal is a 409 and not a 400 — the request is correct, it
// is the state that does not lend itself to it.
func TestValidatingFromAnotherStateIsRefused(t *testing.T) {
	for _, state := range []string{"draft", "building", "verifying", "released", "cancelled"} {
		fake := newFakeStore()
		fake.releases["3.2.0-rc1"] = store.Release{
			ID: 7, Tag: "3.2.0-rc1", Kind: "pre", State: state, ImageVersion: "3.2.0-rc1",
		}
		handler := newReleasesHandler(t, fake)

		request := asRole(httptest.NewRequest(http.MethodPost, "/releases/3.2.0-rc1/release", nil),
			auth.RoleReleaser)
		request.SetPathValue("tag", "3.2.0-rc1")
		recorder := httptest.NewRecorder()
		handler.Release(recorder, request)

		if recorder.Code != http.StatusConflict {
			t.Errorf("state %q: code %d, expected 409", state, recorder.Code)
		}
		if fake.releases["3.2.0-rc1"].State != state {
			t.Errorf("state %q changed to %q", state, fake.releases["3.2.0-rc1"].State)
		}
	}
}

// Retrying after a failure rebuilds from the same reference, without creating a version:
// making the version number carry the history of the cluster's outages would be absurd.
func TestRerunningAFailedBuildReusesTheSameReference(t *testing.T) {
	fake := newFakeStore()
	fake.releases["3.2.0-rc1"] = store.Release{
		ID: 9, Tag: "3.2.0-rc1", Kind: "pre", State: "failed",
		GitRef: "main", ImageVersion: "3.2.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := asRole(httptest.NewRequest(http.MethodPost, "/releases/3.2.0-rc1/build", nil),
		auth.RoleOperator)
	request.SetPathValue("tag", "3.2.0-rc1")
	recorder := httptest.NewRecorder()
	handler.Rebuild(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.jobs) != len(fixtureProject().Buildable()) || fake.jobs[0].Kind != "build" {
		t.Fatalf("jobs: %+v", fake.jobs)
	}
	if fake.jobs[0].Params["revision"] != "main" {
		t.Errorf("revision %q, expected the release's", fake.jobs[0].Params["revision"])
	}
	if fake.jobs[0].Params["version"] != "3.2.0-rc1" {
		t.Errorf("version %q", fake.jobs[0].Params["version"])
	}
	if fake.releases["3.2.0-rc1"].State != "building" {
		t.Errorf("state %q, expected building", fake.releases["3.2.0-rc1"].State)
	}
}

// A final release has nothing to rebuild: its image is its source candidate's, and a
// pipeline would produce a second image under the same tag.
func TestAFinalReleaseIsNotRebuilt(t *testing.T) {
	fake := newFakeStore()
	fake.releases["3.2.0"] = store.Release{
		ID: 9, Tag: "3.2.0", Kind: "final", State: "failed", ImageVersion: "3.2.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := asRole(httptest.NewRequest(http.MethodPost, "/releases/3.2.0/build", nil),
		auth.RoleOperator)
	request.SetPathValue("tag", "3.2.0")
	recorder := httptest.NewRecorder()
	handler.Rebuild(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("code %d, expected 409", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Errorf("no job should have started: %+v", fake.jobs)
	}
}

// Rebuilding a release already shipped would overwrite an image in service. The
// transition table forbids it, and the handler abides by that.
func TestADeliveredReleaseIsNotRebuilt(t *testing.T) {
	fake := newFakeStore()
	fake.releases["3.2.0-rc1"] = store.Release{
		ID: 9, Tag: "3.2.0-rc1", Kind: "pre", State: "released",
		GitRef: "main", ImageVersion: "3.2.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := asRole(httptest.NewRequest(http.MethodPost, "/releases/3.2.0-rc1/build", nil),
		auth.RoleOperator)
	request.SetPathValue("tag", "3.2.0-rc1")
	recorder := httptest.NewRecorder()
	handler.Rebuild(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("code %d, expected 409", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Errorf("no job should have started: %+v", fake.jobs)
	}
}

// A release must name a commit, not a branch: the branch moves, and rebuilding six
// months later would produce a different image.
func TestAPreReleaseIsAnchoredOnACommitAndTagged(t *testing.T) {
	fake := newFakeStore()
	forge := &fakeForge{commit: "abc123def456"}
	handler := newReleasesHandler(t, fake).WithForge(forge, forge)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases",
			strings.NewReader(`{"tag":"3.2.0-rc1","kind":"pre","git_ref":"main"}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}

	// The branch is resolved into a commit, and the tag set on that commit — never on the
	// branch, which would name something else a minute later.
	if len(forge.resolved) != 1 || forge.resolved[0] != "main" {
		t.Errorf("references resolved: %v", forge.resolved)
	}
	if len(forge.tags) != 1 || forge.tags[0] != "3.2.0-rc1@abc123def456" {
		t.Errorf("tags set: %v", forge.tags)
	}

	// The build starts from the commit, not from the tag or the branch: a shallow clone does
	// not create a tag's local reference, and the checkout the Task then performs would
	// fail.
	builds := fake.jobsOfKind("build")
	if len(builds) != len(fixtureProject().Buildable()) {
		t.Fatalf("jobs: %+v", fake.jobs)
	}
	if got := builds[0].Params["revision"]; got != "abc123def456" {
		t.Errorf("revision built %q, expected the commit", got)
	}
	// The tag is still carried by `version`, which becomes the image's tag.
	if got := builds[0].Params["version"]; got != "3.2.0-rc1" {
		t.Errorf("version %q, expected the release's tag", got)
	}
}

// With no token able to write, the release still exists: it is anchored on its commit
// and the caveat says so. Refusing would deny the whole chain for a tag that can be set
// afterwards.
func TestWithoutWriteAccessTheReleaseStaysAnchoredOnItsCommit(t *testing.T) {
	fake := newFakeStore()
	forge := &fakeForge{commit: "abc123def456"}
	// Forge wired read-only: no tagger.
	handler := newReleasesHandler(t, fake).WithForge(forge, nil)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases",
			strings.NewReader(`{"tag":"3.2.0-rc1","kind":"pre","git_ref":"main"}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if len(response.Warnings) == 0 {
		t.Error("the missing tag must be reported")
	}

	// The build starts from the commit: immutable, even with no tag.
	if got := fake.jobs[0].Params["revision"]; got != "abc123def456" {
		t.Errorf("revision built %q, expected the resolved commit", got)
	}
}

// Forge unreachable: the release is created, the caveat says it is not reproducible, and
// the build falls back to the typed reference.
func TestAnUnreachableForgeDoesNotPreventCreation(t *testing.T) {
	fake := newFakeStore()
	forge := &fakeForge{resolveErr: errors.New("forge injoignable")}
	handler := newReleasesHandler(t, fake).WithForge(forge, forge)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases",
			strings.NewReader(`{"tag":"3.2.0-rc1","kind":"pre","git_ref":"main"}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(forge.tags) != 0 {
		t.Error("no tag must be set without a resolved commit")
	}
	if got := fake.jobs[0].Params["revision"]; got != "main" {
		t.Errorf("revision built %q, expected the fallback to the reference typed", got)
	}
}

// A final release is neither anchored nor tagged here: it is a re-tag, its commit is its
// source candidate's, already tagged at its own creation.
func TestAFinalReleaseIsNotTaggedASecondTime(t *testing.T) {
	fake := newFakeStore()
	fake.releases["3.2.0-rc1"] = store.Release{
		ID: 1, Tag: "3.2.0-rc1", Kind: "pre", State: "released", ImageVersion: "3.2.0-rc1",
	}
	forge := &fakeForge{commit: "abc123"}
	handler := newReleasesHandler(t, fake).WithForge(forge, forge)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases",
			strings.NewReader(`{"tag":"3.2.0","kind":"final","source_tag":"3.2.0-rc1","tests_green":true}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(forge.tags) != 0 || len(forge.resolved) != 0 {
		t.Errorf("a final must neither resolve nor tag: resolved=%v tags=%v",
			forge.resolved, forge.tags)
	}
}

/*
 * The reads the badge composition adds to the store.
 *
 * Mute: these tests bear on creating, validating and promoting a release, not on its score.
 * Returning empty values makes the badge not display, which is exactly the expected behaviour for
 * a release with no results.
 */
func (f *fakeReleaseStore) ReleaseBadgesByTag(context.Context) (map[string]store.ReleaseBadges, error) {
	return map[string]store.ReleaseBadges{}, nil
}

func (f *fakeReleaseStore) ListReleaseTestResults(context.Context, int64) ([]store.TestResult, error) {
	return nil, nil
}

func (f *fakeReleaseStore) LatestSecurityAudit(context.Context, int64) (store.SecurityAudit, error) {
	return store.SecurityAudit{}, store.ErrAuditNotFound
}

func (f *fakeReleaseStore) ListReleaseLoadResults(context.Context, int64) ([]store.LoadResult, error) {
	return nil, nil
}

func (f *fakeReleaseStore) ListResultMeasures(context.Context, int64) ([]store.BenchMeasure, error) {
	return nil, nil
}

func (f *fakeReleaseStore) Baselines(context.Context, string) (map[string]store.Baseline, error) {
	return map[string]store.Baseline{}, nil
}

// The release note: these tests do not cover it, detection being exercised in
// internal/worker. An empty note is the expected behaviour for a release with no issue
// linked.
//
// jobsOfKind returns the jobs of a given kind.
//
// The tests name the job they examine by its kind rather than by its position: every
// creation now queues two — the build or the re-tag, and composing the note — and a fixed
// index would have broken at the first addition.
func (f *fakeReleaseStore) jobsOfKind(kind string) []store.JobRequest {
	var found []store.JobRequest
	for _, job := range f.jobs {
		if job.Kind == kind {
			found = append(found, job)
		}
	}
	return found
}

func (f *fakeReleaseStore) ListNoteEntries(context.Context, int64) ([]store.NoteEntry, error) {
	return nil, nil
}

func (f *fakeReleaseStore) LinkNoteIssue(context.Context, int64, string, string, string, string) error {
	return nil
}

func (f *fakeReleaseStore) UnlinkNoteIssue(context.Context, int64, string) error {
	return nil
}

func (f *fakeReleaseStore) ListReleaseVersionIssues(context.Context, int64) ([]store.NoteEntry, error) {
	return nil, nil
}

// The directory of actor names. Empty: these tests are about behaviour, not display, and
// an empty directory falls back to the subject — exactly what an unknown actor does.
func (f *fakeReleaseStore) Directory(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// Re-running a single family now reads the catalogue instead of a hard-coded list, and
// it is the `verification` flag that says what can be replayed.
//
// The point that matters is the refusal: a build is in the catalogue too, and a rebuild
// disguised as a test re-run would republish the release's images instead of replaying a
// test.
func TestRerunningOneKindAcceptsOnlyVerifications(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{
		ID: 42, Tag: "1.8.0", State: "failed", GitCommit: "abc123def456",
	}
	handler := newReleasesHandler(t, fake)

	verify := func(kind string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/releases/1.8.0/verify/"+kind, nil)
		request.SetPathValue("tag", "1.8.0")
		request.SetPathValue("kind", kind)
		recorder := httptest.NewRecorder()
		handler.Verify(recorder, asRole(request, auth.RoleReleaser))
		return recorder
	}

	recorder := verify("unit-tests")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code = %d, expected 202: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.jobs) != 1 || fake.jobs[0].Kind != "unit-tests" {
		t.Fatalf("jobs mis en file: %+v", fake.jobs)
	}
	// The revision verified is the commit that was built, never the branch it came from:
	// that may have moved, and the tests would then be about something other than the image.
	if got := fake.jobs[0].Params["revision"]; got != "abc123def456" {
		t.Errorf("revision re-run = %q, expected the commit built", got)
	}

	if recorder := verify("build"); recorder.Code != http.StatusBadRequest {
		t.Errorf("re-running `build` must be refused, code = %d", recorder.Code)
	}
	if recorder := verify("unknown-kind"); recorder.Code != http.StatusBadRequest {
		t.Errorf("a kind missing from the catalogue must be refused, code = %d", recorder.Code)
	}
	if len(fake.jobs) != 1 {
		t.Errorf("a refusal must queue nothing: %+v", fake.jobs)
	}
}

// ProjectBySlug resolves the project named in the path. Only one here: these tests are
// about what the handler does with the project, not about resolving it.
func (f *fakeReleaseStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != testProjectSlug {
		return store.Project{}, store.ErrProjectNotFound
	}
	// The same project as GetProject, and that is a correction.
	//
	// This fake used to return a project with no targets here, and the complete project
	// through GetProject — two answers for one row of the database. The discrepancy did not
	// show while the build read through GetProject; now that a release's plan is composed
	// from the project the route serves, it decides what gets built, and an inconsistent fake
	// would make tests pass on a project that does not exist.
	//
	// The default cycle is set as the real read sets it: without it the fake would describe a
	// project with no creatable release.
	project := fixtureProject()
	project.Slug = slug
	project.ReleaseKinds = release.DefaultCycle()
	return project, nil
}

// An unknown project returns 404, never another's release.
//
// A tag is unique only within a project, and a URL naming one that does not exist must
// say so. Answering 500 would send someone hunting for a fault over a stale address;
// answering 200 with someone else's release would be worse.
func TestAnUnknownProjectDoesNotReturnAnothersRelease(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 42, Tag: "1.8.0", State: "ready"}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/projects/absent/releases/1.8.0", nil)
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	// `asRole` sets the known project: this test replaces it, since resolution is precisely
	// what it exercises.
	request = asRole(request, auth.RoleViewer)
	request.SetPathValue("project", "absent")

	handler.Get(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("code = %d, expected 404: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "1.8.0") {
		t.Error("the response must say nothing of another project's release")
	}
}

/*
 * The fake store's plan store.
 *
 * Creating a release now opens **its plan**, and it is the plan that queues the builds. The fake
 * store therefore carries, besides the queue, what is needed to open a run — without which these
 * tests would exercise a nil planner rather than the real path.
 *
 * In memory and deliberately poor: what is exercised here is what **goes into the queue**, not
 * the engine's mechanics, which have their own tests.
 */

func (f *fakeReleaseStore) CreatePlanRun(
	_ context.Context, run store.PlanRun,
) (store.PlanRun, bool, error) {
	if f.planRuns == nil {
		f.planRuns = map[int64]store.PlanRun{}
		f.planSteps = map[int64][]store.PlanStep{}
	}
	// Uniqueness by (trigger, fact), as in the database: a redelivery returns the existing
	// run rather than opening a second one.
	for _, known := range f.planRuns {
		if known.Trigger == run.Trigger && known.EventKey == run.EventKey {
			return known, false, nil
		}
	}

	run.ID = int64(len(f.planRuns) + 1)
	run.State = store.PlanRunning
	f.planRuns[run.ID] = run
	for stage, planWithStage := range run.Plan.Stages {
		for position := range planWithStage.Gestes {
			f.planSteps[run.ID] = append(f.planSteps[run.ID], store.PlanStep{
				Stage: stage, Position: position, State: string(plan.Pending),
			})
		}
	}
	return run, true, nil
}

func (f *fakeReleaseStore) PlanRunByID(_ context.Context, id int64) (store.PlanRun, error) {
	run, found := f.planRuns[id]
	if !found {
		return store.PlanRun{}, store.ErrPlanRunNotFound
	}
	return run, nil
}

func (f *fakeReleaseStore) PlanSteps(_ context.Context, runID int64) ([]store.PlanStep, error) {
	return f.planSteps[runID], nil
}

func (f *fakeReleaseStore) MarkPlanStep(
	_ context.Context, runID int64, stage, position int,
	state string, job *int64, detail string,
) error {
	for index, step := range f.planSteps[runID] {
		if step.Stage == stage && step.Position == position {
			f.planSteps[runID][index].State = state
			f.planSteps[runID][index].JobID = job
			f.planSteps[runID][index].Detail = detail
			return nil
		}
	}
	return nil
}

func (f *fakeReleaseStore) PlanStepForJob(
	_ context.Context, jobID int64,
) (int64, store.PlanStep, error) {
	for runID, steps := range f.planSteps {
		for _, step := range steps {
			if step.JobID != nil && *step.JobID == jobID {
				return runID, step, nil
			}
		}
	}
	return 0, store.PlanStep{}, store.ErrPlanRunNotFound
}

func (f *fakeReleaseStore) FinishPlanRun(
	_ context.Context, runID int64, state string,
) (bool, error) {
	run, found := f.planRuns[runID]
	if !found || run.State != store.PlanRunning {
		return false, nil
	}
	run.State = state
	f.planRuns[runID] = run
	return true, nil
}

// A release's plan aims at the identifier the insert returned.
//
// `record` is composed before being written: its ID is zero until the insert returns it.
// Passing `record` as is when opening the plan named a release that does not exist — the
// run failed on the foreign key, with a fact key ending in `:0`, and the release stayed a
// draft with no build leaving.
//
// Nothing caught it: the existing tests checked what arrives in the queue, where a
// release identifier is not read.
func TestThePlanTargetsTheIdentifierReturnedByTheInsert(t *testing.T) {
	fake := newFakeStore()
	forge := &fakeForge{commit: "abc123def456"}
	handler := newReleasesHandler(t, fake).WithForge(forge, forge)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, asRole(
		httptest.NewRequest(http.MethodPost, "/releases",
			strings.NewReader(`{"tag":"3.2.0-rc1","kind":"pre","git_ref":"main"}`)),
		auth.RoleReleaser))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body.String())
	}

	if len(fake.planRuns) != 1 {
		t.Fatalf("%d plan run(s), expected 1", len(fake.planRuns))
	}
	for _, run := range fake.planRuns {
		if run.ReleaseID == nil || *run.ReleaseID == 0 {
			t.Errorf("release targeted = %v — zero names a release that does not exist",
				run.ReleaseID)
		}
		// The fact's key carries the same identifier: two spellings would open two runs for one
		// release.
		if strings.HasSuffix(run.EventKey, ":0") {
			t.Errorf("fact key %q — it carries a null identifier", run.EventKey)
		}
	}
}
