package http

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// testRegistry declares plugins shaped like the built-in ones these tests talk about: a tracker
// with a required project identifier and a sealed key, a forge, a module and an exporter decided per
// project. The core does not import the plugins, so it describes them again here.
var testRegistry = mustRegistry(
	capability.Plugin{
		ID: "redmine", Label: "Redmine", PerProject: true,
		Capabilities: []capability.Header{{Kind: capability.KindTracker, Name: "tracker", Label: "Suivi"}},
		Events:       []capability.EventSpec{{Suffix: "issue.closed", Label: "Demande close"}},
		Global: []capability.Field{
			{Name: "url", Label: "URL", Type: capability.FieldURL},
			{Name: "api_key", Label: "Clé", Type: capability.FieldText, Secret: true},
		},
		Project: []capability.Field{
			{Name: "project", Label: "Identifiant du projet", Type: capability.FieldText, Required: true},
			{Name: "review_statuses", Label: "Statuts de revue", Type: capability.FieldList},
			{Name: "url", Label: "URL", Type: capability.FieldURL},
			{Name: "api_key", Label: "Clé", Type: capability.FieldText, Secret: true},
		},
	},
	capability.Plugin{
		ID: "gitea", Label: "Gitea", PerProject: true,
		Capabilities: []capability.Header{{Kind: capability.KindForge, Name: "forge", Label: "Forge"}},
		Events:       []capability.EventSpec{{Suffix: "pull_request.opened", Label: "Demande de fusion ouverte"}},
		Actions: []capability.ActionSpec{{
			Suffix: "status", Label: "Statut de commit", Summary: "Pose un statut.",
			Fields: []capability.ActionField{{Name: "commit", Label: "Commit", Required: true, Template: true}},
		}},
		Global: []capability.Field{
			{Name: "url", Label: "URL", Type: capability.FieldURL},
			{Name: "token", Label: "Jeton", Type: capability.FieldText, Secret: true},
		},
	},
	capability.Plugin{
		ID: "mse", Label: "MSE", PerProject: true,
		Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "module", Label: "Module"}},
		Events:       []capability.EventSpec{{Suffix: "release.promoted", Label: "Promotion aboutie"}},
		Global:       []capability.Field{{Name: "namespace", Label: "Namespace Flux", Type: capability.FieldText}},
		Project:      []capability.Field{{Name: "namespace", Label: "Namespace Flux", Type: capability.FieldText}},
	},
	capability.Plugin{
		ID: "bon-de-livraison", Label: "Bon de livraison", PerProject: true,
		Capabilities: []capability.Header{{Kind: capability.KindExporter, Name: "exporter", Label: "Livrables"}},
	},
	capability.Plugin{ID: "core", Label: "Socle", Mandatory: true},
)

func mustRegistry(plugins ...capability.Plugin) *capabilities.Registry {
	for i := range plugins {
		plugins[i].Version, plugins[i].APIVersion = "1.0.0", sdk.APIVersion
	}
	registry, err := capabilities.New(plugins...)
	if err != nil {
		panic(err)
	}
	return registry
}
