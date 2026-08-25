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
	deployed     map[string]store.DeployedRelease
	artifacts    []store.Artifact
	objects      map[string]string
	registry     Images
	states       map[int64]string
	// project décrit ce que la release construit. Nil, la fixture ci-dessous
	// s'applique : les tests portent sur le nombre d'exécutions enfilées et sur
	// leurs paramètres, pas sur le contenu de la table.
	project *store.Project
}

// fixtureProject reproduit ce que l'amorçage installe : trois étapes, dont une
// construite sans être promue.
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

func (f *fakeReleaseStore) BuildableProject(context.Context) (store.Project, error) {
	if f.project != nil {
		return *f.project, nil
	}
	return fixtureProject(), nil
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

func (f *fakeReleaseStore) DeployedRelease(_ context.Context, environmentID string) (store.DeployedRelease, error) {
	deployed, found := f.deployed[environmentID]
	if !found {
		return store.DeployedRelease{}, store.ErrNoDeployedRelease
	}
	return deployed, nil
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

// states retient les états écrits, pour vérifier qu'une pré-release passe bien en
// `building` après mise en file de sa construction.
// MarkReleased applique la même condition que la base : l'écriture n'aboutit que
// si la release est encore dans l'état lu.
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

func newFakeStore() *fakeReleaseStore {
	return &fakeReleaseStore{
		releases: map[string]store.Release{},
		actions:  map[string]string{},
		objects:  map[string]string{},
		deployed: map[string]store.DeployedRelease{},
		environments: []store.Environment{
			{ID: "qualif", Label: "Qualification", PromotionSuffix: "testing",
				AcceptsPrerelease: true, Rank: 2, Observable: true, Visible: true},
			{ID: "prod", Label: "Production", PromotionSuffix: "prod",
				AcceptsPrerelease: false, Rank: 4, Observable: false, Visible: true},
		},
	}
}

// fakeForge résout et pose des tags, en retenant ce qu'on lui a demandé.
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
		{Kind: "build", PipelineRef: "build-image"},
	}}
	authorizer := auth.NewAuthorizer(map[string]auth.EnvironmentPolicy{
		"prod": {Sensitive: true}, "preprod": {Sensitive: true},
	})
	return NewReleasesHandler(store, pipelines, authorizer, store.registry, store,
		"portal", "reg.cadoles.com/vcarroy")
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
	// Une exécution par image promue : le pipeline de re-tag n'en traite qu'une.
	if len(fake.jobs) != len(fixtureProject().Promotable()) {
		t.Fatalf("%d job(s) mis en file, attendu %d", len(fake.jobs), len(fixtureProject().Promotable()))
	}
	job := fake.jobs[0]
	if job.Params["image-name"] != "portal" {
		t.Errorf("première image promue = %q", job.Params["image-name"])
	}
	if job.Params["new-tag"] != "1.8.0_prod" {
		t.Errorf("new-tag = %q, attendu 1.8.0_prod", job.Params["new-tag"])
	}
	// Le re-tag part de ce qui existe réellement dans le registre.
	if job.Params["old-tag"] != "1.8.0-rc3" {
		t.Errorf("old-tag = %q, attendu 1.8.0-rc3", job.Params["old-tag"])
	}
	// Une ligne de promotion par image, et non une seule. La table les distingue
	// par son nom d'image ; n'en écrire qu'une laissait `portal-cli` promue sans
	// trace, donc sans moyen de dire que son re-tag avait échoué.
	if len(fake.promotions) != len(fixtureProject().Promotable()) {
		t.Fatalf("promotions = %+v", fake.promotions)
	}
	recorded := map[string]store.Promotion{}
	for _, promotion := range fake.promotions {
		recorded[promotion.ImageName] = promotion
	}
	for _, target := range fixtureProject().Promotable() {
		name := target.Image
		promotion, found := recorded[name]
		if !found {
			t.Errorf("aucune promotion enregistrée pour %s", name)
			continue
		}
		// Enregistrée **demandée**, sans digest : le re-tag n'a pas encore eu lieu.
		if promotion.Digest != "" || promotion.Tag != "1.8.0_prod" {
			t.Errorf("promotion de %s = %+v", name, promotion)
		}
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
		t.Errorf("version d'image = %q, attendu celle de la RC", created.ImageVersion)
	}
	if created.State != "ready" {
		t.Errorf("état = %q: l'image existe déjà, la release est promouvable", created.State)
	}

	retags := fake.jobsOfKind("retag")
	if len(retags) != len(fixtureProject().Promotable()) {
		t.Fatalf("%d job(s) de re-tag, attendu %d", len(retags), len(fixtureProject().Promotable()))
	}
	if retags[0].Params["old-tag"] != "1.8.0-rc3" || retags[0].Params["new-tag"] != "1.8.0" {
		t.Errorf("re-tag = %+v", retags[0].Params)
	}
	// La note est composée pour toute release, finale comprise : ce qu'elle
	// apporte au client ne dépend pas de la façon dont son image a été produite.
	if len(fake.jobsOfKind(ReleaseNotesJobKind)) != 1 {
		t.Error("la composition de la note devrait être mise en file")
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
		ID: 6, Tag: "1.8.0-rc3", Kind: "pre", State: "released", ImageVersion: "1.8.0-rc3",
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
	if len(fake.jobs) != len(fixtureProject().Promotable()) {
		t.Errorf("%d job(s) mis en file, attendu %d", len(fake.jobs), len(fixtureProject().Promotable()))
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

// Le chaînon qui manquait : créer une pré-release doit mettre sa construction en
// file et l'annoncer. Sans cela la release restait `draft` indéfiniment, le
// pipeline existant dans le cluster mais rien ne le déclenchant.
func TestUnePrereleaseEnfileSaConstruction(t *testing.T) {
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

	// Une exécution par image : le pipeline n'en construit qu'une, et c'est le
	// dashboard qui décide combien il en lance.
	builds := fake.jobsOfKind("build")
	if len(builds) != len(fixtureProject().Buildable()) {
		t.Fatalf("%d construction(s) en file, attendu %d", len(builds), len(fixtureProject().Buildable()))
	}
	for index, target := range fixtureProject().Buildable() {
		if builds[index].Params["image-name"] != target.Image ||
			builds[index].Params["target"] != target.Stage {
			t.Errorf("construction %d = %+v, attendu %s/%s",
				index, builds[index].Params, target.Image, target.Stage)
		}
	}
	job := builds[0]
	if len(fake.jobsOfKind(ReleaseNotesJobKind)) != 1 {
		t.Error("la composition de la note devrait être mise en file")
	}
	// `version` porte le tag SemVer et prime sur `tag-name` : sans lui, la Task
	// dériverait le tag de la branche et l'image ne porterait pas la version de
	// la release qui l'a demandée (B17, P10).
	if job.Params["version"] != "3.2.0-rc1" {
		t.Errorf("version transmise %q", job.Params["version"])
	}
	if job.Params["revision"] != "develop" {
		t.Errorf("révision transmise %q", job.Params["revision"])
	}
	if job.ReleaseID == nil {
		t.Error("le job doit porter sa release, sans quoi sa conclusion ne fera rien avancer")
	}

	var response struct {
		BuildJobIDs []int64 `json:"build_job_ids"`
		Release     struct {
			State string `json:"state"`
		} `json:"release"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if len(response.BuildJobIDs) != len(fixtureProject().Buildable()) {
		t.Errorf("%d identifiant(s) de construction rendus, attendu %d",
			len(response.BuildJobIDs), len(fixtureProject().Buildable()))
	}
	if response.Release.State != "building" {
		t.Errorf("état rendu %q, attendu building", response.Release.State)
	}
}

// Une release finale ne construit rien : c'est un re-tag, et lui réclamer une
// construction ferait tourner un pipeline pour produire une image qui existe
// déjà (B17).
func TestUneReleaseFinaleNenfilePasDeConstruction(t *testing.T) {
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
			t.Error("une release finale ne doit pas enfiler de construction")
		}
	}
}

// Une pré-release sans référence Git est créée quand même, et la réserve le dit :
// transmettre une référence vide ferait échouer le clone plusieurs minutes plus
// tard, sur un message qui ne désignerait pas la cause.
func TestUnePrereleaseSansReferenceEstCreeeAvecReserve(t *testing.T) {
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
		t.Fatalf("réponse illisible: %v", err)
	}
	if len(response.Warnings) == 0 {
		t.Error("la réserve doit être rendue: la release existe mais rien ne se construit")
	}
	for _, job := range fake.jobs {
		if job.Kind == "build" {
			t.Error("aucune construction ne doit partir sans référence Git")
		}
	}
}

// La validation est la décision humaine du cycle : elle fait passer `ready` à
// `released`, ce qui autorise la promotion et le re-tag.
func TestValiderUneReleasePreteLaRendLivrable(t *testing.T) {
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
		t.Errorf("état %q, attendu released", fake.releases["3.2.0-rc1"].State)
	}
	// La décision engage ce qui partira : elle est consignée.
	if len(fake.audit) == 0 {
		t.Error("la validation doit être consignée au journal d'audit")
	}
}

// Valider n'a de sens que depuis `ready` : rien à valider avant les tests, plus
// rien à décider après. Le refus est un 409 et non un 400 — la requête est
// correcte, c'est l'état qui ne s'y prête pas.
func TestValiderDepuisUnAutreEtatEstRefuse(t *testing.T) {
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
			t.Errorf("état %q: code %d, attendu 409", state, recorder.Code)
		}
		if fake.releases["3.2.0-rc1"].State != state {
			t.Errorf("état %q modifié en %q", state, fake.releases["3.2.0-rc1"].State)
		}
	}
}

// Relancer après un échec reconstruit depuis la même référence, sans créer de
// version : faire porter au numéro de version l'historique des pannes du cluster
// serait absurde.
func TestRelancerUneConstructionEchoueeReutiliseLaMemeReference(t *testing.T) {
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
		t.Errorf("révision %q, attendu celle de la release", fake.jobs[0].Params["revision"])
	}
	if fake.jobs[0].Params["version"] != "3.2.0-rc1" {
		t.Errorf("version %q", fake.jobs[0].Params["version"])
	}
	if fake.releases["3.2.0-rc1"].State != "building" {
		t.Errorf("état %q, attendu building", fake.releases["3.2.0-rc1"].State)
	}
}

// Une release finale n'a rien à reconstruire : son image est celle de sa RC
// source, et un pipeline produirait une seconde image sous le même tag.
func TestUneReleaseFinaleNeSeReconstruitPas(t *testing.T) {
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
		t.Errorf("code %d, attendu 409", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Errorf("aucun job ne devait partir: %+v", fake.jobs)
	}
}

// Reconstruire une release déjà livrée écraserait une image en service. La table
// de transitions l'interdit, et le handler s'y tient.
func TestUneReleaseLivreeNeSeReconstruitPas(t *testing.T) {
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
		t.Errorf("code %d, attendu 409", recorder.Code)
	}
	if len(fake.jobs) != 0 {
		t.Errorf("aucun job ne devait partir: %+v", fake.jobs)
	}
}

// Une release doit désigner un commit, pas une branche : la branche bouge, et
// reconstruire six mois plus tard produirait une autre image (B17).
func TestUnePrereleaseEstAncreeSurUnCommitEtTaguee(t *testing.T) {
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

	// La branche est résolue en commit, et le tag posé sur ce commit — jamais sur
	// la branche, qui désignerait autre chose une minute plus tard.
	if len(forge.resolved) != 1 || forge.resolved[0] != "main" {
		t.Errorf("références résolues: %v", forge.resolved)
	}
	if len(forge.tags) != 1 || forge.tags[0] != "3.2.0-rc1@abc123def456" {
		t.Errorf("tags posés: %v", forge.tags)
	}

	// La construction part du **commit**, pas du tag ni de la branche : un clone
	// superficiel ne crée pas la référence locale d'un tag, et le checkout que la
	// Task fait ensuite échouerait.
	builds := fake.jobsOfKind("build")
	if len(builds) != len(fixtureProject().Buildable()) {
		t.Fatalf("jobs: %+v", fake.jobs)
	}
	if got := builds[0].Params["revision"]; got != "abc123def456" {
		t.Errorf("révision construite %q, attendu le commit", got)
	}
	// Le tag reste porté par `version`, qui devient le tag de l'image (B17, P10).
	if got := builds[0].Params["version"]; got != "3.2.0-rc1" {
		t.Errorf("version %q, attendu le tag de la release", got)
	}
}

// Sans jeton capable d'écrire, la release existe quand même : elle est ancrée sur
// son commit et la réserve le dit. Refuser priverait de toute la chaîne pour un
// tag qu'on peut poser ensuite.
func TestSansEcritureLaReleaseResteAncreeSurSonCommit(t *testing.T) {
	fake := newFakeStore()
	forge := &fakeForge{commit: "abc123def456"}
	// Forge branchée en lecture seule : pas de tagger.
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
		t.Fatalf("réponse illisible: %v", err)
	}
	if len(response.Warnings) == 0 {
		t.Error("l'absence de tag doit être signalée")
	}

	// La construction part du commit : immuable, même sans tag.
	if got := fake.jobs[0].Params["revision"]; got != "abc123def456" {
		t.Errorf("révision construite %q, attendu le commit résolu", got)
	}
}

// Forge injoignable : la release est créée, la réserve dit qu'elle n'est pas
// reproductible, et la construction retombe sur la référence saisie.
func TestUneForgeInjoignableNempechePasLaCreation(t *testing.T) {
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
		t.Error("aucun tag ne doit être posé sans commit résolu")
	}
	if got := fake.jobs[0].Params["revision"]; got != "main" {
		t.Errorf("révision construite %q, attendu le repli sur la référence saisie", got)
	}
}

// Une release finale n'est ni ancrée ni taguée ici : c'est un re-tag, son commit
// est celui de sa RC source, déjà taguée à sa propre création.
func TestUneReleaseFinaleNestPasTagueeUneSecondeFois(t *testing.T) {
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
		t.Errorf("une finale ne doit ni résoudre ni taguer: résolus=%v tags=%v",
			forge.resolved, forge.tags)
	}
}

/*
 * Les lectures que la composition des pastilles ajoute au magasin.
 *
 * Muettes : ces tests portent sur la création, la validation et la promotion
 * d'une release, pas sur son score. Rendre des valeurs vides fait que la
 * pastille ne s'affiche pas, ce qui est exactement le comportement attendu d'une
 * release sans résultats.
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

// La note de release : ces tests ne la couvrent pas, la détection étant éprouvée
// dans `internal/worker`. Une note vide est le comportement attendu d'une
// release dont aucune demande n'a été rattachée.
// jobsOfKind rend les jobs d'une nature donnée.
//
// Les tests désignent le job qu'ils examinent par sa nature plutôt que par sa
// position : toute création en enfile désormais deux — la construction ou le
// re-tag, et la composition de la note — et un indice figé se serait cassé au
// premier ajout suivant.
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

func (f *fakeReleaseStore) LinkNoteIssue(context.Context, int64, int64, string, string, string) error {
	return nil
}

func (f *fakeReleaseStore) UnlinkNoteIssue(context.Context, int64, int64) error {
	return nil
}

func (f *fakeReleaseStore) ListReleaseVersionIssues(context.Context, int64) ([]store.NoteEntry, error) {
	return nil, nil
}

// L'annuaire des noms d'acteurs. Vide : ces tests portent sur des comportements,
// pas sur l'affichage, et un annuaire vide fait retomber sur le sujet — le
// comportement exact d'un acteur inconnu.
func (f *fakeReleaseStore) Directory(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
