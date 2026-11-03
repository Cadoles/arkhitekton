-- Les mises à jour des plugins.
--
-- Un plugin suit désormais son dépôt par défaut : l'administrateur donne le dépôt, le dashboard y
-- cherche régulièrement une version plus récente, et la propose. Il peut toujours l'épingler à un
-- digest, comme avant ; le dashboard dit alors seulement qu'une version plus récente existe.
--
-- `plugin_sources` est ce qu'un plugin suit, un par plugin installé, et ce que la dernière
-- vérification a trouvé. Une installation dit si elle suit son dépôt (`follows`), quelle version son
-- étiquette annonçait (`expected_version`, vérifiée contre le manifeste), et laquelle elle remplace
-- (`replaces`) : une mise à jour active la nouvelle et retire l'ancienne, réglages et liaisons
-- conservés.
--
-- Une installation demandée par dépôt ou par étiquette n'a pas encore de digest : c'est le worker,
-- qui détient les identifiants des registres, qui le résout avant de tirer. L'api n'y accède pas.
--
-- Les plugins déjà installés l'ont été par digest : ils sont repris épinglés, ce qu'ils étaient.

-- +goose Up
alter table plugin_installations
    add column follows boolean not null default false,
    add column expected_version text not null default '',
    add column replaces bigint references plugin_installations (id),
    drop constraint plugin_installations_digest_check,
    add constraint plugin_installations_digest_check
        check (digest = '' or digest ~ '^sha256:[0-9a-f]{64}$'),
    add constraint plugin_installations_resolved check (state = 'pending' or state = 'failed' or digest <> '');

create table plugin_sources (
    plugin_id          text primary key check (plugin_id ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'),
    repository         text not null,
    pinned             boolean not null default true,
    latest_version     text not null default '',
    latest_digest      text not null default '',
    checked_at         timestamptz,
    check_error        text not null default '',
    check_requested_at timestamptz
);

insert into plugin_sources (plugin_id, repository, pinned)
select plugin_id, split_part(reference, '@', 1), true
from plugin_installations
where active
on conflict (plugin_id) do nothing;

-- +goose Down
drop table if exists plugin_sources;
delete from plugin_installations where digest = '';
alter table plugin_installations
    drop constraint if exists plugin_installations_resolved,
    drop constraint plugin_installations_digest_check,
    add constraint plugin_installations_digest_check check (digest ~ '^sha256:[0-9a-f]{64}$'),
    drop column if exists replaces,
    drop column if exists expected_version,
    drop column if exists follows;
