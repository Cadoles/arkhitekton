package mse

import (
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
)

// Policies rend les exigences de promotion déclarées au catalogue.
//
// Le noyau garde le mécanisme — `auth.PermDeploy`, le rôle minimal selon la
// sensibilité —, le plugin fournit la table. Elle était écrite dans `main.go`, à
// côté d'un TODO qui promettait de la lire ailleurs (doc 16 §16.7, couture 4).
func Policies(environments []catalog.Environment) map[string]auth.EnvironmentPolicy {
	policies := make(map[string]auth.EnvironmentPolicy, len(environments))
	for _, environment := range environments {
		policies[environment.ID] = auth.EnvironmentPolicy{Sensitive: environment.IsSensitive()}
	}
	return policies
}
