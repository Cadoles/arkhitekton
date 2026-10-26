package worker

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// forgeRegistry declares the forge and the tracker as their plugins do: the fields that are sealed,
// and the forge's actions with their closed sets, carried out by the executor given. The core does
// not import the plugins, so it describes them again here.
func forgeRegistry(executor capability.ActionExecutor) *capabilities.Registry {
	registry, err := capabilities.New(
		capability.Plugin{
			ID: "gitea", Version: "1.0.0", APIVersion: sdk.APIVersion, PerProject: true,
			Executor: executor,
			Events:   []capability.EventSpec{{Suffix: "pull_request.opened"}},
			Actions: []capability.ActionSpec{
				{Suffix: "status", Fields: []capability.ActionField{
					{Name: "commit", Required: true, Template: true},
					{Name: "state", Required: true, OneOf: []string{"pending", "success", "failure", "error"}},
					{Name: "context", Required: true},
					{Name: "description", Template: true},
					{Name: "target_url", Template: true},
				}},
				{Suffix: "comment", Fields: []capability.ActionField{
					{Name: "number", Required: true, Template: true},
					{Name: "body", Required: true, Template: true},
				}},
			},
			Global: []capability.Field{
				{Name: "url", Type: capability.FieldURL},
				{Name: "token", Type: capability.FieldText, Secret: true},
			},
		},
		capability.Plugin{
			ID: "redmine", Version: "1.0.0", APIVersion: sdk.APIVersion, PerProject: true,
			Events: []capability.EventSpec{{Suffix: "issue.closed"}},
			Global: []capability.Field{
				{Name: "url", Type: capability.FieldURL},
				{Name: "api_key", Type: capability.FieldText, Secret: true},
			},
			Project: []capability.Field{
				{Name: "project", Type: capability.FieldText, Required: true},
				{Name: "review_statuses", Type: capability.FieldList},
				{Name: "url", Type: capability.FieldURL},
				{Name: "api_key", Type: capability.FieldText, Secret: true},
			},
		},
	)
	if err != nil {
		panic(err)
	}
	return registry
}

// providerRegistry is [forgeRegistry] with an executor nothing calls.
var providerRegistry = forgeRegistry(&recordingExecutor{})

// testActions joins the core's actions with the test plugins'.
var testActions = action.NewCatalog(providerRegistry.Plugins())
