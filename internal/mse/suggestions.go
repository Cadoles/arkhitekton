package mse

import (
	"context"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
)

// EnvironmentSuggestions returns the environments the launch form offers for a parameter of
// kind `environment`.
//
// The core used to read the catalogue's section itself. It now asks the plugin for the list,
// which returns none when the module is switched off for the project, or with no project: the
// field then stays free-text. Read at each request, like the routes' switch.
func EnvironmentSuggestions(
	declared *catalog.Catalog, modules *dashhttp.Modules, projects dashhttp.ProjectResolver,
) func(context.Context, string) []dashhttp.Suggestion {
	return func(ctx context.Context, projectSlug string) []dashhttp.Suggestion {
		if projectSlug == "" {
			return nil
		}
		project, err := projects.ProjectBySlug(ctx, projectSlug)
		if err != nil || !modules.EnabledIn(ctx, project.ID, plugin.ModuleMSE) {
			return nil
		}
		environments := declared.EnvironmentsOf(projectSlug)
		proposed := make([]dashhttp.Suggestion, 0, len(environments))
		for _, environment := range environments {
			if !environment.IsVisible() {
				continue
			}
			proposed = append(proposed, dashhttp.Suggestion{Value: environment.ID, Label: environment.Label})
		}
		return proposed
	}
}
