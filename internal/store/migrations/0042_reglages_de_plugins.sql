-- Les réglages des plugins, à deux portées.
--
-- Doc 12 §12.5 nomme cinq points d'extension — suivi, forge, déployeur,
-- attestation, livrables — et tranche le mécanisme : un registre à la
-- compilation. Le binaire sait donc quels plugins existent ; ce qu'il ignore,
-- c'est **lesquels sont employés et comment ils sont réglés**. C'est ce que ces
-- deux tables portent.
--
-- # Pourquoi deux portées et non une
--
-- Un plugin sert d'abord les spécificités d'un projet, ce qui plaide pour une
-- configuration par projet. Mais l'URL d'une forge ou le namespace de Flux sont
-- des valeurs de l'instance, et les redemander à chaque projet serait une saisie
-- répétée dont la moindre divergence se paierait en pannes obscures.
--
-- D'où la règle : le global porte les défauts, le projet porte ses écarts. Un
-- champ vide au niveau d'un projet **reprend** celui de l'instance, il ne
-- l'efface pas — sans quoi un formulaire qui n'affiche pas une clé secrète la
-- perdrait au premier enregistrement.
--
-- # L'activation, et ce que l'absence de ligne veut dire
--
-- `plugin_settings.enabled` dit qu'un plugin est disponible sur cette instance.
-- `project_plugins.enabled` est la décision explicite d'un projet ; **l'absence
-- de ligne vaut héritage du global**, et non refus.
--
-- Ce choix n'est pas cosmétique : traiter l'absence comme un refus désactiverait
-- d'un coup, pour tous les projets déclarés avant cette migration, le bon de
-- livraison et l'attestation qu'ils reçoivent aujourd'hui. Un déploiement doit
-- pouvoir appliquer une migration sans que des livrables cessent d'être proposés.
--
-- # Les valeurs sensibles
--
-- La colonne `config` porte aussi les champs secrets — une clé d'API de suivi —,
-- mais **scellés** par `internal/secrets` avant d'y entrer. La règle « aucun
-- identifiant en base » (doc 12 §12.7) visait les identifiants de registre, dont
-- le consommateur est Tekton : le dashboard n'a qu'à désigner un Secret du
-- cluster, et n'a jamais besoin de le lire. Une clé de suivi a un autre
-- consommateur, le dashboard lui-même, et aucune désignation ne le dispense de la
-- présenter à chaque appel.
--
-- L'abaissement est réel et assumé : une sauvegarde de la base devient un porteur
-- de secrets, et la clé de scellement doit être sauvegardée ailleurs qu'elle.

-- +goose Up

create table if not exists plugin_settings (
    plugin_id  text primary key,
    -- Le plugin est disponible sur cette instance. Faux le retire des écrans de
    -- projet : un interrupteur qu'on ne peut pas atteindre vaut mieux qu'un
    -- réglage qui ne fait rien.
    enabled    boolean     not null default true,
    config     jsonb       not null default '{}'::jsonb,
    updated_at timestamptz not null default now(),
    -- Qui a changé le réglage. Vide pour ce que l'amorçage a posé lui-même : la
    -- distinction compte, elle dit qu'une valeur vient de la configuration du
    -- déploiement et non d'une décision humaine.
    updated_by text        not null default ''
);

-- Pas de clé étrangère vers un catalogue de plugins : le registre vit dans le
-- binaire, pas en base. Une ligne dont le plugin a disparu d'une version reste
-- lisible et se voit — l'effacer silencieusement perdrait une configuration que
-- quelqu'un pourrait vouloir récupérer.

create table if not exists project_plugins (
    project_id bigint      not null references projects (id) on delete cascade,
    plugin_id  text        not null,
    enabled    boolean     not null default true,
    config     jsonb       not null default '{}'::jsonb,
    updated_at timestamptz not null default now(),
    updated_by text        not null default '',
    primary key (project_id, plugin_id)
);

-- La configuration est une carte plate de chaînes, à l'image de
-- `environments.deployer_settings`. Une valeur imbriquée ou numérique
-- traverserait le décodage Go sans erreur ici pour échouer chez le plugin, loin
-- de sa cause.
alter table plugin_settings add constraint plugin_settings_config_plate
    check (not jsonb_path_exists(config, '$.* ? (@.type() <> "string")'));

alter table project_plugins add constraint project_plugins_config_plate
    check (not jsonb_path_exists(config, '$.* ? (@.type() <> "string")'));

-- L'identifiant n'est jamais vide : une ligne sans plugin ne se rattache à rien
-- et ne se corrige que par une suppression manuelle.
alter table plugin_settings add constraint plugin_settings_id_non_vide
    check (btrim(plugin_id) <> '');

alter table project_plugins add constraint project_plugins_id_non_vide
    check (btrim(plugin_id) <> '');

-- +goose Down
drop table if exists project_plugins;
drop table if exists plugin_settings;
