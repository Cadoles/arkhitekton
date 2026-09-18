package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les modules, et la porte de doc 16 §16.8 : une instance sans le plugin MSE
 * reste cohérente — ses routes répondent 404 et `/me` ne l'annonce pas. Les
 * cibles de promotion d'une release sont l'une de ces routes (doc 16 §16.14).
 */

type lecteurDePlugins struct {
	settings map[string]store.PluginSettings
	// projets porte les décisions explicites, par projet.
	projets map[int64]map[string]store.PluginSettings
	err     error
}

func (l lecteurDePlugins) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return l.settings, l.err
}

func (l lecteurDePlugins) ProjectPluginSettings(_ context.Context, projectID int64) (map[string]store.PluginSettings, error) {
	return l.projets[projectID], l.err
}

func modulesAvec(mse bool) *Modules {
	return NewModules(lecteurDePlugins{settings: map[string]store.PluginSettings{
		plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: mse},
		"redmine":        {PluginID: "redmine", Enabled: true},
	}}, quietLogger())
}

// Seuls les plugins de genre module sont annoncés : un suivi allumé n'apporte
// pas d'écran.
func TestSeulsLesModulesAllumesSontAnnonces(t *testing.T) {
	if got := modulesAvec(true).Active(context.Background()); !slices.Equal(got, []string{"mse"}) {
		t.Errorf("modules = %v, attendu [mse]", got)
	}
	if got := modulesAvec(false).Active(context.Background()); len(got) != 0 {
		t.Errorf("modules = %v, attendu aucun", got)
	}
}

// Une lecture impossible masque les modules plutôt que d'afficher des écrans dont
// chaque requête échouerait.
func TestUneLectureImpossibleMasqueLesModules(t *testing.T) {
	modules := NewModules(lecteurDePlugins{err: errors.New("base injoignable")}, quietLogger())
	if modules.Enabled(context.Background(), plugin.ModuleMSE) {
		t.Error("un module dont l'état est illisible est annoncé allumé")
	}
}

// Une route d'un module éteint répond 404, et le handler n'est pas atteint.
func TestUneRouteDUnModuleEteintRepond404(t *testing.T) {
	middleware := NewMiddleware(nil, nil, quietLogger()).WithModules(modulesAvec(false))
	atteint := false
	handler := middleware.RequireModule(plugin.ModuleMSE)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { atteint = true }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/environments", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("code %d, attendu 404", recorder.Code)
	}
	if atteint {
		t.Error("le handler d'un module éteint a été atteint")
	}
}

func TestUneRouteDUnModuleAllumePasse(t *testing.T) {
	middleware := NewMiddleware(nil, nil, quietLogger()).WithModules(modulesAvec(true))
	atteint := false
	handler := middleware.RequireModule(plugin.ModuleMSE)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { atteint = true }))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/environments", nil))
	if !atteint {
		t.Error("le handler d'un module allumé n'a pas été atteint")
	}
}

// `/me` annonce les modules allumés : c'est ce que la navigation lit.
func TestMeAnnonceLesModules(t *testing.T) {
	handler := (&AuthHandler{logger: quietLogger()}).WithModules(modulesAvec(true))

	request := httptest.NewRequest(http.MethodGet, "/me", nil)
	request = request.WithContext(context.WithValue(request.Context(), identityKey,
		auth.Identity{Subject: "s", Username: "vcarroy", Role: auth.RoleAdmin}))
	recorder := httptest.NewRecorder()
	handler.Me(recorder, request)

	var payload struct {
		Modules []string `json:"modules"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if !slices.Equal(payload.Modules, []string{"mse"}) {
		t.Errorf("modules = %v, attendu [mse]", payload.Modules)
	}
}

// Un projet hérite du module allumé sur l'instance, peut l'éteindre pour lui, et
// ne peut pas le rallumer quand l'instance l'a éteint (doc 16 §16.18).
func TestUnModuleSeResoutDansLeProjet(t *testing.T) {
	lecteur := func(global bool) *Modules {
		return NewModules(lecteurDePlugins{
			settings: map[string]store.PluginSettings{plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: global}},
			projets: map[int64]map[string]store.PluginSettings{
				2: {plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: false}},
				3: {plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: true}},
			},
		}, quietLogger())
	}
	ctx := context.Background()

	for _, cas := range []struct {
		global  bool
		projet  int64
		attendu bool
		libelle string
	}{
		{true, 1, true, "sans décision, le projet hérite"},
		{true, 2, false, "éteint pour le projet"},
		{false, 3, false, "l'instance éteinte l'emporte sur le projet"},
	} {
		if got := lecteur(cas.global).EnabledIn(ctx, cas.projet, plugin.ModuleMSE); got != cas.attendu {
			t.Errorf("%s: %v, attendu %v", cas.libelle, got, cas.attendu)
		}
	}
}

type resolveurDeProjets map[string]int64

func (r resolveurDeProjets) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	id, found := r[slug]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: id, Slug: slug}, nil
}

// Une route qui nomme un projet répond 404 quand le module est éteint pour lui,
// même allumé sur l'instance.
func TestUneRouteDeProjetSuitLeModuleDuProjet(t *testing.T) {
	modules := NewModules(lecteurDePlugins{
		settings: map[string]store.PluginSettings{plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: true}},
		projets: map[int64]map[string]store.PluginSettings{
			2: {plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: false}},
		},
	}, quietLogger())
	middleware := NewMiddleware(nil, nil, quietLogger()).
		WithProjects(resolveurDeProjets{"mse": 1, "tekton": 2}).WithModules(modules)

	for slug, attendu := range map[string]int{"mse": http.StatusOK, "tekton": http.StatusNotFound} {
		handler := middleware.RequireModule(plugin.ModuleMSE)(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		request := httptest.NewRequest(http.MethodGet, "/projects/"+slug+"/releases/1.0.0/targets", nil)
		request.SetPathValue("project", slug)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != attendu {
			t.Errorf("projet %s: code %d, attendu %d", slug, recorder.Code, attendu)
		}
	}
}
