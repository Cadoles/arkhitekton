-- Rend opaques les réglages du déployeur.
--
-- `environments` portait trois colonnes qui appartenaient toutes à Flux :
-- `promotion_suffix` — une métadonnée SemVer que la plage d'une ImagePolicy
-- retient —, `accepts_prerelease` — un accord avec cette plage — et
-- `flux_kustomization`, qui disait son origine dans son nom.
--
-- Aucune des trois n'a de sens pour un déployeur qui ne sélectionne pas par
-- plage SemVer, et pourtant tout environnement devait les porter. Le schéma
-- imposait un outil, comme `versions.redmine_id` en imposait un autre avant la
-- migration 0030 (doc 12 §12.9, lot D).
--
-- Elles deviennent une carte que le dashboard **transporte sans la lire** :
-- catalogue, base, worker et API la font circuler telle quelle, et seul le
-- déployeur nomme ses clés. Ajouter un second déployeur n'ajoute plus de
-- colonne.
--
-- Les clés reprennent le vocabulaire déclaré par `internal/flux` :
-- `promotionSuffix`, `acceptsPrerelease`, `kustomization`. La dernière perd son
-- préfixe : elle vit désormais dans un espace qui appartient déjà au déployeur,
-- et l'y répéter serait redondant.

-- +goose Up
alter table environments
  add column if not exists deployer_settings jsonb not null default '{}'::jsonb;

-- Les booléens deviennent des chaînes : c'est ce qu'une configuration
-- transporte, et le déployeur les interprète. La colonne ne peut pas porter des
-- types différents selon la clé.
-- Conditionné au suffixe : sans lui, l'environnement n'était atteint par aucun
-- déployeur, et lui inventer un `acceptsPrerelease` déposerait un réglage que
-- personne n'a déclaré.
update environments
set deployer_settings = jsonb_strip_nulls(jsonb_build_object(
    'promotionSuffix',   nullif(promotion_suffix, ''),
    'acceptsPrerelease', case
        when promotion_suffix = '' then null
        when accepts_prerelease then 'true'
        else 'false'
    end,
    'kustomization',     nullif(flux_kustomization, '')
));

-- Une carte doit rester une carte de chaînes : une valeur imbriquée ou
-- numérique traverserait le décodage Go sans erreur ici, pour échouer chez le
-- déployeur, loin de sa cause.
alter table environments add constraint environments_deployer_settings_plates
  check (not jsonb_path_exists(deployer_settings, '$.* ? (@.type() <> "string")'));

alter table environments drop column if exists promotion_suffix;
alter table environments drop column if exists accepts_prerelease;
alter table environments drop column if exists flux_kustomization;

-- +goose Down
alter table environments add column if not exists promotion_suffix text not null default '';
alter table environments add column if not exists accepts_prerelease boolean not null default false;
alter table environments add column if not exists flux_kustomization text;

update environments
set promotion_suffix   = coalesce(deployer_settings ->> 'promotionSuffix', ''),
    accepts_prerelease = coalesce(deployer_settings ->> 'acceptsPrerelease', 'false') = 'true',
    flux_kustomization = deployer_settings ->> 'kustomization';

alter table environments drop constraint if exists environments_deployer_settings_plates;
alter table environments drop column if exists deployer_settings;
