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

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/secrets"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les réglages de plugins.
 *
 * Un écran qui reçoit des clés d'API depuis un navigateur : les tests portent
 * donc d'abord sur ce qui ne doit **jamais** sortir, puis sur les deux façons de
 * perdre une valeur — l'effacer en croyant ne rien changer, et la refuser en
 * croyant la valider.
 */

const cleDeTest = "sN0Xk1t2Qv8yZ3aB4cD5eF6gH7iJ8kL9mN0oP1qR2sQ="

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
			"flux":             {PluginID: "flux", Enabled: true, Config: map[string]string{}},
			"portaldata":       {PluginID: "portaldata", Enabled: true, Config: map[string]string{}},
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
			t.Fatalf("chiffreur: %v", err)
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

// projectOf extrait le slug d'un chemin `/projects/<slug>/...`.
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
		t.Fatalf("réponse illisible: %v — %s", err, recorder.Body.String())
	}
	return payload
}

/*
 * Ce qui ne sort jamais.
 */

// Une clé d'API enregistrée n'est jamais réaffichée — ni dans la réponse à
// l'écriture, ni dans la liste.
//
// C'est ce qui rend la saisie d'un secret depuis un navigateur acceptable : la
// relire ne suffit pas à la voler. L'écran n'en connaît que la présence.
func TestUneCleDApiNestJamaisReaffichee(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	recorder := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"url":"https://redmine.example.fr","api_key":"clé-de-production"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d — %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "clé-de-production") {
		t.Fatalf("la clé transparaît dans la réponse: %s", recorder.Body.String())
	}

	listed := httptest.NewRecorder()
	handler.List(listed, httptest.NewRequest(http.MethodGet, "/admin/plugins", nil))
	if strings.Contains(listed.Body.String(), "clé-de-production") {
		t.Fatalf("la clé transparaît dans la liste: %s", listed.Body.String())
	}

	// Mais sa **présence** se lit : sans elle, l'écran ne saurait pas distinguer
	// un champ jamais rempli d'un champ rempli qu'il n'affiche pas.
	payload := decodePlugin(t, recorder)
	names, _ := payload["secrets_set"].([]any)
	if len(names) != 1 || names[0] != "api_key" {
		t.Errorf("champs sensibles renseignés = %v", names)
	}
}

// Ce qui est écrit en base est scellé, et se rouvre avec la clé.
func TestLaValeurEcriteEnBaseEstScellee(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"clé-de-production"}}`)

	stored := fake.global["redmine"].Config["api_key"]
	if stored == "" || strings.Contains(stored, "clé-de-production") {
		t.Fatalf("valeur stockée = %q", stored)
	}

	cipher, _ := secrets.New(cleDeTest)
	opened, err := cipher.Open(stored)
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	if opened != "clé-de-production" {
		t.Errorf("valeur ouverte = %q", opened)
	}
}

// Sans clé de scellement, l'écriture d'un champ sensible est refusée — et le
// message nomme la variable manquante.
//
// Refusée plutôt qu'écrite en clair : c'est le seul endroit où la distinction se
// joue, et un enregistrement qui « réussit » en stockant le clair serait la pire
// des issues.
func TestSansCleDeScellementLeChampSensibleEstRefuse(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, "")

	recorder := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"clé-de-production"}}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, attendu 409 — %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "SECRETS_KEY") {
		t.Errorf("message %q — il doit nommer la variable", recorder.Body.String())
	}
	if _, written := fake.global["redmine"].Config["api_key"]; written {
		t.Error("la clé a été écrite malgré le refus")
	}

	// Le reste reste réglable : un déploiement sans scellement doit pouvoir
	// activer un plugin et saisir ses champs ordinaires.
	ok := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"url":"https://redmine.example.fr"}}`)
	if ok.Code != http.StatusOK {
		t.Errorf("code %d — %s", ok.Code, ok.Body.String())
	}
}

/*
 * Les deux façons de perdre une valeur.
 */

// Un champ sensible laissé vide est **conservé**, pas effacé.
//
// L'écran ne reçoit jamais la clé, donc ne peut pas la renvoyer : la traiter
// comme un effacement ferait perdre la clé au premier enregistrement du moindre
// autre champ — modifier une URL suffirait à couper la synchronisation.
func TestUnChampSensibleLaisseVideEstConserve(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"clé-de-production"}}`)
	sealed := fake.global["redmine"].Config["api_key"]

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"url":"https://redmine.example.fr","api_key":""}}`)

	if got := fake.global["redmine"].Config["api_key"]; got != sealed {
		t.Errorf("clé = %q, attendue conservée telle quelle", got)
	}
	if got := fake.global["redmine"].Config["url"]; got != "https://redmine.example.fr" {
		t.Errorf("URL = %q", got)
	}
}

// L'effacement existe, et demande un geste distinct.
func TestLEffacementDUnChampSensibleEstExplicite(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"clé-de-production"}}`)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{},"cleared_secrets":["api_key"]}`)

	if got, present := fake.global["redmine"].Config["api_key"]; present {
		t.Errorf("clé = %q, attendue effacée", got)
	}
}

// Un réglage que le plugin ne déclare pas est refusé, et non enregistré en
// silence : une faute de frappe se découvrirait sinon des semaines plus tard.
func TestUnReglageInconnuEstRefuse(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), cleDeTest)

	recorder := putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"cle_api":"x"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code %d, attendu 400 — %s", recorder.Code, recorder.Body.String())
	}
}

// Un plugin inconnu répond 404 en nommant ceux qui existent.
func TestUnPluginInconnuRepond404(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), cleDeTest)

	recorder := putPlugin(t, handler.Set, "/admin/plugins/jira", "jira", `{"enabled":true}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("code %d, attendu 404", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "redmine") {
		t.Errorf("message %q — il doit nommer les plugins embarqués", recorder.Body.String())
	}
}

/*
 * La portée projet.
 */

// N'apparaissent au projet que les plugins qui se décident par projet, et que
// l'instance n'a pas éteints.
//
// Un interrupteur qui ne fait rien est pire qu'une absence : il invite à
// l'actionner, puis à chercher pourquoi rien ne change.
func TestLeProjetNeVoitQueCeQuIlPeutDecider(t *testing.T) {
	fake := newFakePluginStore()
	// L'instance éteint l'attestation : le projet ne doit plus la voir.
	fake.global["portaldata"] = store.PluginSettings{PluginID: "portaldata", Enabled: false}
	handler := pluginsHandlerWith(t, fake, cleDeTest)

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
		t.Fatalf("réponse illisible: %v", err)
	}

	ids := make([]string, 0, len(payload.Plugins))
	for _, entry := range payload.Plugins {
		ids = append(ids, entry.ID)
		// Sans décision du projet, l'héritage vaut « oui » : c'est ce qui fait
		// qu'un projet déclaré avant ces tables continue de recevoir son bon de
		// livraison et son attestation.
		if !entry.Enabled {
			t.Errorf("plugin %q éteint alors que le projet n'a rien décidé", entry.ID)
		}
	}

	if slices.Contains(ids, "portaldata") {
		t.Error("un plugin éteint sur l'instance est proposé au projet")
	}
	// `flux` vaut nécessairement pour toute l'instance : son namespace n'est pas
	// une propriété d'un projet, et prétendre le contraire afficherait un
	// interrupteur qui ne fait rien.
	if slices.Contains(ids, "flux") {
		t.Error("un plugin qui vaut pour l'instance est proposé au projet")
	}
	if !slices.Contains(ids, "redmine") || !slices.Contains(ids, "bon-de-livraison") {
		t.Errorf("plugins proposés = %v", ids)
	}
}

// Un projet ne peut pas rallumer ce que l'instance a éteint : le déploiement ne
// le porte pas, et chaque appel échouerait.
func TestUnProjetNePeutPasRallumerCeQueLInstanceAEteint(t *testing.T) {
	fake := newFakePluginStore()
	fake.global["bon-de-livraison"] = store.PluginSettings{PluginID: "bon-de-livraison", Enabled: false}
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/bon-de-livraison", "bon-de-livraison", `{"enabled":true}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, attendu 409 — %s", recorder.Code, recorder.Body.String())
	}
}

// Un plugin qui vaut pour l'instance ne se règle pas par projet.
//
// `flux` en est un : le namespace où vivent ses objets n'est pas une propriété
// d'un projet. `gitea`, lui, a cessé d'en être un le jour où il s'est mis à
// observer — surveiller les demandes de fusion d'un dépôt est bien une décision
// de projet, même quand la connexion vaut pour toute l'instance.
func TestUnPluginDInstanceNeSeReglePasParProjet(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), cleDeTest)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/flux", "flux", `{"enabled":false}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code %d, attendu 409 — %s", recorder.Code, recorder.Body.String())
	}
}

// L'obligation d'un champ porte sur la valeur **résolue** : un projet qui laisse
// l'URL vide parce que l'instance la fournit est accepté.
func TestUnProjetHeriteDesValeursDeLInstance(t *testing.T) {
	fake := newFakePluginStore()
	fake.global["redmine"] = store.PluginSettings{
		PluginID: "redmine", Enabled: true,
		Config: map[string]string{"url": "https://redmine.example.fr"},
	}
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"project":"mse-dev"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d — %s", recorder.Code, recorder.Body.String())
	}

	if got := fake.byProject[7]["redmine"].Config["project"]; got != "mse-dev" {
		t.Errorf("projet de suivi = %q", got)
	}
	// L'URL n'est pas recopiée : le projet n'a rien de particulier à dire, et
	// figer la valeur de l'instance ferait qu'un changement global ne
	// l'atteindrait plus.
	if _, copied := fake.byProject[7]["redmine"].Config["url"]; copied {
		t.Error("l'URL de l'instance a été recopiée dans le projet")
	}
}

// Sans identifiant de projet nulle part, le réglage est refusé : c'est la
// différence entre « rien de particulier à dire » et « la valeur manque partout ».
func TestUnChampObligatoireAbsentPartoutEstRefuse(t *testing.T) {
	handler := pluginsHandlerWith(t, newFakePluginStore(), cleDeTest)

	recorder := putPlugin(t, handler.SetProject,
		"/projects/mse/plugins/redmine", "redmine", `{"enabled":true,"config":{}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code %d, attendu 400 — %s", recorder.Code, recorder.Body.String())
	}
}

// Le retrait rend le projet à l'héritage, et n'est pas une désactivation.
//
// Les confondre ferait qu'un projet, une fois écarté du réglage commun, ne
// pourrait plus jamais le suivre à nouveau.
func TestLeRetraitRendLeProjetALHeritage(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	putPlugin(t, handler.SetProject, "/projects/mse/plugins/bon-de-livraison",
		"bon-de-livraison", `{"enabled":false}`)
	if fake.byProject[7]["bon-de-livraison"].Enabled {
		t.Fatal("la désactivation n'a pas été enregistrée")
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
		t.Error("la décision du projet subsiste après le retrait")
	}
}

// Le journal d'audit porte l'activation, jamais la configuration : il se lit avec
// moins de précautions qu'une base, c'est précisément son but.
func TestLeJournalNePorteJamaisLaConfiguration(t *testing.T) {
	fake := newFakePluginStore()
	handler := pluginsHandlerWith(t, fake, cleDeTest)

	putPlugin(t, handler.Set, "/admin/plugins/redmine", "redmine",
		`{"enabled":true,"config":{"api_key":"clé-de-production"}}`)

	if len(fake.audit) != 1 || fake.audit[0] != "plugin.settings:redmine" {
		t.Errorf("journal = %v", fake.audit)
	}
}
