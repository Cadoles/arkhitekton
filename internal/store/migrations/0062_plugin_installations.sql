-- Plugins an administrator installs from an OCI registry, by digest.
--
-- A row is requested by reference, then completed by the job that pulls and verifies it: the
-- plugin's id and version are known only once its manifest is read. Two versions of a plugin may
-- be installed side by side, one of them active for new runs; the others stay loaded to read the
-- runs they launched.

-- +goose Up
create table plugin_installations (
    id           bigserial primary key,
    reference    text not null,
    digest       text not null check (digest ~ '^sha256:[0-9a-f]{64}$'),
    plugin_id    text check (plugin_id ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'),
    version      text,
    signed_by    text,
    object       text,
    state        text not null default 'pending' check (state in ('pending', 'installed', 'failed', 'removed')),
    error        text not null default '',
    active       boolean not null default false,
    requested_by text not null,
    requested_at timestamptz not null default now(),
    installed_at timestamptz,
    removed_at   timestamptz,
    check (state <> 'installed' or (plugin_id is not null and version is not null and signed_by is not null and object is not null)),
    check (not active or state = 'installed')
);

create unique index plugin_installations_version on plugin_installations (plugin_id, version) where state = 'installed';
create unique index plugin_installations_active on plugin_installations (plugin_id) where active;

-- +goose Down
drop table plugin_installations;
