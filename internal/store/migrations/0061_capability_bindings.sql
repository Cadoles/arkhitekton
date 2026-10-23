-- Capability bindings an administrator records, completing the catalogue file.
--
-- The file wins for any capability it binds; a recorded binding never names a service account.
-- The counter is what the running dashboard watches: it moves whenever bindings or installed plugins
-- change, and the capabilities in force are then built again.

-- +goose Up
create table capability_bindings (
    ref        text primary key check (ref ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?/[a-z0-9]([a-z0-9-]*[a-z0-9])?$'),
    binding    jsonb not null,
    updated_by text not null,
    updated_at timestamptz not null default now()
);

create table capability_state (
    singleton boolean primary key default true check (singleton),
    version   bigint not null default 0
);
insert into capability_state default values;

-- +goose Down
drop table capability_state;
drop table capability_bindings;
