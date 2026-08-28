-- Sort le nom de Flux du relevé des environnements.
--
-- `observations.flux_state` nommait un outil dans une colonne, comme
-- `versions.redmine_id` nommait l'autre (migrations 0030 et 0031). Même faute,
-- même correction : la colonne porte ce que la donnée **est**, non de quel
-- produit elle vient.
--
-- Ce qu'elle est, le contrat le disait déjà. `provider.ResolutionDeclared`
-- distingue l'intention — ce qu'un outil de déploiement a retenu — du constat
-- applicatif porté par `portal_version`. La colonne prend le mot du contrat.
--
-- `portal_version` et `migration_state` gardent leur nom : ils désignent le
-- constat d'un attester, et « portal » y nomme la source de l'attestation, pas
-- l'outil de déploiement. Les renommer relève du lot D d'une autre frontière ;
-- les mêler ici mélangerait deux corrections dans une migration.

-- +goose Up
alter table environment_observations rename column flux_state to declared_state;

-- +goose Down
alter table environment_observations rename column declared_state to flux_state;
