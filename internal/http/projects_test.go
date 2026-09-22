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
 * Declaring the repositories, seen from the API.
 *
 * Two things deserve to be held: refusing a malformed repository — an access test on `mse` would
 * fail and read as a permission problem although it is the entry that is at fault — and the fact
 * that an impossible test is not recorded as a failure.
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
	// keyPresent carries the three states of the verdict on the key. Nil is the default, and
	// that is deliberate: "the question could not be asked" must be the case a test forgets to
	// configure, not "the key is absent".
	keyPresent *bool
	// seenPublicKey keeps the key the handler passed on: it is what decides what the forge
	// went looking for.
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
		KeyNote:       "test check",
	}, nil
}

func trueFlag() *bool  { valeur := true; return &valeur }
func falseFlag() *bool { valeur := false; return &valeur }

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

// A repository without `owner/name` would fail the access check on a malformed URL, and
// the failure would read as a rights problem.
func TestAMalformedRepositoryIsRefused(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore())

	for _, repository := range []string{"mse", "", "CNOUS/", "/mse", "CNOUS/mse/sous"} {
		body := `{"repository":"` + repository + `"}`
		if code := createProject(t, handler, body).Code; code != http.StatusBadRequest {
			t.Errorf("repository %q: code %d, expected 400", repository, code)
		}
	}
}

// Obvious values are derived rather than required: stumbling over a label whose value goes
// without saying teaches nothing.
func TestTheDefaultsAreInferred(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake).WithForgeURL("https://forge.example.fr")

	if code := createProject(t, handler, `{"repository":"CNOUS/mse"}`).Code; code != http.StatusCreated {
		t.Fatalf("code %d", code)
	}

	project := fake.projects[1]
	if project.Label != "mse" {
		t.Errorf("label = %q, expected the repository's name", project.Label)
	}
	if project.ForgeURL != "https://forge.example.fr" {
		t.Errorf("forge = %q, expected the one from the configuration", project.ForgeURL)
	}
	if project.BuildContext != "." {
		t.Errorf("contexte = %q", project.BuildContext)
	}
}

func TestAnAlreadyDeclaredRepositoryReturns409(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore())

	createProject(t, handler, `{"repository":"CNOUS/mse"}`)
	if code := createProject(t, handler, `{"repository":"CNOUS/mse"}`).Code; code != http.StatusConflict {
		t.Errorf("code %d, expected 409", code)
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

// With no forge configured, the check does not happen. Recording it as a failure would
// send someone hunting for an access problem where there is only a missing token.
func TestAnImpossibleTestIsNotRecordedAsAFailure(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake)
	createProject(t, handler, `{"repository":"CNOUS/mse"}`)

	if code := probe(t, handler, "1").Code; code != http.StatusServiceUnavailable {
		t.Errorf("code %d, expected 503", code)
	}
	if len(fake.checks) != 0 {
		t.Errorf("check recorded with no test: %v", fake.checks)
	}
}

// An unreachable repository is a verdict, not a dashboard fault: the response is 200 and
// carries the reason, which the screen displays.
func TestAnUnreachableRepositoryIsACheck(t *testing.T) {
	fake := newFakeProjectStore()
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{err: errors.New("gitea: 404 on /repos/CNOUS/mse")})
	createProject(t, handler, `{"repository":"CNOUS/mse"}`)

	recorder := probe(t, handler, "1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d, expected 200: an unreachable repository is not an outage", recorder.Code)
	}

	var payload struct {
		OK   bool   `json:"ok"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if payload.OK || payload.Note == "" {
		t.Errorf("check = %+v — the reason must accompany the failure", payload)
	}
	if len(fake.checks) != 1 || fake.checks[0] {
		t.Errorf("constats = %v", fake.checks)
	}
}

func TestASuccessfulTestRecordsTheBranch(t *testing.T) {
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
		t.Errorf("note = %q, expected the default branch", fake.projects[1].CheckNote)
	}
}

// No shared key is returned with the projects any more.
//
// It used to be, and the screen displayed it to projects that did not yet have their own.
// Leaving it today would be worse than useless: it would invite pasting it into a
// repository, while nothing mounts it any more — the run refuses to leave without the
// project's key.
func TestNoSharedKeyIsReturnedWithTheProjects(t *testing.T) {
	handler := NewProjectsHandler(newFakeProjectStore()).
		WithForgeURL("https://forge.example.fr")

	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest(http.MethodGet, "/projects", nil))

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if _, present := payload["deploy_key"]; present {
		t.Errorf("a shared key is still returned: %v", payload["deploy_key"])
	}
	// With no prober, the screen must be able to disable the button rather than let someone
	// click a check that will not conclude.
	if configured, _ := payload["probe_configured"].(bool); configured {
		t.Error("the probe is announced although none is wired")
	}
}

// The tracker declaration survives editing the project.
//
// It did not, and it is the most expensive failure met on this screen: the form did not
// carry `tracker_project`, the update wrote it anyway — hence empty — and saving a
// project's label cleared its tracker declaration. Synchronisation then stopped without a
// word: no tracked project, so nothing to do, so no error.
//
// Observed locally: a project had lost its tracker identifier while its label was edited,
// and the versions screen offered to declare a version by hand as though it had never had
// a tracker.
func TestTheTrackedFlagSurvivesAModification(t *testing.T) {
	projects := newFakeProjectStore()
	handler := NewProjectsHandler(projects)

	recorder := createProject(t, handler, `{
		"repository": "vcarroy/app", "label": "App",
		"tracker_project": "mse-dev", "dockerfile": "Dockerfile"
	}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("creation: %d %s", recorder.Code, recorder.Body)
	}
	if projects.projects[1].TrackerProject != "mse-dev" {
		t.Fatalf("tracked at creation: %q", projects.projects[1].TrackerProject)
	}

	// The gesture that cleared everything: renaming the project.
	update := httptest.NewRequest(http.MethodPut, "/projects/1", strings.NewReader(`{
		"repository": "vcarroy/app", "label": "Application",
		"tracker_project": "mse-dev", "dockerfile": "Dockerfile"
	}`))
	update.SetPathValue("id", "1")
	recorder = httptest.NewRecorder()
	handler.Update(recorder, asAdmin(update))

	if recorder.Code != http.StatusOK {
		t.Fatalf("update: %d %s", recorder.Code, recorder.Body)
	}
	if got := projects.projects[1].TrackerProject; got != "mse-dev" {
		t.Errorf("tracked after modification: %q — synchronisation would stop", got)
	}
}

// The tracker is returned with the project, without which the form could not restate it:
// it would come back empty on every edit, and the empty field would clear the declaration
// as surely as its absence.
func TestTheTrackedFlagIsReturnedWithTheProject(t *testing.T) {
	projects := newFakeProjectStore()
	handler := NewProjectsHandler(projects)

	if recorder := createProject(t, handler, `{
		"repository": "vcarroy/app", "label": "App", "tracker_project": "mse-dev"
	}`); recorder.Code != http.StatusCreated {
		t.Fatalf("creation: %d", recorder.Code)
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
		t.Errorf("tracked returned: %+v", payload.Projects)
	}
}

/*
 * The observation about the deploy key.
 *
 * It replaces a human confirmation — "paste it into the repository, then say it is done". An
 * intention, with the defect of intentions: it stayed true after the key had been removed from
 * the repository, and the pipelines then failed on an SSH error that nothing connected to the
 * screen asserting the opposite.
 */

// projectWithKey declares a project whose key the worker has already set.
func projectWithKey(t *testing.T, fake *fakeProjectStore, active bool) {
	t.Helper()
	fake.projects[1] = store.Project{
		ID: 1, Repository: "CNOUS/mse", Slug: "mse", Label: "mse",
		DeployKeyPublic: "ssh-ed25519 AAAAC3Nz arkhitekton",
		DeployKeyActive: active,
	}
	fake.next = 2
}

// The check passes on the project's key: it is what the forge must look for.
func TestTheTestLooksForTheProjectsKey(t *testing.T) {
	fake := newFakeProjectStore()
	projectWithKey(t, fake, false)

	var vue string
	handler := NewProjectsHandler(fake).
		WithProbe(fakeProber{keyPresent: trueFlag(), vuePublicKey: &vue})

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if vue != "ssh-ed25519 AAAAC3Nz arkhitekton" {
		t.Errorf("key returned = %q", vue)
	}
}

// Found in the repository, the key becomes used — without anyone asserting it.
func TestAKeyFoundInTheRepositoryBecomesInUse(t *testing.T) {
	fake := newFakeProjectStore()
	projectWithKey(t, fake, false)
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{keyPresent: trueFlag()})

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if !fake.projects[1].DeployKeyActive {
		t.Error("the key found present was not kept")
	}
}

// Absent from the repository, it stops being used — the case a manual confirmation could
// not catch up: it stayed true indefinitely.
func TestAKeyRemovedFromTheRepositoryStopsBeingInUse(t *testing.T) {
	fake := newFakeProjectStore()
	projectWithKey(t, fake, true)
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{keyPresent: falseFlag()})

	if code := probe(t, handler, "1").Code; code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if fake.projects[1].DeployKeyActive {
		t.Error("the key missing from the repository is still announced as in use")
	}
}

// Silence overturns nothing.
//
// When the question could not be asked — a token with no administration right on the
// repository — the previous state is left as it is. Overwriting it to false would cut the
// builds of a project whose key may well be in place, for the sole reason that the
// dashboard may not read the repository's keys.
func TestAQuestionWithNoAnswerOverturnsNothing(t *testing.T) {
	fake := newFakeProjectStore()
	projectWithKey(t, fake, true)
	handler := NewProjectsHandler(fake).WithProbe(fakeProber{})

	recorder := probe(t, handler, "1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if !fake.projects[1].DeployKeyActive {
		t.Error("a check with no answer overturned the previous state")
	}

	// And the screen receives null, which it must say differently from an absence.
	var payload struct {
		KeyPresent *bool `json:"key_present"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("corps illisible: %v", err)
	}
	if payload.KeyPresent != nil {
		t.Errorf("key_present = %v, expected null", *payload.KeyPresent)
	}
}
