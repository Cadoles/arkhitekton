package mse

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/host/hosttest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

type pluginSettings struct {
	global          bool
	switchedOffList map[int64]bool
}

func (p pluginSettings) PluginSettingsAll(context.Context) (map[string]host.PluginSettings, error) {
	return map[string]host.PluginSettings{PluginID: {PluginID: PluginID, Enabled: p.global}}, nil
}

func (p pluginSettings) ProjectPluginSettings(_ context.Context, projectID int64) (map[string]host.PluginSettings, error) {
	if p.switchedOffList[projectID] {
		return map[string]host.PluginSettings{PluginID: {PluginID: PluginID, Enabled: false}}, nil
	}
	return nil, nil
}

type projects map[string]int64

func (p projects) ProjectBySlug(_ context.Context, slug string) (host.Project, error) {
	id, found := p[slug]
	if !found {
		return host.Project{}, host.ErrProjectNotFound
	}
	return host.Project{ID: id, Slug: slug}, nil
}

// The visible environments are offered to the project that has the module, and to no other:
// with no project, an unknown project or the module switched off, the field stays free.
func TestTheOfferedEnvironmentsFollowTheProjectsModule(t *testing.T) {
	hidden := false
	environments := &host.Catalog{Projects: map[string]host.ProjectDeclaration{
		"mse": {Environments: []host.Environment{
			{ID: "qualif", Label: "Qualification"},
			{ID: "fantome", Label: "Ghost", Visible: &hidden},
		}},
		// Declared, but for a project that switched the module off.
		"tekton": {Environments: []host.Environment{{ID: "qualif", Label: "Qualification"}}},
	}}
	resolver := projects{"mse": 1, "tekton": 2}
	ctx := context.Background()

	switchedOn := EnvironmentSuggestions(environments,
		hosttest.Modules(t, pluginSettings{global: true, switchedOffList: map[int64]bool{2: true}}, modulePlugin), resolver)
	if proposed := switchedOn(ctx, "mse"); len(proposed) != 1 || proposed[0].Value != "qualif" {
		t.Errorf("project with the module: %+v", proposed)
	}
	for _, slug := range []string{"tekton", "", "unknown"} {
		if proposed := switchedOn(ctx, slug); proposed != nil {
			t.Errorf("project %q: %+v, expected no list", slug, proposed)
		}
	}

	switchedOff := EnvironmentSuggestions(environments,
		hosttest.Modules(t, pluginSettings{global: false}, modulePlugin), resolver)
	if proposed := switchedOff(ctx, "mse"); proposed != nil {
		t.Errorf("instance switched off: %+v, expected no list", proposed)
	}
}

// modulePlugin declares the MSE module, as its plugin does.
var modulePlugin = capability.Plugin{
	ID: PluginID, Version: "1.0.0", APIVersion: sdk.APIVersion, PerProject: true,
	Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "module"}},
}
