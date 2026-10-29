-- A table of the demonstration plugin's own, for trying a purge.

-- +goose Up
create table providers_demo_notes (
    id         bigserial primary key,
    body       text not null,
    created_at timestamptz not null default now()
);

-- +goose Down
drop table providers_demo_notes;
