package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/secrets"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The plugins' settings.
 *
 * A screen that receives API keys from a browser: the tests therefore bear first on what must
 * **never** come out, then on the two ways of losing a value — erasing it while believing one
 * changes nothing, and refusing it while believing one validates it.
 */

const testKey = "sN0Xk1t2Qv8yZ3aB4cD5eF6gH7iJ8kL9mN0oP1qR2sQ="

type fakePluginStore struct {
	global    map[string]store.PluginSettings
	byProject map[int64]map[string]store.PluginSettings
	cleared   []string
	audit     []string
}

func newFakePluginStore() *fakePluginStore {
	return &fakePluginStore{
		global: map[string]store.PluginSettings{
			"redmine":          {PluginID: "redmine", Enabled: true, Config: map[string]string{}},
			"gitea":            {PluginID: "gitea", Enabled: true, Config: map[string]string{}},
			"mse":              {PluginID: "mse", Enabled: true, Config: map[string]string{}},
			"bon-de-livraison": {PluginID: "bon-de-livraison", Enabled: true, Config: map[string]string{}},
		},
		byProject: map[int64]map[string]store.PluginSettings{},
	}
}

func (f *fakePluginStore) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return f.global, nil
}

func (f *fakePluginStore) SetPluginSettings(_ context.Context, entry store.PluginSettings, actor string) error {
	entry.UpdatedBy = actor
	f.global[entry.PluginID] = entry
	return nil
}

func (f *fakePluginStore) ProjectPluginSettings(_ context.Context, projectID int64) (map[string]store.PluginSettings, error) {
	if own, declared := f.byProject[projectID]; declared {
		return own, nil
	}
	return map[string]store.PluginSettings{}, nil
}

func (f *fakePluginStore) SetProjectPluginSettings(
	_ context.Context, projectID int64, entry store.PluginSettings, actor string,
) error {
	entry.UpdatedBy = actor
	if f.byProject[projectID] == nil {
		f.byProject[projectID] = map[string]store.PluginSettings{}
	}
	f.byProject[projectID][entry.PluginID] = entry
	return nil
}

func (f *fakePluginStore) ClearProjectPluginSettings(_ context.Context, projectID int64, pluginID string) error {
	delete(f.byProject[projectID], pluginID)
	f.cleared = append(f.cleared, pluginID)
	return nil
}

func (f *fakePluginStore) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	if slug != "mse" {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: 7, Slug: "mse"}, nil
}

func (f *fakePluginStore) Audit(_ context.Context, _, action, target string, _ map[string]any) error {
	f.audit = append(f.audit, action+":"+target)
	return nil
}

func pluginsHandlerWith(t *testing.T, fake *fakePluginStore, key string) *PluginsHandler {
	t.Helper()
	var cipher *secrets.Cipher
	if key != "" {
		built, err := secrets.New(key)
		if err != nil {
			t.Fatalf("cipher: %v", err)
		}
		cipher = built
	}
	return NewPluginsHandler(fake, cipher, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func putPlugin(t *testing.T, handler http.HandlerFunc, path, plugin, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := asAdmin(httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
	request.SetPathValue("plugin", plugin)
	if project := projectOf(path); project != "" {
		request.SetPathValue("project", project)
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// projectOf extracts the slug from a `/projects/<slug>/...` path.
func projectOf(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "projects" {
		return parts[1]
	}
	return ""
}

func decodePlugin(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unreadable response: %v — %s", err, recorder.Body.String())
	}
	return payload
}

/*
 * What never comes out.
 */

// A recorded API key is never displayed again — neither in the write's response nor in
// the list.
//
// That is what makes typing a secret from a browser acceptable: reading it back is not
// enough to steal it. The screen knows only that it is there.
func TestAnApiKeyIsNeverDisplayedAgain(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, testKey)

	recorder := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"url":"https://redmine.example.fr","api_key":"production-key"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d — %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "production-key") {
		t.Fatalf("the key shows through in the response: %s", recorder.Body.String())
	}

	listed := httptest.NewRecorder()
	handler.List(listed, httptest.NewRequest(http.MethodGet, "/admin/plugins", nil))
	if strings.Contains(listed.Body.String(), "production-key") {
		t.Fatalf("the key shows through in the list: %s", listed.Body.String())
	}

	// Its presence, though, is readable: without it the screen could not tell a field never
	// filled in from a field filled in that it does not display.
	payload := decodePlugin(t, recorder)
	names, _ := payload["secrets_set"].([]any)
	if len(names) != 1 || names[0] != "api_key" {
		t.Errorf("sensitive fields set = %v", names)
	}
}

// What is written to the database is sealed, and reopens with the key.
func TestTheValueWrittenToTheDatabaseIsSealed(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, testKey)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"production-key"}}`)

	stored := fake.global["redmine"].Config["api_key"]
	if stored == "" || strings.Contains(stored, "production-key") {
		t.Fatalf("stored value = %q", stored)
	}

	cipher, _ := secrets.New(testKey)
	opened, err := cipher.Open(stored)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if opened != "production-key" {
		t.Errorf("opened value = %q", opened)
	}
}

// With no sealing key, writing a sensitive field is refused — and the message names the
// missing variable.
//
// Refused rather than written in the clear: this is the only place where the distinction
// is made, and a save that "succeeds" by storing plaintext would be the worst outcome.
func TestWithoutASealingKeyTheSensitiveFieldIsRefused(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, "")

	recorder := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"production-key"}}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, expected 409 — %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "SECRETS_KEY") {
		t.Errorf("message %q — it must name the variable", recorder.Body.String())
	}
	if _, written := fake.global["redmine"].Config["api_key"]; written {
		t.Error("the key was written despite the refusal")
	}

	// The rest stays settable: a deployment without sealing must be able to enable a plugin
	// and type its ordinary fields.
	ok := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"url":"https://redmine.example.fr"}}`)
	if ok.Code != http.StatusOK {
		t.Errorf("code %d — %s", ok.Code, ok.Body.String())
	}
}

/*
 * The two ways of losing a value.
 */

// A sensitive field left empty is kept, not cleared.
//
// The screen never receives the key, so it cannot send it back: treating that as a
// deletion would lose the key the first time any other field was saved — editing a URL
// would be enough to cut synchronisation.
func TestASensitiveFieldLeftEmptyIsKept(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, testKey)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"production-key"}}`)
	sealed := fake.global["redmine"].Config["api_key"]

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"url":"https://redmine.example.fr","api_key":""}}`)

	if got := fake.global["redmine"].Config["api_key"]; got != sealed {
		t.Errorf("key = %q, expected kept as it is", got)
	}
	if got := fake.global["redmine"].Config["url"]; got != "https://redmine.example.fr" {
		t.Errorf("URL = %q", got)
	}
}

// Clearing exists, and requires a distinct step.
func TestClearingASensitiveFieldIsExplicit(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, testKey)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"production-key"}}`)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{},"cleared_secrets":["api_key"]}`)

	if got, present := fake.global["redmine"].Config["api_key"]; present {
		t.Errorf("key = %q, expected cleared", got)
	}
}

// A setting the plugin does not declare is refused, and not saved silently: a typo would
// otherwise surface weeks later.
func TestAnUnknownSettingIsRefused(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), testKey)

	recorder := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"cle_api":"x"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code %d, expected 400 — %s", recorder.Code, recorder.Body.String())
	}
}

// An unknown plugin answers 404 naming the ones that exist.
func TestAnUnknownPluginAnswers404(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), testKey)

	recorder := putPlugin(t, handler.Set, "/admin/plugins/jira", "jira", `{"enabled":true}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("code %d, expected 404", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "redmine") {
		t.Errorf("message %q — it must name the embedded plugins", recorder.Body.String())
	}
}

/*
 * The project scope.
 */

// Only the plugins that are decided per project, and that the instance has not switched
// off, appear to a project.
//
// A switch that does nothing is worse than an absence: it invites pressing it, then
// wondering why nothing changes.
func TestTheProjectSeesOnlyWhatItCanDecide(t *testing.T) {
	fake := newFakePluginStore()
	// The instance switches off the delivery note: the project must no longer see it.
	fake.global["bon-de-livraison"] = store.PluginSettings{PluginID: "bon-de-livraison", Enabled: false}
	handler := pluginsHandlerWith(t, fake, testKey)

	request := httptest.NewRequest(http.MethodGet, "/projects/mse/plugins", nil)
	request.SetPathValue("project", "mse")
	recorder := httptest.NewRecorder()
	handler.ProjectList(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d — %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Plugins []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}

	ids := make([]string, 0, len(payload.Plugins))
	for _, entry := range payload.Plugins {
		ids = append(ids, entry.ID)
		// With no decision from the project, inheritance means yes: that is what keeps a project
		// declared before these tables receiving what the instance offers it.
		if !entry.Enabled {
			t.Errorf("plugin %q switched off although the project decided nothing", entry.ID)
		}
	}

	if slices.Contains(ids, "bon-de-livraison") {
		t.Error("a plugin switched off on the instance is offered to the project")
	}
	if !slices.Contains(ids, "redmine") {
		t.Errorf("plugins offered = %v", ids)
	}
}

// A project cannot switch back on what the instance switched off: the deployment does not
// carry it, and every call would fail.
func TestAProjectCannotSwitchBackOnWhatTheInstanceSwitchedOff(t *testing.T) {
	fake := newFakePluginStore()
	fake.global["bon-de-livraison"] = store.PluginSettings{PluginID: "bon-de-livraison", Enabled: false}
	handler := pluginsHandlerWith(t, fake, testKey)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/bon-de-livraison", "bon-de-livraison", `{"enabled":true}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, expected 409 — %s", recorder.Code, recorder.Body.String())
	}
}

// The MSE module is set per project: switched on for the instance, a project outside its
// scope switches it off for itself.
//
// No plugin in the registry is instance-only any more; the refusal of a per-project
// setting stays in the handler for the day one comes back.
func TestTheMSEModuleIsSetPerProject(t *testing.T) {
	fake := newFakePluginStore()
	fake.global["mse"] = store.PluginSettings{PluginID: "mse", Enabled: true}
	handler := pluginsHandlerWith(t, fake, testKey)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/mse", "mse", `{"enabled":false}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d, expected 200 — %s", recorder.Code, recorder.Body.String())
	}
}

// A field's requirement is about the resolved value: a project leaving the URL empty
// because the instance provides it is accepted.
func TestAProjectInheritsTheInstancesValues(t *testing.T) {
	fake := newFakePluginStore()
	fake.global["redmine"] = store.PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{"url": "https://redmine.example.fr"},
	}
	handler := pluginsHandlerWith(t, fake, testKey)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"project":"mse-dev"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d — %s", recorder.Code, recorder.Body.String())
	}

	if got := fake.byProject[7]["redmine"].Config["project"]; got != "mse-dev" {
		t.Errorf("tracker project = %q", got)
	}
	// The URL is not copied across: the project has nothing particular to say, and freezing
	// the instance's value would mean a global change no longer reaches it.
	if _, copied := fake.byProject[7]["redmine"].Config["url"]; copied {
		t.Error("the instance's URL was copied into the project")
	}
}

// With no project identifier anywhere, the setting is refused: that is the difference
// between "nothing particular to say" and "the value is missing everywhere".
func TestARequiredFieldMissingEverywhereIsRefused(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), testKey)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/redmine", "redmine", `{"enabled":true,"config":{}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code %d, expected 400 — %s", recorder.Code, recorder.Body.String())
	}
}

// Removal returns the project to inheritance, and is not a disabling.
//
// Confusing them would mean a project, once departed from the common setting, could never
// follow it again.
func TestRemovalReturnsTheProjectToInheritance(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, testKey)

	putPlugin(t, handler.SetProject, "/projects/mse/plugins/bon-de-livraison",
		"bon-de-livraison", `{"enabled":false}`)
	if fake.byProject[7]["bon-de-livraison"].Enabled {
		t.Fatal("the switch-off was not recorded")
	}

	request := asAdmin(httptest.NewRequest(http.MethodDelete,
		"/projects/mse/plugins/bon-de-livraison", nil))
	request.SetPathValue("project", "mse")
	request.SetPathValue("plugin", "bon-de-livraison")
	recorder := httptest.NewRecorder()
	handler.ClearProject(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d — %s", recorder.Code, recorder.Body.String())
	}
	if _, declared := fake.byProject[7]["bon-de-livraison"]; declared {
		t.Error("the project's decision survives the removal")
	}
}

// The audit log carries whether it is enabled, never the configuration: it is read with
// fewer precautions than a database, which is precisely its purpose.
func TestTheLogNeverCarriesTheConfiguration(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, testKey)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"production-key"}}`)

	if len(fake.audit) != 1 || fake.audit[0] != "plugin.settings:redmine" {
		t.Errorf("journal = %v", fake.audit)
	}
}
