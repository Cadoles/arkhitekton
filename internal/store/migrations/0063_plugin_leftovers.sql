-- What an uninstalled plugin leaves behind: its tables, which an administrator may purge.
--
-- The version that was active when the plugin was uninstalled is its leftover: its layer, which
-- carries the migrations that undo the tables, is kept until the plugin is purged or installed
-- again. The version table is recorded when a version is installed, so that the screen says what a
-- purge would drop without reading the layer.

-- +goose Up
alter table plugin_installations
    add column migrations_table text not null default '',
    add column leftover boolean not null default false,
    add check (not leftover or (state = 'removed' and migrations_table <> ''));

create unique index plugin_installations_leftover on plugin_installations (plugin_id) where leftover;

-- +goose Down
drop index plugin_installations_leftover;
alter table plugin_installations drop column leftover, drop column migrations_table;
