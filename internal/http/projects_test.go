package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * La déclaration des dépôts, vue de l'API.
 *
 * Deux choses méritent d'être tenues : le refus d'un dépôt malformé — un test
 * d'accès sur `mse` échouerait et se lirait comme un défaut de droits alors que
 * c'est la saisie qui est fautive — et le fait qu'un test impossible ne se
 * consigne pas en échec.
 */

type fakeProjectStore struct {
	projects  map[int64]store.Project
	next      int64
	checks    []bool
	audits    []string
	rotations []int64
}

func (f *fakeProjectStore) ActivateDeployKey(_ context.Context, id int64, active bool) error {
	project, known := f.projects[id]
	if !known {
		return store.ErrProjectNotFound
	}
	project.DeployKeyActive = active
	f.projects[id] = project
	return nil
}

func (f *fakeProjectStore) RequestKeyRotation(_ context.Context, id int64) error {
	if _, known := f.projects[id]; !known {
		return store.ErrProjectNotFound
	}
	f.rotations = append(f.rotations, id)
	return nil
}

func newFakeProjectStore() *fakeProjectStore {
	return &fakeProjectStore{projects: map[int64]store.Project{}, next: 1}
}

func (f *fakeProjectStore) ListProjects(context.Context) ([]store.Project, error) {
	list := make([]store.Project, 0, len(f.projects))
	for _, project := range f.projects {
		list = append(list, project)
	}
	return list, nil
}

func (f *fakeProjectStore) GetProject(_ context.Context, id int64) (store.Project, error) {
	project, found := f.projects[id]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return project, nil
}

func (f *fakeProjectStore) CreateProject(_ context.Context, project store.Project) (int64, error) {
	for _, existing := range f.projects {
		if existing.Repository == project.Repository {
			return 0, store.ErrProjectExists
		}
	}
	id := f.next
	f.next++
	project.ID = id
	f.projects[id] = project
	return id, nil
}

func (f *fakeProjectStore) UpdateProject(_ context.Context, project store.Project) error {
	existing, found := f.projects[project.ID]
	if !found {
		return store.ErrProjectNotFound
	}
	project.Repository = existing.Repository
	f.projects[project.ID] = project
	return nil
}

func (f *fakeProjectStore) DeleteProject(_ context.Context, id int64) error {
	if _, found := f.projects[id]; !found {
		return store.ErrProjectNotFound
	}
	delete(f.projects, id)
	return nil
}

func (f *fakeProjectStore) RecordProjectCheck(_ context.Context, id int64, ok bool, note string) error {
	project, found := f.projects[id]
	if !found {
		return store.ErrProjectNotFound
	}
	project.CheckOK = &ok
	project.CheckNote = note
	f.projects[id] = project
	f.checks = append(f.checks, ok)
	return nil
}

func (f *fakeProjectStore) Audit(_ context.Context, _, action, _ string, _ map[string]any) error {
	f.audits = append(f.audits, action)
	return nil
}

type fakeProber struct {
	err error
	// keyPresent porte les trois états du constat sur la clé. Nil est le défaut,
	// et c'est délibéré : « la question n'a pas pu être posée » doit être le cas
	// qu'un test oublie de configurer, pas « la clé est absente ».
	keyPresent *bool
	// vuePublicKey retient la clé que le handler a transmise : c'est elle qui
	// décide de ce que la forge est allée chercher.
	vuePublicKey *string
}

func (p fakeProber) Probe(_ context.Context, _, publicKey string) (provider.Probe, error) {
	if p.vuePublicKey != nil {
		*p.vuePublicKey = publicKey
	}
	if p.err != nil {
		return provider.Probe{}, p.err
	}
	return provider.Probe{
		DefaultBranch: "main",
		KeyPresent:    p.keyPresent,
		KeyNote:       "constat de test",
	}, nil
}

func vrai() *bool { valeur := true; return &valeur }
func faux() *bool { valeur := false; return &valeur }

func asAdmin(request *http.Request) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleAdmin}))
}

func createProject(t *testing.T, handler *ProjectsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.Create(recorder, asAdmin(httptest.NewRequest(http.MethodPost, "/projects",
		strings.NewReader(body))))
	return recorder
}

// Un dépôt sans `owner/nom` ferait échouer le test d'accès sur une URL
// malformée, et l'échec se lirait comme un défaut de droits.
func TestUnDepotMalformeEstRefuse(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore())

	for _, repository := range []string{"mse", "", "CNOUS/", "/mse", "CNOUS/mse/sous"} {
		body := `{"repository":"` + repository + `"}`
		if code := createProject(t, handler, body).Code; code != http.StatusBadRequest {
			t.Errorf("dépôt %q: code %d, attendu 400", repository, code)
		}
	}
}

// Les valeurs évidentes sont déduites plutôt qu'exigées : buter sur un libellé
// dont la valeur va de soi n'apprend rien.
func TestLesDefautsSontDeduits(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake).WithForgeURL("https://forge.example.fr")

	if code := createProject(t, handler, `{"repository":"CNOUS/mse"}`).Code; code != http.StatusCreated {
		t.Fatalf("code %d", code)
	}

	project := fake.projects[1]
	if project.Label != "mse" {
		t.Errorf("libellé = %q, attendu le nom du dépôt", project.Label)
	}
	if project.ForgeURL != "https://forge.example.fr" {
		t.Errorf("forge = %q, attendue celle de la configuration", project.ForgeURL)
	}
	if project.BuildContext != "." {
		t.Errorf("contexte = %q", project.BuildContext)
	}
}

func TestUnDepotDejaDeclareRend409(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore())

	createProject(t, handler, `{"repository":"CNOUS/mse"}`)
	if code := createProject(t, handler, `{"repository":"CNOUS/mse"}`).Code; code != http.StatusConflict {
		t.Errorf("code %d, attendu 409", code)
	}
}

func probe(t *testing.T, handler *ProjectsHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/projects/"+id+"/probe", nil)
	request.SetPathValue("id", id)
	recorder := httptest.NewRecorder()
	handler.Probe(recorder, asAdmin(request))
	return recorder
}

// Sans forge configurée, le test n'a pas lieu. Le consigner en échec ferait
// chercher un défaut d'accès là où il n'y a qu'un jeton absent.
func TestUnTestImpossibleNeSeConsignePasEnEchec(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake)
	createProject(t, handler, `{"repository":"CNOUS/mse"}`)

	if code := probe(t, handler, "1").Code; code != http.StatusServiceUnavailable {
		t.Errorf("code %d, attendu 503", code)
	}
	if len(fake.checks) != 0 {
		t.Errorf("constat enregistré sans test: %v", fake.checks)
	}
}

// Un dépôt injoignable est un constat, pas une panne du dashboard : la réponse
// est 200 et porte le motif, que l'écran affiche.
func TestUnDepotInjoignableEstUnConstat(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{err: errors.New("gitea: 404 sur /repos/CNOUS/mse")})
	createProject(t, handler, `{"repository":"CNOUS/mse"}`)

	recorder := probe(t, handler, "1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d, attendu 200: un dépôt injoignable n'est pas une panne", recorder.Code)
	}

	var payload struct {
		OK   bool   `json:"ok"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if payload.OK || payload.Note == "" {
		t.Errorf("constat = %+v — le motif doit accompagner l'échec", payload)
	}
	if len(fake.checks) != 1 || fake.checks[0] {
		t.Errorf("constats = %v", fake.checks)
	}
}

func TestUnTestReussiConsigneLaBranche(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{})
	createProject(t, handler, `{"repository":"CNOUS/mse"}`)

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if len(fake.checks) != 1 || !fake.checks[0] {
		t.Fatalf("constats = %v", fake.checks)
	}
	if !strings.Contains(fake.projects[1].CheckNote, "main") {
		t.Errorf("note = %q, attendue la branche par défaut", fake.projects[1].CheckNote)
	}
}

// Aucune clé **partagée** n'est plus rendue avec les projets.
//
// Elle l'était, et l'écran l'affichait aux projets qui n'avaient pas encore la
// leur. La laisser aujourd'hui serait pire qu'inutile : elle inviterait à la
// coller dans un dépôt, alors que plus rien ne la monte — le run refuse de partir
// sans la clé du projet.
func TestAucuneClePartageeNestRendueAvecLesProjets(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore()).
		WithForgeURL("https://forge.example.fr")

	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest(http.MethodGet, "/projects", nil))

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if _, present := payload["deploy_key"]; present {
		t.Errorf("une clé partagée est encore rendue: %v", payload["deploy_key"])
	}
	// Sans sonde, l'écran doit pouvoir désactiver le bouton plutôt que de
	// laisser cliquer sur un test qui ne conclura pas.
	if configured, _ := payload["probe_configured"].(bool); configured {
		t.Error("la sonde est annoncée alors qu'aucune n'est branchée")
	}
}

// La déclaration de suivi survit à une modification du projet.
//
// Elle n'y survivait pas, et c'est la panne la plus coûteuse rencontrée sur cet
// écran : le formulaire ne portait pas `tracker_project`, l'update l'écrivait
// quand même — donc à la valeur vide —, et **enregistrer le libellé d'un projet
// effaçait sa déclaration de suivi**. La synchronisation s'arrêtait alors sans
// un mot : plus de projet suivi, donc plus rien à faire, donc aucune erreur.
//
// Constaté en production locale : le projet MSE avait perdu son `mse-dev` en
// éditant son libellé, et l'écran des versions proposait de déclarer une version
// à la main comme s'il n'avait jamais eu de Redmine.
func TestLaDeclarationDeSuiviSurvitAUneModification(t *testing.T) {
	projects := newFakeProjectStore()
	handler := NewProjectsHandler(projects)

	recorder := createProject(t, handler, `{
		"repository": "vcarroy/app", "label": "App",
		"tracker_project": "mse-dev", "dockerfile": "Dockerfile"
	}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("création: %d %s", recorder.Code, recorder.Body)
	}
	if projects.projects[1].TrackerProject != "mse-dev" {
		t.Fatalf("suivi à la création: %q", projects.projects[1].TrackerProject)
	}

	// Le geste qui effaçait tout : renommer le projet.
	update := httptest.NewRequest(http.MethodPut, "/projects/1", strings.NewReader(`{
		"repository": "vcarroy/app", "label": "Application",
		"tracker_project": "mse-dev", "dockerfile": "Dockerfile"
	}`))
	update.SetPathValue("id", "1")
	recorder = httptest.NewRecorder()
	handler.Update(recorder, asAdmin(update))

	if recorder.Code != http.StatusOK {
		t.Fatalf("mise à jour: %d %s", recorder.Code, recorder.Body)
	}
	if got := projects.projects[1].TrackerProject; got != "mse-dev" {
		t.Errorf("suivi après modification: %q — la synchronisation s'arrêterait", got)
	}
}

// Le suivi est **rendu** avec le projet, sans quoi le formulaire ne pourrait pas
// le repréciser : il repartirait vide à chaque édition, et le champ vide
// effacerait la déclaration aussi sûrement que son absence.
func TestLeSuiviEstRenduAvecLeProjet(t *testing.T) {
	projects := newFakeProjectStore()
	handler := NewProjectsHandler(projects)

	if recorder := createProject(t, handler, `{
		"repository": "vcarroy/app", "label": "App", "tracker_project": "mse-dev"
	}`); recorder.Code != http.StatusCreated {
		t.Fatalf("création: %d", recorder.Code)
	}

	recorder := httptest.NewRecorder()
	handler.List(recorder, asAdmin(httptest.NewRequest(http.MethodGet, "/projects", nil)))

	var payload struct {
		Projects []struct {
			TrackerProject string `json:"tracker_project"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("corps illisible: %v", err)
	}
	if len(payload.Projects) != 1 || payload.Projects[0].TrackerProject != "mse-dev" {
		t.Errorf("suivi rendu: %+v", payload.Projects)
	}
}

/*
 * Le constat sur la clé de déploiement.
 *
 * Il remplace une confirmation humaine — « collez-la dans le dépôt, puis dites
 * que c'est fait ». Une intention, avec le défaut des intentions (B8) : elle
 * restait vraie après qu'on avait retiré la clé du dépôt, et les pipelines
 * échouaient alors sur une erreur SSH que rien ne rattachait à l'écran qui
 * affirmait le contraire.
 */

// projetAvecCle déclare un projet dont le worker a déjà posé la clé.
func projetAvecCle(t *testing.T, fake *fakeProjectStore, active bool) {
	t.Helper()
	fake.projects[1] = store.Project{
		ID: 1, Repository: "CNOUS/mse", Slug: "mse", Label: "mse",
		DeployKeyPublic: "ssh-ed25519 AAAAC3Nz arkhitekton",
		DeployKeyActive: active,
	}
	fake.next = 2
}

// Le test transmet la clé du projet : c'est elle que la forge doit chercher.
func TestLeTestCherIcheLaCleDuProjet(t *testing.T) {
	fake := newFakeProjectStore()
	projetAvecCle(t, fake, false)

	var vue string
	handler := NewProjectsHandler(fake).
		WithProbe(fakeProber{keyPresent: vrai(), vuePublicKey: &vue})

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if vue != "ssh-ed25519 AAAAC3Nz arkhitekton" {
		t.Errorf("clé transmise = %q", vue)
	}
}

// Trouvée dans le dépôt, la clé devient employée — sans que personne l'affirme.
func TestUneCleTrouveeDansLeDepotDevientEmployee(t *testing.T) {
	fake := newFakeProjectStore()
	projetAvecCle(t, fake, false)
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{keyPresent: vrai()})

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if !fake.projects[1].DeployKeyActive {
		t.Error("la clé constatée présente n'a pas été retenue")
	}
}

// Absente du dépôt, elle cesse de l'être — c'est le cas qu'une confirmation
// manuelle ne savait pas rattraper : elle restait vraie indéfiniment.
func TestUneCleRetireeDuDepotCesseDetreEmployee(t *testing.T) {
	fake := newFakeProjectStore()
	projetAvecCle(t, fake, true)
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{keyPresent: faux()})

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if fake.projects[1].DeployKeyActive {
		t.Error("la clé absente du dépôt est toujours annoncée employée")
	}
}

// Le silence ne renverse rien.
//
// Quand la question n'a pas pu être posée — jeton sans droit d'administration sur
// le dépôt —, l'état précédent est laissé tel quel. L'écraser à faux couperait
// les constructions d'un projet dont la clé est peut-être en place, pour la seule
// raison que le dashboard n'a pas le droit de lire les clés du dépôt.
func TestUneQuestionSansReponseNeRenverseRien(t *testing.T) {
	fake := newFakeProjectStore()
	projetAvecCle(t, fake, true)
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{})

	recorder := probe(t, handler, "1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if !fake.projects[1].DeployKeyActive {
		t.Error("un constat sans réponse a renversé l'état précédent")
	}

	// Et l'écran reçoit `null`, qu'il doit dire autrement qu'une absence.
	var payload struct {
		KeyPresent *bool `json:"key_present"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("corps illisible: %v", err)
	}
	if payload.KeyPresent != nil {
		t.Errorf("key_present = %v, attendu null", *payload.KeyPresent)
	}
}
