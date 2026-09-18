package mse

import (
	"context"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
)

// EnvironmentSuggestions rend les environnements que le formulaire de lancement
// propose pour un paramètre de nature `environment`.
//
// Le noyau lisait la section du catalogue lui-même. Il demande désormais la liste
// au plugin, qui n'en rend aucune quand le module est éteint pour le projet, ou
// sans projet : le champ reste alors en saisie libre (doc 16 §16.7, couture 9 ;
// §16.18). Lu à chaque requête, comme l'interrupteur des routes.
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
