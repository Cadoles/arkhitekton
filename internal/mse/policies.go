package mse

import (
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
)

// Policies rend les exigences de promotion déclarées au catalogue, par projet.
//
// Le noyau garde le mécanisme — le rôle minimal selon la sensibilité —, le plugin
// fournit la table. Chaque projet déclare la sensibilité de ses environnements
// dans sa section du catalogue (doc 16 §16.7, couture 4 ; §16.18).
func Policies(declared *catalog.Catalog) auth.ProjectPolicies {
	policies := make(auth.ProjectPolicies, len(declared.Projects))
	for slug, project := range declared.Projects {
		environments := make(map[string]auth.EnvironmentPolicy, len(project.Environments))
		for _, environment := range project.Environments {
			environments[environment.ID] = auth.EnvironmentPolicy{Sensitive: environment.IsSensitive()}
		}
		policies[slug] = environments
	}
	return policies
}
