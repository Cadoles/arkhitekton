package mse

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type pluginSettings struct {
	global  bool
	eteints map[int64]bool
}

func (p pluginSettings) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return map[string]store.PluginSettings{plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: p.global}}, nil
}

func (p pluginSettings) ProjectPluginSettings(_ context.Context, projectID int64) (map[string]store.PluginSettings, error) {
	if p.eteints[projectID] {
		return map[string]store.PluginSettings{plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: false}}, nil
	}
	return nil, nil
}

type projets map[string]int64

func (p projets) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	id, found := p[slug]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: id, Slug: slug}, nil
}

// Les environnements visibles sont proposés au projet qui a le module, et à aucun
// autre : sans projet, projet inconnu ou module éteint, le champ reste libre.
func TestLesEnvironnementsProposesSuiventLeModuleDuProjet(t *testing.T) {
	hidden := false
	environments := &catalog.Catalog{Projects: map[string]catalog.ProjectDeclaration{
		"mse": {Environments: []catalog.Environment{
			{ID: "qualif", Label: "Qualification"},
			{ID: "fantome", Label: "Fantôme", Visible: &hidden},
		}},
		// Déclarés, mais pour un projet qui a éteint le module.
		"tekton": {Environments: []catalog.Environment{{ID: "qualif", Label: "Qualification"}}},
	}}
	resolver := projets{"mse": 1, "tekton": 2}
	ctx := context.Background()

	allume := EnvironmentSuggestions(environments,
		dashhttp.NewModules(pluginSettings{global: true, eteints: map[int64]bool{2: true}}, quietLogger()), resolver)
	if proposed := allume(ctx, "mse"); len(proposed) != 1 || proposed[0].Value != "qualif" {
		t.Errorf("projet avec le module: %+v", proposed)
	}
	for _, slug := range []string{"tekton", "", "inconnu"} {
		if proposed := allume(ctx, slug); proposed != nil {
			t.Errorf("projet %q: %+v, attendu aucune liste", slug, proposed)
		}
	}

	eteint := EnvironmentSuggestions(environments,
		dashhttp.NewModules(pluginSettings{global: false}, quietLogger()), resolver)
	if proposed := eteint(ctx, "mse"); proposed != nil {
		t.Errorf("instance éteinte: %+v, attendu aucune liste", proposed)
	}
}
