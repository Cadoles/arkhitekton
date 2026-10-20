package mse

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

type pluginSettings struct {
	global          bool
	switchedOffList map[int64]bool
}

func (p pluginSettings) PluginSettingsAll(context.Context) (map[string]store.PluginSettings, error) {
	return map[string]store.PluginSettings{plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: p.global}}, nil
}

func (p pluginSettings) ProjectPluginSettings(_ context.Context, projectID int64) (map[string]store.PluginSettings, error) {
	if p.switchedOffList[projectID] {
		return map[string]store.PluginSettings{plugin.ModuleMSE: {PluginID: plugin.ModuleMSE, Enabled: false}}, nil
	}
	return nil, nil
}

type projects map[string]int64

func (p projects) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
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
	resolver := projects{"mse": 1, "tekton": 2}
	ctx := context.Background()

	switchedOn := EnvironmentSuggestions(environments,
		dashhttp.NewModules(moduleRegistry, pluginSettings{global: true, switchedOffList: map[int64]bool{2: true}}, quietLogger()), resolver)
	if proposed := switchedOn(ctx, "mse"); len(proposed) != 1 || proposed[0].Value != "qualif" {
		t.Errorf("project with the module: %+v", proposed)
	}
	for _, slug := range []string{"tekton", "", "unknown"} {
		if proposed := switchedOn(ctx, slug); proposed != nil {
			t.Errorf("project %q: %+v, expected no list", slug, proposed)
		}
	}

	switchedOff := EnvironmentSuggestions(environments,
		dashhttp.NewModules(moduleRegistry, pluginSettings{global: false}, quietLogger()), resolver)
	if proposed := switchedOff(ctx, "mse"); proposed != nil {
		t.Errorf("instance switched off: %+v, expected no list", proposed)
	}
}

// moduleRegistry declares the MSE module, as its plugin does.
var moduleRegistry = func() *capabilities.Registry {
	registry, err := capabilities.New(capability.Plugin{
		ID: plugin.ModuleMSE, Version: "1.0.0", APIVersion: sdk.APIVersion, PerProject: true,
		Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "module"}},
	})
	if err != nil {
		panic(err)
	}
	return registry
}()
