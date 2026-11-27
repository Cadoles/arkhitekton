package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/connect"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Declaring a version by hand.
 *
 * It exists because a project **with no tracker** had no way to start: its versions were born of
 * creating a release, whose form lives on the screen of a version that did not exist. An empty
 * screen, no button, no way out.
 */

type fakeVersionStore struct {
	project store.Project
	created []string
	audits  []string
}

func (f *fakeVersionStore) ListVersionSummaries(context.Context, int64) ([]store.VersionSummary, error) {
	return nil, nil
}

func (f *fakeVersionStore) GetVersionSummary(context.Context, int64) (store.VersionSummary, error) {
	return store.VersionSummary{}, store.ErrVersionNotFoundByID
}

func (f *fakeVersionStore) ListVersionIssues(context.Context, int64) ([]store.VersionIssue, error) {
	return nil, nil
}

func (f *fakeVersionStore) ListVersionReleases(context.Context, int64) ([]store.VersionRelease, error) {
	return nil, nil
}

func (f *fakeVersionStore) GetSyncState(context.Context, string, int64) (store.SyncState, error) {
	return store.SyncState{}, nil
}

func (f *fakeVersionStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != f.project.Slug {
		return store.Project{}, store.ErrProjectNotFound
	}
	return f.project, nil
}

func (f *fakeVersionStore) GetProject(context.Context, int64) (store.Project, error) {
	return f.project, nil
}

func (f *fakeVersionStore) EnsureVersion(_ context.Context, _ int64, name string, _ bool) (int64, error) {
	f.created = append(f.created, name)
	return int64(len(f.created)), nil
}

func (f *fakeVersionStore) Audit(_ context.Context, _, action, _ string, _ map[string]any) error {
	f.audits = append(f.audits, action)
	return nil
}

func declareVersion(t *testing.T, store *fakeVersionStore, body string, trackers ...Trackers) *httptest.ResponseRecorder {
	t.Helper()

	var tracker Trackers
	if len(trackers) > 0 {
		tracker = trackers[0]
	}
	handler := NewVersionsHandler(store, tracker)

	request := httptest.NewRequest(http.MethodPost, "/projects/app/versions",
		strings.NewReader(body))
	request.SetPathValue("project", "app")
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "vcarroy"}))

	recorder := httptest.NewRecorder()
	handler.Create(recorder, request)
	return recorder
}

func untracked() *fakeVersionStore {
	return &fakeVersionStore{project: store.Project{ID: 1, Slug: "app"}}
}

// The case that unblocks everything: a project with no tracker declares its first version.
func TestAProjectWithNoTrackerDeclaresItsVersion(t *testing.T) {
	projects := untracked()

	recorder := declareVersion(t, projects, `{"name":"1.8.0"}`)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body)
	}
	if len(projects.created) != 1 || projects.created[0] != "1.8.0" {
		t.Errorf("versions created: %v", projects.created)
	}
	// Recorded: it is the only thing that will say who decided a scope existed, since no
	// synchronisation will say it in its place.
	if len(projects.audits) != 1 || projects.audits[0] != store.AuditVersionDeclare {
		t.Errorf("journal: %v", projects.audits)
	}
}

// On a tracked project the declaration is refused: its versions belong to the tool, which is
// authoritative. A version created here would stay awaiting an adoption that would never
// come.
func TestATrackedProjectRefusesTheManualDeclaration(t *testing.T) {
	projects := untracked()

	recorder := declareVersion(t, projects, `{"name":"1.8.0"}`, fixedTracker{id: "redmine", project: "shop-dev"})

	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, expected 409: %s", recorder.Code, recorder.Body)
	}
	if len(projects.created) != 0 {
		t.Errorf("versions created: %v", projects.created)
	}
	// The message must say why, otherwise it will be taken for a fault.
	if !strings.Contains(recorder.Body.String(), "authoritative") {
		t.Errorf("body %s", recorder.Body)
	}
}

// An empty name names nothing, and letting it through would produce a version the screen
// would display with no title.
func TestAnEmptyVersionNameIsRefused(t *testing.T) {
	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{}`} {
		recorder := declareVersion(t, untracked(), body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, expected 400", body, recorder.Code)
		}
	}
}

// The shape is checked against the project's scheme, prefixes stripped: `v1.8.0` and `1.8.0`
// name the same version, and refusing the first would require knowing an internal convention
// to type a number.
func TestTheShapeIsCheckedAgainstTheProjectsScheme(t *testing.T) {
	if recorder := declareVersion(t, untracked(), `{"name":"v1.8.0"}`); recorder.Code != http.StatusCreated {
		t.Errorf("a prefixed name must be accepted: %d %s", recorder.Code, recorder.Body)
	}

	recorder := declareVersion(t, untracked(), `{"name":"not a version"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("code %d, expected 400", recorder.Code)
	}
}

// An opaque scheme accepts everything: refusing would impose a convention the project has not
// declared.
func TestAnOpaqueSchemeAcceptsAnyName(t *testing.T) {
	projects := untracked()
	projects.project.VersionScheme = "opaque"

	if recorder := declareVersion(t, projects, `{"name":"build-4711"}`); recorder.Code != http.StatusCreated {
		t.Errorf("code %d: %s", recorder.Code, recorder.Body)
	}
}

// The name created is the one typed, never the normalised form: that serves to compare, not
// to rename what the user wrote.
func TestTheNameTypedIsKeptAsItIs(t *testing.T) {
	projects := untracked()

	recorder := declareVersion(t, projects, `{"name":"v1.8.0"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("code %d", recorder.Code)
	}

	var payload struct{ Name string }
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if payload.Name != "v1.8.0" || projects.created[0] != "v1.8.0" {
		t.Errorf("name returned %q, created %v", payload.Name, projects.created)
	}
}

// The flag that decides whether the button is shown.
//
// The server-side refusal was tested, emitting the flag was not: a missing `tracked` in the
// body would make the interface's check true, and the button would appear on a tracked
// project — exactly what it must not do. The server would then refuse, but the user would
// have clicked into a wall.
func TestTheListSaysWhetherTheProjectIsTracked(t *testing.T) {
	cases := map[string]struct {
		tracker string
		want    bool
	}{
		"tracked project":         {tracker: "shop-dev", want: true},
		"project with no tracker": {tracker: "", want: false},
	}

	for name, expected := range cases {
		projects := untracked()

		handler := NewVersionsHandler(projects, fixedTracker{id: "redmine", project: expected.tracker})

		request := httptest.NewRequest(http.MethodGet, "/projects/app/versions", nil)
		request.SetPathValue("project", "app")
		recorder := httptest.NewRecorder()
		handler.List(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: code %d: %s", name, recorder.Code, recorder.Body)
		}

		var payload struct {
			Sync struct {
				Tracked bool `json:"tracked"`
			} `json:"sync"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: unreadable body: %v", name, err)
		}
		if payload.Sync.Tracked != expected.want {
			t.Errorf("%s: tracked = %v, expected %v — the button would show wrongly",
				name, payload.Sync.Tracked, expected.want)
		}
	}
}

// The screens name the tracker by its plugin: they say "Redmine" only of Redmine.
func TestTheListNamesTheTrackerPlugin(t *testing.T) {
	projects := untracked()
	request := httptest.NewRequest(http.MethodGet, "/projects/app/versions", nil)
	request.SetPathValue("project", "app")

	for tracker, want := range map[Trackers]string{fixedTracker{id: "forgery"}: "Suivi forgery", nil: ""} {
		recorder := httptest.NewRecorder()
		NewVersionsHandler(projects, tracker).List(recorder, request)
		var payload struct {
			Sync struct {
				Tracker *struct{ ID, Label string } `json:"tracker"`
			} `json:"sync"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unreadable body: %v", err)
		}
		got := ""
		if payload.Sync.Tracker != nil {
			got = payload.Sync.Tracker.Label
		}
		if got != want {
			t.Errorf("tracker = %q, expected %q", got, want)
		}
	}
}

// suggestingStore is a version `3.4.0` with its releases in the dashboard.
type suggestingStore struct {
	fakeVersionStore
	releases []string
}

func (s *suggestingStore) GetVersionSummary(context.Context, int64) (store.VersionSummary, error) {
	return store.VersionSummary{ID: 7, ProjectID: 1, Name: "v3.4.0"}, nil
}

func (s *suggestingStore) ListVersionReleases(context.Context, int64) ([]store.VersionRelease, error) {
	releases := make([]store.VersionRelease, 0, len(s.releases))
	for _, tag := range s.releases {
		releases = append(releases, store.VersionRelease{Tag: tag, Kind: "pre"})
	}
	return releases, nil
}

// taggedForge is a repository carrying tags.
type taggedForge struct {
	provider.Forge
	tags []string
}

func (f taggedForge) ListTags(context.Context) ([]provider.Ref, bool, error) {
	refs := make([]provider.Ref, 0, len(f.tags))
	for _, tag := range f.tags {
		refs = append(refs, provider.Ref{Name: tag})
	}
	return refs, false, nil
}

type tagForges struct{ forge provider.Forge }

func (f tagForges) Forge(context.Context, int64) (provider.Forge, error) { return f.forge, nil }

func (f tagForges) TagWriter(context.Context, int64) (provider.TagWriter, error) {
	return nil, connect.ErrNotConfigured
}

func suggested(t *testing.T, handler *VersionsHandler) (map[string]string, bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/projects/app/versions/7/suggested-tags", nil)
	request.SetPathValue("project", "app")
	request.SetPathValue("id", "7")
	recorder := httptest.NewRecorder()
	handler.SuggestedTags(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d: %s", recorder.Code, recorder.Body)
	}
	var body struct {
		Tags map[string]string `json:"suggested_tags"`
		Read bool              `json:"forge_read"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Tags, body.Read
}

// The repository's candidates count: a tag set before the dashboard, or by hand, is taken, and
// the next candidate follows the highest of them all.
func TestTheSuggestionCountsTheRepositorysTags(t *testing.T) {
	versions := &suggestingStore{
		fakeVersionStore: fakeVersionStore{project: store.Project{ID: 1, Slug: "app", ReleaseKinds: release.DefaultCycle()}},
		releases:         []string{"3.4.0-rc.1"},
	}

	tags, read := suggested(t, NewVersionsHandler(versions, nil))
	if read || tags["pre"] != "3.4.0-rc.2" {
		t.Errorf("without a forge: %v, read %v", tags, read)
	}

	forge := taggedForge{tags: []string{"3.4.0-rc.9", "3.4.0-rc.10", "3.5.0-rc.3", "3.3.0"}}
	tags, read = suggested(t, NewVersionsHandler(versions, nil).WithForges(tagForges{forge: forge}))
	if !read || tags["pre"] != "3.4.0-rc.11" || tags["final"] != "3.4.0" {
		t.Errorf("with the forge: %v, read %v", tags, read)
	}
}
