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
	history      []store.EnvironmentHistory
	audit        []string
	actions      map[string]string
	artifacts    []store.Artifact
	objects      map[string]string
	registry     Images
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

func (f *fakeReleaseStore) ListEnvironmentHistory(context.Context, string, int) ([]store.EnvironmentHistory, error) {
	return f.history, nil
}

func (f *fakeReleaseStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
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

// Get sert aussi de stockage objet : le handler n'en attend qu'une lecture.
func (f *fakeReleaseStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	content, found := f.objects[key]
	if !found {
		return nil, errors.New("objet absent")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeReleaseStore) Enqueue(_ context.Context, request store.JobRequest) (int64, error) {
	f.jobs = append(f.jobs, request)
	return int64(len(f.jobs)), nil
}

func newFakeStore() *fakeReleaseStore {
	return &fakeReleaseStore{
		releases: map[string]store.Release{},
		actions:  map[string]string{},
		objects:  map[string]string{},
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
	return NewReleasesHandler(store, pipelines, authorizer, store.registry, store, "portal")
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

// fakeRegistry rend un digest par tag connu, et échoue sur les autres — un tag
// de promotion mis en file n'existe pas encore dans le registre.
type fakeRegistry map[string]string

func (f fakeRegistry) Digest(_ context.Context, _, tag string) (string, error) {
	digest, found := f[tag]
	if !found {
		return "", errors.New("manifeste absent")
	}
	return digest, nil
}

// Le digest est ce qui prouve qu'une promotion n'a pas reconstruit : le tag
// suffixé et le tag d'origine portent le même.
func TestGetResolvesDigestsForEveryTag(t *testing.T) {
	fake := newFakeStore()
	fake.registry = fakeRegistry{"1.8.0-rc3": "sha256:aaa", "1.8.0": "sha256:aaa"}
	fake.releases["1.8.0"] = store.Release{
		ID: 8, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	fake.promotions = []store.Promotion{
		{ReleaseID: 8, EnvironmentID: "prod", ImageName: "portal", Tag: "1.8.0_prod"},
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0", nil)
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()
	handler.Get(recorder, asRole(request, auth.RoleReleaser))

	var payload releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("décodage: %v", err)
	}

	digests := map[string]string{}
	for _, image := range payload.Images {
		digests[image.Tag] = image.Digest
	}
	if digests["1.8.0-rc3"] != digests["1.8.0"] || digests["1.8.0"] == "" {
		t.Errorf("digests = %v, attendu identiques entre la RC et la finale", digests)
	}
	// Le tag de promotion n'est pas encore poussé : digest vide, et ce n'est pas
	// une erreur de lecture.
	if digests["1.8.0_prod"] != "" {
		t.Errorf("tag de promotion = %q, attendu sans digest", digests["1.8.0_prod"])
	}
	if payload.ImagesError != "" {
		t.Errorf("message d'erreur = %q, attendu aucun", payload.ImagesError)
	}
}

// Un registre absent ne doit pas rendre la page illisible, mais il doit se dire :
// « pas de digest » et « impossible de lire le digest » n'appellent pas la même
// réaction.
func TestGetSaysWhyDigestsAreMissing(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0-rc1"] = store.Release{
		ID: 9, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0-rc1", nil)
	request.SetPathValue("tag", "1.8.0-rc1")
	recorder := httptest.NewRecorder()
	handler.Get(recorder, asRole(request, auth.RoleViewer))

	var payload releasePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("code = %d: un registre absent ne doit pas casser la page", recorder.Code)
	}
	if payload.ImagesError == "" {
		t.Error("l'absence de digest doit être expliquée")
	}
	if len(payload.Images) == 0 {
		t.Error("les tags restent affichables sans leur digest")
	}
}

func TestUpdateActionsRecordsTheAuthor(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 10, Tag: "1.8.0", Kind: "final", State: "ready"}
	handler := newReleasesHandler(t, fake)

	body := `{"markdown":"- purge du cache"}`
	request := httptest.NewRequest(http.MethodPut, "/releases/1.8.0/actions", strings.NewReader(body))
	request.SetPathValue("tag", "1.8.0")
	recorder := httptest.NewRecorder()

	handler.UpdateActions(recorder, asRole(request, auth.RoleOperator))

	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	if fake.actions["1.8.0"] != "- purge du cache" {
		t.Errorf("texte = %q", fake.actions["1.8.0"])
	}
	// L'auteur est posé par le serveur, jamais par le client : ce texte finit
	// dans un document contractuel.
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
		t.Errorf("code = %d, attendu 404", recorder.Code)
	}
}

// Ce que le dashboard déclenche sort de lui : un tag poussé dans un registre
// survit à toute correction en base.
func TestOutgoingActionsAreAudited(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{
		ID: 11, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodPost, "/releases/1.8.0/promote/prod", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("environment", "prod")
	handler.Promote(httptest.NewRecorder(), asRole(request, auth.RoleReleaser))

	if len(fake.audit) != 1 || fake.audit[0] != "release.promote:1.8.0" {
		t.Errorf("journal = %v", fake.audit)
	}
}

// Le journal est une trace, pas une transaction : son échec ne doit pas annuler
// une promotion qui a bien été mise en file.
func TestAFailingAuditDoesNotBlockThePromotion(t *testing.T) {
	fake := &failingAuditStore{newFakeStore()}
	fake.releases["1.8.0"] = store.Release{
		ID: 12, Tag: "1.8.0", Kind: "final", State: "ready", ImageVersion: "1.8.0-rc3",
	}
	handler := newReleasesHandler(t, fake.fakeReleaseStore)
	handler.store = fake

	request := httptest.NewRequest(http.MethodPost, "/releases/1.8.0/promote/prod", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("environment", "prod")
	recorder := httptest.NewRecorder()
	handler.Promote(recorder, asRole(request, auth.RoleReleaser))

	if recorder.Code != http.StatusAccepted {
		t.Errorf("code = %d, attendu 202: %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.jobs) != 1 {
		t.Errorf("%d job(s) mis en file", len(fake.jobs))
	}
}

type failingAuditStore struct{ *fakeReleaseStore }

func (f *failingAuditStore) Audit(context.Context, string, string, string, map[string]any) error {
	return errors.New("journal indisponible")
}

func TestHistoryFollowsWhatWasRequested(t *testing.T) {
	fake := newFakeStore()
	fake.history = []store.EnvironmentHistory{
		{ReleaseTag: "1.8.0-rc2", ImageVersion: "1.8.0-rc2", Tag: "1.8.0-rc2_testing"},
		{ReleaseTag: "1.8.0-rc1", ImageVersion: "1.8.0-rc1", Tag: "1.8.0-rc1_testing"},
	}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/environments/qualif/history", nil)
	request.SetPathValue("environment", "qualif")
	recorder := httptest.NewRecorder()
	handler.History(recorder, asRole(request, auth.RoleViewer))

	var payload struct {
		History []historyPayload `json:"history"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if len(payload.History) != 2 || payload.History[0].Release != "1.8.0-rc2" {
		t.Errorf("historique = %+v", payload.History)
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
	// Le générateur part de la release, pas du tag : sans le rattachement il ne
	// saurait pas quoi produire.
	if job.Params[exportParamKind] != store.ArtifactDeliveryNote || job.Params[exportParamFormat] != "pdf" {
		t.Errorf("paramètres = %v", job.Params)
	}
	// Un export ne touche ni le registre ni le cluster : rien ne justifie de le
	// sérialiser avec des pipelines.
	if job.QueueClass != "parallel" {
		t.Errorf("classe de file = %q", job.QueueClass)
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
		t.Errorf("code = %d, attendu 400", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Error("aucun job ne doit partir pour un format inconnu")
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

// Sans ce contrôle, un identifiant deviné donnerait le bon de livraison d'une
// autre release — un document contractuel qui n'est pas le sien.
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
		t.Errorf("code = %d, attendu 404", recorder.Code)
	}
}

// La ligne survit à une purge du bucket : le dire vaut mieux qu'un fichier vide.
func TestDownloadReportsAMissingObject(t *testing.T) {
	fake := newFakeStore()
	fake.releases["1.8.0"] = store.Release{ID: 24, Tag: "1.8.0"}
	fake.artifacts = []store.Artifact{{ID: 9, ReleaseID: 24, Format: "pdf", Object: "purgé"}}
	handler := newReleasesHandler(t, fake)

	request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0/exports/9/download", nil)
	request.SetPathValue("tag", "1.8.0")
	request.SetPathValue("artifact", "9")
	recorder := httptest.NewRecorder()
	handler.Download(recorder, asRole(request, auth.RoleViewer))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code = %d, attendu 404", recorder.Code)
	}
}
