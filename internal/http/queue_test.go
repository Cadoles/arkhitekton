package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les files vues de l'API.
 *
 * Deux choses méritent d'être tenues ici : le regroupement — une file par type,
 * y compris vide, puisque c'est quand rien ne tourne qu'on vient vérifier ce
 * qu'une file autorise — et le refus d'un plafond qui immobiliserait la file.
 */

type fakeQueueStore struct {
	jobs     []store.Job
	releases []store.Release
	limits   map[string]int
	set      map[string]int
	// cleared keeps the returns to default: telling setting from undoing is what makes a
	// setting reversible.
	cleared []string
}

func newFakeQueueStore() *fakeQueueStore {
	return &fakeQueueStore{limits: map[string]int{}, set: map[string]int{}}
}

func (f *fakeQueueStore) Enqueue(context.Context, store.JobRequest) (int64, error) { return 1, nil }
func (f *fakeQueueStore) ListQueue(context.Context) ([]store.Job, error)           { return f.jobs, nil }
func (f *fakeQueueStore) GetJob(context.Context, int64) (store.Job, error) {
	return store.Job{}, store.ErrJobNotFound
}
func (f *fakeQueueStore) CancelQueuedJob(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (f *fakeQueueStore) RequestCancel(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (f *fakeQueueStore) Directory(context.Context) (map[string]string, error) {
	return map[string]string{"121": "vcarroy"}, nil
}
func (f *fakeQueueStore) QueueLimits(context.Context) (map[string]int, error) { return f.limits, nil }
func (f *fakeQueueStore) SetQueueLimit(_ context.Context, kind string, max int, _ string) error {
	f.set[kind] = max
	return nil
}
func (f *fakeQueueStore) ClearQueueLimit(_ context.Context, kind string) error {
	f.cleared = append(f.cleared, kind)
	return nil
}
func (f *fakeQueueStore) Audit(context.Context, string, string, string, map[string]any) error {
	return nil
}
func (f *fakeQueueStore) ListReleases(context.Context, int64, int) ([]store.Release, error) {
	return f.releases, nil
}

func queueHandler(fake *fakeQueueStore) *QueueHandler {
	return NewQueueHandler(fake, &catalog.Catalog{
		MaxConcurrentRuns: 10,
		Pipelines: []catalog.Pipeline{
			{
				Kind: "unit-tests", PipelineRef: "php-unit-tests", Label: "Tests unitaires",
				Params: map[string]catalog.Param{
					"revision": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestGitRef},
					// Optional, and with no nature: the case that must stay free-text, and which did not
					// appear at all before.
					"debug":    {Source: catalog.SourceInput},
					"repo-url": {Source: catalog.SourceConfig, Key: "forge.mse.repoUrl"},
				},
			},
			{
				Kind: "e2e-tests", PipelineRef: "e2e-test", Label: "Tests E2E", MaxConcurrent: 1,
				Params: map[string]catalog.Param{
					"image-tag": {Source: catalog.SourceInput, Required: true, Suggest: catalog.SuggestReleaseTag},
					// The reference list is built from what the catalogue declares: without this parameter no
					// image would be offered, which is the intended behaviour.
					"portal-image": {
						Source: catalog.SourceInput, Required: true,
						Suggest: catalog.SuggestImage, Image: "portal"},
				},
			},
		},
	}).WithSuggestions(fake, "reg.example.fr/mse", nil)
}

type queueListing struct {
	Queues []struct {
		Kind          string `json:"kind"`
		Label         string `json:"label"`
		MaxConcurrent int    `json:"max_concurrent"`
		DefaultMax    int    `json:"default_max_concurrent"`
		Overridden    bool   `json:"overridden"`
		Known         bool   `json:"known"`
		Running       int    `json:"running"`
		Waiting       int    `json:"waiting"`
		Jobs          []struct {
			ID          int64  `json:"id"`
			RequestedBy string `json:"requested_by"`
		} `json:"jobs"`
	} `json:"queues"`
	Kinds []struct {
		Kind   string `json:"kind"`
		Inputs []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
			Suggest  string `json:"suggest"`
		} `json:"inputs"`
	} `json:"kinds"`
	Suggestions   map[string][]struct{ Value, Label string } `json:"suggestions"`
	MaxConcurrent int                                        `json:"max_concurrent"`
}

func listQueues(t *testing.T, handler *QueueHandler) queueListing {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest(http.MethodGet, "/queue", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}

	var listing queueListing
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	return listing
}

// One queue per declared kind, busy or not.
func TestEachTypeHasItsOwnQueueEvenEmpty(t *testing.T) {
	fake := newFakeQueueStore()
	fake.jobs = []store.Job{
		{ID: 1, Kind: "unit-tests", State: store.JobRunning, RequestedBy: "121"},
		{ID: 2, Kind: "unit-tests", State: store.JobQueued, RequestedBy: "121"},
	}

	listing := listQueues(t, queueHandler(fake))

	if len(listing.Queues) != 2 {
		t.Fatalf("%d queue(s), expected one per declared type: %+v", len(listing.Queues), listing.Queues)
	}

	unit := listing.Queues[0]
	if unit.Kind != "unit-tests" || unit.Running != 1 || unit.Waiting != 1 {
		t.Errorf("file unit-tests = %+v", unit)
	}
	// Le plafond du catalogue, faute de réglage.
	if unit.MaxConcurrent != catalog.DefaultKindConcurrency || unit.Overridden {
		t.Errorf("cap = %d, set %v", unit.MaxConcurrent, unit.Overridden)
	}
	// The OIDC subject is translated: what one reads on a queue is who.
	if len(unit.Jobs) != 2 || unit.Jobs[0].RequestedBy != "vcarroy" {
		t.Errorf("jobs = %+v", unit.Jobs)
	}

	e2e := listing.Queues[1]
	if e2e.MaxConcurrent != 1 || len(e2e.Jobs) != 0 {
		t.Errorf("e2e queue = %+v — an empty queue stays displayed", e2e)
	}
}

// A setting replaces the catalogue's ceiling, which is still returned beside it: the
// screen must be able to offer going back to it.
func TestASettingIsReturnedWithItsDefault(t *testing.T) {
	fake := newFakeQueueStore()
	fake.limits["unit-tests"] = 2

	listing := listQueues(t, queueHandler(fake))

	unit := listing.Queues[0]
	if unit.MaxConcurrent != 2 || unit.DefaultMax != catalog.DefaultKindConcurrency || !unit.Overridden {
		t.Errorf("queue set = %+v", unit)
	}
}

// Local tasks do not become PipelineRuns and have no catalogue entry, but they do occupy a
// queue. Hiding them would make jobs that really are waiting disappear from the screen.
func TestAQueueOutsideTheCatalogueStaysDisplayed(t *testing.T) {
	fake := newFakeQueueStore()
	fake.jobs = []store.Job{{ID: 7, Kind: "delivery-note", State: store.JobQueued, RequestedBy: "121"}}

	listing := listQueues(t, queueHandler(fake))

	if len(listing.Queues) != 3 {
		t.Fatalf("%d queue(s), expected the catalogue's two plus the observed one", len(listing.Queues))
	}
	local := listing.Queues[2]
	if local.Kind != "delivery-note" || local.Known {
		t.Errorf("file locale = %+v", local)
	}
	if local.Waiting != 1 {
		t.Errorf("attente = %d", local.Waiting)
	}
}

func setLimit(t *testing.T, handler *QueueHandler, kind, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPut, "/queue/limits/"+kind, strings.NewReader(body))
	request.SetPathValue("kind", kind)
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleAdmin}))

	recorder := httptest.NewRecorder()
	handler.SetLimit(recorder, request)
	return recorder
}

// A zero or negative ceiling would immobilise the queue for ever, with nothing saying so.
// It is refused, like one exceeding what the cluster holds.
func TestAnOutOfBoundsCapIsRefused(t *testing.T) {
	fake := newFakeQueueStore()
	handler := queueHandler(fake)

	for _, body := range []string{`{"max_concurrent":0}`, `{"max_concurrent":-1}`, `{"max_concurrent":999}`} {
		recorder := setLimit(t, handler, "unit-tests", body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, expected 400", body, recorder.Code)
		}
	}
	if len(fake.set) != 0 {
		t.Errorf("an invalid cap was recorded: %v", fake.set)
	}

	if recorder := setLimit(t, handler, "unit-tests", `{"max_concurrent":3}`); recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if fake.set["unit-tests"] != 3 {
		t.Errorf("cap recorded = %v", fake.set)
	}
}

// Undoing a setting is a distinct operation from setting it back to its previous value:
// sending a number would not say "as the catalogue does", but "exactly this number", which
// would stop following the catalogue if it changed.
func TestReturningToTheDefaultClearsTheSetting(t *testing.T) {
	fake := newFakeQueueStore()
	handler := queueHandler(fake)

	request := httptest.NewRequest(http.MethodDelete, "/queue/limits/unit-tests", nil)
	request.SetPathValue("kind", "unit-tests")
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleAdmin}))

	recorder := httptest.NewRecorder()
	handler.ClearLimit(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if len(fake.cleared) != 1 || fake.cleared[0] != "unit-tests" {
		t.Errorf("settings cleared = %v", fake.cleared)
	}
}

// The launch form is populated from the catalogue and the database.
//
// Its fields used to be free-text: an empty revision forces going elsewhere to find a
// branch name and copy it, and the typo only surfaces when the clone fails.
func TestTheLaunchFormIsPopulated(t *testing.T) {
	fake := newFakeQueueStore()
	fake.releases = []store.Release{{Tag: "3.2.0-rc3"}, {Tag: "3.1.0"}}

	listing := listQueues(t, queueHandler(fake))

	if len(listing.Kinds) != 2 {
		t.Fatalf("%d type(s)", len(listing.Kinds))
	}

	// Required first, then alphabetical: a Go map has no order, and a form whose fields move
	// on every opening is unusable.
	unit := listing.Kinds[0].Inputs
	if len(unit) != 2 {
		t.Fatalf("unit-tests parameters = %+v — the optional ones count too", unit)
	}
	if unit[0].Name != "revision" || !unit[0].Required || unit[0].Suggest != catalog.SuggestGitRef {
		t.Errorf("first parameter = %+v", unit[0])
	}
	if unit[1].Name != "debug" || unit[1].Required || unit[1].Suggest != "" {
		t.Errorf("second parameter = %+v — an optional one with no kind stays free", unit[1])
	}

	// Release tags, and images composed with the registry's prefix. The browser does not know
	// that prefix: sending it so the browser can assemble them would amount to publishing it
	// for no gain.
	tags := listing.Suggestions[catalog.SuggestReleaseTag]
	if len(tags) != 2 || tags[0].Value != "3.2.0-rc3" {
		t.Errorf("tags offered = %+v", tags)
	}
	images := listing.Suggestions[catalog.Input{Suggest: catalog.SuggestImage, Image: "portal"}.SuggestionKey()]
	if len(images) != 2 || images[0].Value != "reg.example.fr/mse/portal:3.2.0-rc3" {
		t.Errorf("images offered = %+v", images)
	}
	// The environments come from the MSE plugin: without it none is offered and the field
	// stays free, even when the catalogue declares some.
	if environments, present := listing.Suggestions[catalog.SuggestEnvironment]; present {
		t.Errorf("environments offered with no plugin = %+v", environments)
	}

	proposed := []Suggestion{{Value: "dev", Label: "Unstable"}}
	withPlugin := listQueues(t, queueHandler(fake).WithEnvironmentSuggestions(
		func(context.Context, string) []Suggestion { return proposed }))
	if environments := withPlugin.Suggestions[catalog.SuggestEnvironment]; len(environments) != 1 ||
		environments[0].Value != "dev" {
		t.Errorf("environments offered by the plugin = %+v", environments)
	}

	// Git references are deliberately missing: they are read live from the forge, and
	// reloading them with the queue would make it pay every two seconds.
	if _, present := listing.Suggestions[catalog.SuggestGitRef]; present {
		t.Error("Git references must not travel through the queue")
	}
}

func launch(t *testing.T, handler *QueueHandler, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/queue", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "121", Username: "vcarroy", Role: auth.RoleOperator}))

	recorder := httptest.NewRecorder()
	handler.Launch(recorder, request)
	return recorder
}

// Extra parameters land in a Kubernetes object's spec: a fanciful name produces an
// unreadable validation error there rather than a clear refusal.
func TestAFancifulParameterNameIsRefused(t *testing.T) {
	handler := queueHandler(newFakeQueueStore())

	refused := launch(t, handler,
		`{"kind":"unit-tests","params":{"revision":"main","mauvais nom":"x"}}`)
	if refused.Code != http.StatusBadRequest {
		t.Errorf("code %d, expected 400", refused.Code)
	}

	// An acceptable name goes through, and the parameter need not be declared in the
	// catalogue: that is what makes a customised run possible.
	accepted := launch(t, handler, `{"kind":"unit-tests","params":{"revision":"main","debug-mode":"1"}}`)
	if accepted.Code != http.StatusAccepted {
		t.Errorf("code %d, expected 202", accepted.Code)
	}
}

// BuildableProject names the host project. One is enough here: these tests are about what
// the handler does with the project, not about choosing it.
func (f *fakeQueueStore) BuildableProject(context.Context) (store.Project, error) {
	return store.Project{ID: 1, Repository: "vcarroy/test", Dockerfile: "Dockerfile"}, nil
}

// ProjectBySlug resolves the project named in the path. One here, answering any slug:
// these tests are about what the handler does with the project, not about resolving it.
func (f *fakeQueueStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != testProjectSlug {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 1, Slug: slug, Repository: "vcarroy/test", Dockerfile: "Dockerfile"}, nil
}
