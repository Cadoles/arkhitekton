package mse

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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

// The visible environments are offered to the project that has the module, and to no other:
// with no project, an unknown project or the module switched off, the field stays free.
func TestTheOfferedEnvironmentsFollowTheProjectsModule(t *testing.T) {
	hidden := false
	environments := &catalog.Catalog{Projects: map[string]catalog.ProjectDeclaration{
		"mse": {Environments: []catalog.Environment{
			{ID: "qualif", Label: "Qualification"},
			{ID: "fantome", Label: "Ghost", Visible: &hidden},
		}},
		// Declared, but for a project that switched the module off.
		"tekton": {Environments: []catalog.Environment{{ID: "qualif", Label: "Qualification"}}},
	}}
	resolver := projets{"mse": 1, "tekton": 2}
	ctx := context.Background()

	allume := EnvironmentSuggestions(environments,
		dashhttp.NewModules(pluginSettings{global: true, eteints: map[int64]bool{2: true}}, quietLogger()), resolver)
	if proposed := allume(ctx, "mse"); len(proposed) != 1 || proposed[0].Value != "qualif" {
		t.Errorf("project with the module: %+v", proposed)
	}
	for _, slug := range []string{"tekton", "", "unknown"} {
		if proposed := allume(ctx, slug); proposed != nil {
			t.Errorf("project %q: %+v, expected no list", slug, proposed)
		}
	}

	eteint := EnvironmentSuggestions(environments,
		dashhttp.NewModules(pluginSettings{global: false}, quietLogger()), resolver)
	if proposed := eteint(ctx, "mse"); proposed != nil {
		t.Errorf("instance switched off: %+v, expected no list", proposed)
	}
}
