package catalog

import "forge.cadoles.com/vcarroy/arkhitekton/internal/auth"

// EnvironmentPolicies returns the promotion requirements the catalogue declares, per project.
//
// The authorisation keeps the mechanism, the minimum role according to sensitivity; the catalogue
// supplies the table. Each project declares the sensitivity of its environments in its section.
func (c *Catalog) EnvironmentPolicies() auth.ProjectPolicies {
	policies := make(auth.ProjectPolicies, len(c.Projects))
	for slug, project := range c.Projects {
		environments := make(map[string]auth.EnvironmentPolicy, len(project.Environments))
		for _, environment := range project.Environments {
			environments[environment.ID] = auth.EnvironmentPolicy{Sensitive: environment.IsSensitive()}
		}
		policies[slug] = environments
	}
	return policies
}
