package mse

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// Policies returns the promotion requirements declared in the catalogue, per project.
//
// The core keeps the mechanism — the minimum role according to sensitivity — the plugin
// supplies the table. Each project declares the sensitivity of its environments in its section
// of the catalogue.
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
