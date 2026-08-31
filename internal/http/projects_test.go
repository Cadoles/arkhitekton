package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
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

type fakeProber struct{ err error }

func (p fakeProber) Probe(context.Context, string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return "main", nil
}

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
	handler := NewProjectsHandler(fake).WithDeployKey("", "https://forge.example.fr")

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

// La clé publique est affichée telle quelle : c'est ce qu'on colle dans les
// réglages d'un dépôt. Sa moitié privée, elle, ne quitte jamais le secret.
func TestLaClePubliqueEstRendueAvecLesProjets(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore()).
		WithDeployKey("ssh-ed25519 AAAAC3Nz dashboard", "https://forge.example.fr")

	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest(http.MethodGet, "/projects", nil))

	var payload struct {
		DeployKey       string `json:"deploy_key"`
		ProbeConfigured bool   `json:"probe_configured"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if payload.DeployKey != "ssh-ed25519 AAAAC3Nz dashboard" {
		t.Errorf("clé = %q", payload.DeployKey)
	}
	// Sans sonde, l'écran doit pouvoir désactiver le bouton plutôt que de
	// laisser cliquer sur un test qui ne conclura pas.
	if payload.ProbeConfigured {
		t.Error("la sonde est annoncée alors qu'aucune n'est branchée")
	}
}
