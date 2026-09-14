package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les modules, et la porte de doc 16 §16.8 : une instance sans le plugin MSE
 * reste cohérente — ses routes répondent 404, `/me` ne l'annonce pas, et une
 * release ne propose aucune promotion.
 */

type lecteurDePlugins struct {
	settings map[string]store.PluginSettings
	err      error
}

func (l lecteurDePlugins) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return l.settings, l.err
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

// Sans le module MSE, une release ne propose aucune cible de promotion — même si
// la table des environnements garde les lignes d'une activation passée.
func TestSansModuleMSEUneReleaseNaPasDeCible(t *testing.T) {
	for _, cas := range []struct {
		mse     bool
		cibles  bool
		libelle string
	}{
		{mse: false, cibles: false, libelle: "module éteint"},
		{mse: true, cibles: true, libelle: "module allumé"},
	} {
		fake := newFakeStore()
		fake.releases["1.8.0-rc1"] = store.Release{
			ID: 9, Tag: "1.8.0-rc1", Kind: "pre", State: "ready", ImageVersion: "1.8.0-rc1",
		}
		handler := newReleasesHandler(t, fake).WithModules(modulesAvec(cas.mse))

		request := httptest.NewRequest(http.MethodGet, "/releases/1.8.0-rc1", nil)
		request.SetPathValue("tag", "1.8.0-rc1")
		recorder := httptest.NewRecorder()
		handler.Get(recorder, asRole(request, auth.RoleReleaser))

		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: code %d — %s", cas.libelle, recorder.Code, recorder.Body.String())
		}
		var payload releasePayload
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: décodage: %v", cas.libelle, err)
		}
		if got := len(payload.Targets) > 0; got != cas.cibles {
			t.Errorf("%s: %d cible(s)", cas.libelle, len(payload.Targets))
		}
	}
}
