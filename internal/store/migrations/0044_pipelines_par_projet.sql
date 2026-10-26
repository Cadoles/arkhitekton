-- Déclare quels pipelines du registre s'appliquent à un projet.
--
-- Le catalogue dit ce que **l'instance** sait lancer ; il ne dit pas ce que
-- chaque projet veut lancer. Un projet sans campagne de charge voyait pourtant
-- `load-tests` s'enchaîner après chaque construction, échouer faute de scénario,
-- et laisser une release en `verifying` sur un verdict qui n'arriverait jamais.
--
-- Le catalogue portait déjà `verification: true` par pipeline, ce qui était le bon
-- geste au mauvais niveau : il retire une famille pour **tout le monde**, alors
-- que la question — « ce projet vérifie-t-il ceci ? » — est propre au projet.
--
-- # L'absence de ligne vaut activé
--
-- Même règle que pour les plugins, pour la même raison : traiter l'absence comme
-- un refus désactiverait d'un coup toutes les vérifications de tous les projets
-- déclarés avant cette migration. Un déploiement doit pouvoir appliquer une
-- migration sans que sa chaîne de vérification s'arrête.
--
-- # Pas de clé étrangère vers un catalogue
--
-- Les familles vivent dans le fichier de catalogue, pas en base. Une ligne dont
-- la famille a disparu reste lisible et se voit ; l'effacer silencieusement
-- perdrait un réglage que quelqu'un pourrait vouloir récupérer, et une famille
-- retirée puis remise reviendrait activée sans qu'on l'ait demandé.

-- +goose Up

create table if not exists project_pipelines (
    project_id bigint      not null references projects (id) on delete cascade,
    -- kind est l'identifiant employé par le dashboard, celui du catalogue —
    -- distinct du nom du Pipeline dans le cluster, qui peut être renommé.
    kind       text        not null,
    enabled    boolean     not null default true,
    updated_at timestamptz not null default now(),
    updated_by text        not null default '',
    primary key (project_id, kind)
);

alter table project_pipelines add constraint project_pipelines_kind_non_vide
    check (btrim(kind) <> '');

-- +goose Down
drop table if exists project_pipelines;
