-- +goose Up
-- Référentiel : versions Redmine, demandes.
--
-- La table `environments` était créée ici. Elle appartient au plugin MSE, qui la
-- crée par ses propres migrations (doc 16 §16.17).

create table versions (
    id                 bigserial primary key,
    redmine_id         bigint      not null unique,
    name               text        not null,
    description        text,
    status             text        not null,
    due_date           date,
    redmine_updated_on timestamptz,
    synced_at          timestamptz not null,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now()
);

-- Projection statut Redmine -> bucket, éditable sans redéploiement : les statuts
-- sont propres au workflow du client et changent sans prévenir.
create table issue_status_mapping (
    status_name text primary key,
    bucket      text not null check (bucket in ('todo', 'doing', 'done', 'rejected')),
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);

create table issues (
    id                 bigserial primary key,
    redmine_id         bigint  not null unique,
    version_id         bigint  references versions (id) on delete set null,
    tracker            text    not null,
    subject            text    not null,
    status_name        text    not null,
    status_bucket      text    not null check (status_bucket in ('todo', 'doing', 'done', 'rejected')),
    priority           text,
    assignee           text,
    done_ratio         int     not null default 0,
    is_closed          boolean not null default false,
    redmine_updated_on timestamptz,
    synced_at          timestamptz not null,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now()
);

create index issues_version_bucket_idx on issues (version_id, status_bucket);
-- Les anomalies ouvertes d'une version sont comptées à part du taux de
-- complétion : une version peut être fonctionnellement complète et en porter.
create index issues_open_by_tracker_idx on issues (version_id, tracker) where is_closed = false;

-- État des synchronisations. `last_ok` est distinct de `last_run` pour que
-- l'interface puisse afficher « données vieilles de 14 minutes, dernière
-- synchronisation en échec » plutôt qu'un chiffre muet.
create table sync_state (
    source     text primary key,
    cursor     text,
    last_run   timestamptz,
    last_ok    timestamptz,
    last_error text,
    updated_at timestamptz not null default now()
);

-- +goose Down
drop table if exists sync_state;
drop table if exists issues;
drop table if exists issue_status_mapping;
drop table if exists versions;
