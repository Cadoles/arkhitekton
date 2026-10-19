package mse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

const projectSlug = "test"

type fakeStore struct {
	releases     map[string]store.Release
	environments []storage.Environment
	jobs         []store.JobRequest
	promotions   []storage.Promotion
	history      []storage.EnvironmentHistory
	audit        []string
	auditErr     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		releases: map[string]store.Release{},
		environments: []storage.Environment{
			{ID: "qualif", Label: "Qualification", Rank: 2, Observable: true, Visible: true,
				DeployerSettings: map[string]string{
					flux.SettingSuffix: "testing", flux.SettingAcceptsPrerelease: "true",
				}},
			{ID: "prod", Label: "Production", Rank: 4, Observable: false, Visible: true,
				DeployerSettings: map[string]string{
					flux.SettingSuffix: "prod", flux.SettingAcceptsPrerelease: "false",
				}},
		},
	}
}

// fixtureProject carries three images, one of them built without being promoted.
func fixtureProject() store.Project {
	promoted, held := true, false
	return store.Project{
		Slug: projectSlug, Repository: "vcarroy/mse", Dockerfile: "Dockerfile",
		Targets: []store.Target{
			{Stage: "web", Image: "portal", Promote: &promoted},
			{Stage: "cli", Image: "portal-cli", Promote: &promoted},
			{Stage: "dev", Image: "portal-dev", Promote: &held},
		},
	}
}

func (f *fakeStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != projectSlug {
		return store.Project{}, store.ErrProjectNotFound
	}
	return fixtureProject(), nil
}

func (f *fakeStore) GetProject(context.Context, int64) (store.Project, error) {
	return fixtureProject(), nil
}

func (f *fakeStore) GetRelease(_ context.Context, _ int64, tag string) (store.Release, error) {
	item, found := f.releases[tag]
	if !found {
		return store.Release{}, store.ErrReleaseNotFound
	}
	return item, nil
}

func (f *fakeStore) ListEnvironments(context.Context, int64) ([]storage.Environment, error) {
	return f.environments, nil
}

func (f *fakeStore) ListEnvironmentViews(context.Context, int64) ([]storage.EnvironmentView, error) {
	views := make([]storage.EnvironmentView, 0, len(f.environments))
	for _, environment := range f.environments {
		views = append(views, storage.EnvironmentView{Environment: environment})
	}
	return views, nil
}

func (f *fakeStore) LatestPromotions(context.Context, int64) (map[string]storage.EnvironmentPromotion, error) {
	return nil, nil
}

func (f *fakeStore) ListEnvironmentHistory(context.Context, int64, string, int) ([]storage.EnvironmentHistory, error) {
	return f.history, nil
}

func (f *fakeStore) DeployedRelease(context.Context, int64, string) (storage.DeployedRelease, error) {
	return storage.DeployedRelease{}, storage.ErrNoDeployedRelease
}

func (f *fakeStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	f.jobs = append(f.jobs, request)
	return int64(len(f.jobs)), nil
}

func (f *fakeStore) RecordPromotion(_ context.Context, promotion storage.Promotion) error {
	f.promotions = append(f.promotions, promotion)
	return nil
}

func (f *fakeStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.audit = append(f.audit, action+":"+target)
	return nil
}

func newHandler(fake *fakeStore) *Handler {
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{{Kind: "core/retag", PipelineRef: "retag-image"}}}
	authorizer := auth.NewAuthorizer(auth.ProjectPolicies{projectSlug: {
		"prod": {Sensitive: true}, "preprod": {Sensitive: true},
	}})
	// The Flux deployer, with no client: planning needs none.
	return NewHandler(fake, pipelines, authorizer,
		flux.NewDeployer(nil, flux.DefaultPromotionKind, ""), quietLogger())
}

func asRole(request *http.Request, role auth.Role) *http.Request {
	identity := auth.Identity{Subject: "vcarroy", Username: "vcarroy", Role: role}
	request.SetPathValue("project", projectSlug)
	return request.WithContext(dashhttp.ContextWithIdentity(request.Context(), identity))
}

func promote(handler *Handler, tag, environment string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/releases/"+tag+"/promote/"+environment, nil)
	request.SetPathValue("tag", tag)
	request.SetPathValue("environment", environment)
	recorder := httptest.NewRecorder()
	handler.Promote(recorder, asRole(request, auth.RoleReleaser))
	return recorder
}

// Pushing to production a tag no ImagePolicy keeps produces no error. The refusal must
// therefore come from here, before the queueing.
func TestPromoteRefusesAPrereleaseTowardsProduction(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc1"] = store.Release{
		ID: 1, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
	}

	recorder := promote(newHandler(fake), "1.8.0-rc1", "prod")

	if recorder.Code != http.StatusConflict {
		t.Errorf("code = %d, expected 409", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Errorf("%d job(s) queued although the promotion is refused", len(fake.jobs))
	}
}

// A final release produced by re-tag reaches production, and the tag pushed is composed from
// its tag — not from its image's version.
func TestPromoteComposesFromTheReleaseTagNotTheImageVersion(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{
		ID: 2, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}

	recorder := promote(newHandler(fake), "1.8.0", "prod")

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code = %d, expected 202: %s", recorder.Code, recorder.Body.String())
	}
	promotable := fixtureProject().Promotable()
	if len(fake.jobs) != len(promotable) {
		t.Fatalf("%d job(s) queued, expected %d", len(fake.jobs), len(promotable))
	}
	job := fake.jobs[0]
	if job.Params["image-name"] != "portal" || job.Params["new-tag"] != "1.8.0_prod" ||
		job.Params["old-tag"] != "1.8.0-rc3" {
		t.Errorf("parameters = %+v", job.Params)
	}
	// One promotion row per image, recorded as **requested**, with no digest.
	if len(fake.promotions) != len(promotable) {
		t.Fatalf("promotions = %+v", fake.promotions)
	}
	for index, promotion := range fake.promotions {
		if promotion.ImageName != promotable[index].Image || promotion.Digest != "" ||
			promotion.Tag != "1.8.0_prod" {
			t.Errorf("promotion = %+v", promotion)
		}
	}
	if len(fake.audit) != 1 || fake.audit[0] != "release.promote:1.8.0" {
		t.Errorf("journal = %v", fake.audit)
	}
}

func TestPromoteRefusesAReleaseWithoutImage(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.9.0-rc1"] = store.Release{
		ID: 3, Tag: "1.9.0-rc1", Kind: "pre", State: "draft", ImageVersion: "1.9.0-rc1",
	}

	if recorder := promote(newHandler(fake), "1.9.0-rc1", "qualif"); recorder.Code != http.StatusConflict {
		t.Errorf("code = %d, expected 409 for an unbuilt release", recorder.Code)
	}
}

// The log is a trace, not a transaction: its failure does not cancel a queued promotion.
func TestAFailingAuditDoesNotBlockThePromotion(t *testing.T) {
	fake := newFakeStore()
	fake.auditErr = errors.New("journal indisponible")
	fake.releases["1.8.0"] = store.Release{
		ID: 12, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}

	recorder := promote(newHandler(fake), "1.8.0", "prod")

	if recorder.Code != http.StatusAccepted {
		t.Errorf("code = %d, expected 202: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.jobs) != len(fixtureProject().Promotable()) {
		t.Errorf("%d job(s) mis en file", len(fake.jobs))
	}
}

// The interface needs to know not only that a target is refused, but why: a greyed-out button
// with no reason reads as an outage.
func TestTargetsExplainWhyATargetIsRefused(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc1"] = store.Release{
		ID: 7, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
	}

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0-rc1/targets", nil)
	request.SetPathValue("tag", "1.8.0-rc1")
	recorder := httptest.NewRecorder()
	newHandler(fake).Targets(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d", recorder.Code)
	}
	var payload struct {
		Targets []promotionTargetVw `json:"targets"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	targets := map[string]promotionTargetVw{}
	for _, target := range payload.Targets {
		targets[target.Environment] = target
	}
	if qualif := targets["qualif"]; !qualif.Allowed || qualif.Tag != "1.8.0-rc1_testing" {
		t.Errorf("qualif = %+v, expected allowed with the Flux suffix", qualif)
	}
	if prod := targets["prod"]; prod.Allowed || prod.Reason == "" {
		t.Errorf("prod = %+v, expected refused with a reason", prod)
	}
}

func TestHistoryFollowsWhatWasRequested(t *testing.T) {
	fake := newFakeStore()
	fake.history = []storage.EnvironmentHistory{
		{ReleaseTag: "1.8.0-rc2", ImageVersion: "1.8.0-rc2", Tag: "1.8.0-rc2_testing"},
		{ReleaseTag: "1.8.0-rc1", ImageVersion: "1.8.0-rc1", Tag: "1.8.0-rc1_testing"},
	}

	request := httptest.NewRequest(http.MethodGet, "/environments/qualif/history", nil)
	request.SetPathValue("environment", "qualif")
	recorder := httptest.NewRecorder()
	newHandler(fake).History(recorder, asRole(request, auth.RoleViewer))

	var payload struct {
		History []historyPayload `json:"history"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.History) != 2 || payload.History[0].Release != "1.8.0-rc2" {
		t.Errorf("historique = %+v", payload.History)
	}
}
