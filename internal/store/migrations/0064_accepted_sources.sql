-- Les sources qu'un administrateur a acceptées, et l'état qui attend sa décision.
--
-- Installer un plugin est de la responsabilité de l'administrateur : il saisit un digest, ce qui
-- est l'assertion la plus forte qu'on puisse faire sur un artefact. Face à une source que
-- l'instance ne reconnaît pas, le refuser sèchement l'obligeait à passer par
-- `PLUGIN_ALLOW_UNSIGNED`, donc par un déploiement, pour une décision qui est la sienne.
--
-- `plugin_id` et non une clé : la signature est au format « simple signing » de cosign, qui ne
-- porte pas la clé publique. Il n'y a donc rien à retenir d'un signataire inconnu, et ce que
-- l'acceptation retient est le plugin, pas son auteur.

-- +goose Up
create table accepted_sources (
    plugin_id   text primary key check (plugin_id ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'),
    -- La raison constatée au moment de l'acceptation : « aucune signature », « signée par aucune
    -- clé de confiance ». Elle date, et c'est voulu : elle dit ce qui a été accepté, pas ce qui
    -- serait constaté aujourd'hui.
    reason      text not null default '',
    accepted_by text not null,
    accepted_at timestamptz not null default now()
);

alter table plugin_installations drop constraint plugin_installations_state_check;
alter table plugin_installations add constraint plugin_installations_state_check
    check (state in ('pending', 'installed', 'failed', 'removed', 'unrecognised'));

-- +goose Down
alter table plugin_installations drop constraint plugin_installations_state_check;
update plugin_installations set state = 'failed' where state = 'unrecognised';
alter table plugin_installations add constraint plugin_installations_state_check
    check (state in ('pending', 'installed', 'failed', 'removed'));
drop table if exists accepted_sources;
