package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// fakeReleaseStore remplace la base : les décisions testées ici sont celles du
// handler, et les faire dépendre d'un PostgreSQL les rendrait plus lentes sans
// les rendre plus sûres.
type fakeReleaseStore struct {
	releases     map[string]store.Release
	environments []store.Environment
	jobs         []store.JobRequest
	promotions   []store.Promotion
}

func (f *fakeReleaseStore) EnsureVersion(context.Context, string) (int64, error) { return 1, nil }

func (f *fakeReleaseStore) CreateRelease(_ context.Context, release store.Release) (int64, error) {
	release.ID = int64(len(f.releases) + 10)
	f.releases[release.Tag] = release
	return release.ID, nil
}

func (f *fakeReleaseStore) GetRelease(_ context.Context, tag string) (store.Release, error) {
	release, found := f.releases[tag]
	if !found {
		return store.Release{}, store.ErrReleaseNotFound
	}
	return release, nil
}

func (f *fakeReleaseStore) ListReleases(context.Context, int) ([]store.Release, error) {
	var releases []store.Release
	for _, release := range f.releases {
		releases = append(releases, release)
	}
	return releases, nil
}

func (f *fakeReleaseStore) RecordPromotion(_ context.Context, promotion store.Promotion) error {
	f.promotions = append(f.promotions, promotion)
	return nil
}

func (f *fakeReleaseStore) ListPromotions(context.Context, int64) ([]store.Promotion, error) {
	return f.promotions, nil
}

func (f *fakeReleaseStore) ListEnvironments(context.Context) ([]store.Environment, error) {
	return f.environments, nil
}

func (f *fakeReleaseStore) ListEnvironmentViews(context.Context) ([]store.EnvironmentView, error) {
	var views []store.EnvironmentView
	for _, environment := range f.environments {
		views = append(views, store.EnvironmentView{Environment: environment})
	}
	return views, nil
}

func (f *fakeReleaseStore) LatestPromotions(context.Context) (map[string]store.EnvironmentPromotion, error) {
	return nil, nil
}

func (f *fakeReleaseStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	f.jobs = append(f.jobs, request)
	return int64(len(f.jobs)), nil
}

func newFakeStore() *fakeReleaseStore {
	return &fakeReleaseStore{
		releases: map[string]store.Release{},
		environments: []store.Environment{
			{ID: "qualif", Label: "Qualification", PromotionSuffix: "testing",
				AcceptsPrerelease: true, Rank: 2, Observable: true, Visible: true},
			{ID: "prod", Label: "Production", PromotionSuffix: "prod",
				AcceptsPrerelease: false, Rank: 4, Observable: false, Visible: true},
		},
	}
}

func newReleasesHandler(t *testing.T, store *fakeReleaseStore) *ReleasesHandler {
	t.Helper()
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "promote", PipelineRef: "promote-image", QueueClass: "exclusive-registry"},
	}}
	authorizer := auth.NewAuthorizer(map[string]auth.EnvironmentPolicy{
		"prod": {Sensitive: true}, "preprod": {Sensitive: true},
	})
	return NewReleasesHandler(store, pipelines, authorizer, "portal")
}

func asRole(request *http.Request, role auth.Role) *http.Request {
	identity := auth.Identity{Subject: "vcarroy", Username: "vcarroy", Role: role}
	return request.WithContext(context.WithValue(request.Context(), identityKey, identity))
}

// Le cas qui coûte le plus cher : pousser vers la production un tag qu'aucune
// ImagePolicy ne retient ne produit aucune erreur. Le refus doit donc venir
// d'ici, avant la mise en file.
func TestPromoteRefusesAPrereleaseTowardsProduction(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc1"] = store.Release{
		ID: 1, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPost, "/releases/1.8.0-rc1/promote/prod", nil)
	request.SetPathValue("tag", "1.8.0-rc1")
	request.SetPathValue("environment", "prod")
	recorder := httptest.NewRecorder()

	handler.Promote(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusConflict {
		t.Errorf("code = %d, attendu 409", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Errorf("%d job(s) mis en file alors que la promotion est refusée", len(fake.jobs))
	}
}

// Une release finale produite par re-tag doit atteindre la production, et le tag
// poussé doit être composé depuis son tag — non depuis la version de son image,
// restée celle de la RC.
func TestPromoteComposesFromTheReleaseTagNotTheImageVersion(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{
		ID: 2, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPost, "/releases/1.8.0/promote/prod", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("environment", "prod")
	recorder := httptest.NewRecorder()

	handler.Promote(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("code = %d, attendu 202: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.jobs) != 1 {
		t.Fatalf("%d job(s) mis en file", len(fake.jobs))
	}
	job := fake.jobs[0]
	if job.Params["new-tag"] != "1.8.0_prod" {
		t.Errorf("new-tag = %q, attendu 1.8.0_prod", job.Params["new-tag"])
	}
	// Le re-tag part de ce qui existe réellement dans le registre.
	if job.Params["old-tag"] != "1.8.0-rc3" {
		t.Errorf("old-tag = %q, attendu 1.8.0-rc3", job.Params["old-tag"])
	}
	if job.QueueClass != "exclusive-registry" {
		t.Errorf("classe de file = %q: deux re-tags concurrents sur le même dépôt", job.QueueClass)
	}
	// La promotion est enregistrée **demandée**, sans digest : le re-tag n'a pas
	// encore eu lieu.
	if len(fake.promotions) != 1 || fake.promotions[0].Digest != "" {
		t.Errorf("promotions = %+v", fake.promotions)
	}
}

func TestPromoteRefusesAReleaseWithoutImage(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.9.0-rc1"] = store.Release{
		ID: 3, Tag: "1.9.0-rc1", Kind: "pre", State: "draft", ImageVersion: "1.9.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPost, "/releases/1.9.0-rc1/promote/qualif", nil)
	request.SetPathValue("tag", "1.9.0-rc1")
	request.SetPathValue("environment", "qualif")
	recorder := httptest.NewRecorder()

	handler.Promote(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusConflict {
		t.Errorf("code = %d, attendu 409 pour une release non construite", recorder.Code)
	}
}

// Créer `1.8.0-rc1` en la déclarant finale produit une release que la production
// ignorera silencieusement. Le refus doit être immédiat.
func TestCreateRefusesAKindThatContradictsTheTag(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0-rc1","kind":"final"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, attendu 400", recorder.Code)
	}
}

func TestCreateRefusesAMismatchedRedmineVersion(t *testing.T) {
	fake := newFakeStore()
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0-rc1","kind":"pre","redmine_version":"1.9.0"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code = %d, attendu 400: la jointure avec Redmine serait rompue", recorder.Code)
	}
}

// La release finale hérite de l'image de sa RC, et son propre tag doit exister
// dans le registre pour qu'une ImagePolicy puisse le retenir.
func TestCreateFinalRetagsFromItsSource(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc3"] = store.Release{
		ID: 4, Tag: "1.8.0-rc3", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc3",
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
		t.Errorf("version d'image = %q, attendu celle de la RC", created.ImageVersion)
	}
	if created.State != "ready" {
		t.Errorf("état = %q: l'image existe déjà, la release est promouvable", created.State)
	}

	if len(fake.jobs) != 1 {
		t.Fatalf("%d job(s) de re-tag", len(fake.jobs))
	}
	if fake.jobs[0].Params["old-tag"] != "1.8.0-rc3" || fake.jobs[0].Params["new-tag"] != "1.8.0" {
		t.Errorf("re-tag = %+v", fake.jobs[0].Params)
	}
}

// Une RC dont l'image n'est pas construite ne peut pas être re-taguée : il n'y a
// rien à promouvoir.
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
		t.Errorf("code = %d, attendu 400", recorder.Code)
	}
}

// Livrer une version finale engage ce qui part chez l'utilisateur : la nature est
// dans le corps, donc c'est au handler de la contrôler.
func TestCreateFinalRequiresTheReleaserRole(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc3"] = store.Release{
		ID: 6, Tag: "1.8.0-rc3", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake)

	body := `{"tag":"1.8.0","kind":"final","source_tag":"1.8.0-rc3"}`
	request := httptest.NewRequest(http.MethodPost, "/releases", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, asRole(request, auth.RoleOperator))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("code = %d, attendu 403", recorder.Code)
	}
}

// L'interface a besoin de savoir non seulement qu'une cible est refusée, mais
// pourquoi : un bouton grisé sans motif se lit comme une panne.
func TestGetExplainsWhyATargetIsRefused(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc1"] = store.Release{
		ID: 7, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0-rc1", nil)
	request.SetPathValue("tag", "1.8.0-rc1")
	recorder := httptest.NewRecorder()

	handler.Get(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d", recorder.Code)
	}

	var payload releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("décodage: %v", err)
	}

	targets := map[string]promotionTargetVw{}
	for _, target := range payload.Targets {
		targets[target.Environment] = target
	}
	if qualif := targets["qualif"]; !qualif.Allowed || qualif.Tag != "1.8.0-rc1_testing" {
		t.Errorf("qualif = %+v, attendu autorisée avec le suffixe Flux", qualif)
	}
	if prod := targets["prod"]; prod.Allowed || prod.Reason == "" {
		t.Errorf("prod = %+v, attendu refusée avec un motif", prod)
	}
}
