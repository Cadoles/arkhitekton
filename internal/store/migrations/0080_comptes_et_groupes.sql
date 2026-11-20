-- Les comptes, et les rôles donnés à un groupe du fournisseur d'identité.
--
-- `accounts` garde toute personne qui s'est connectée, avec ou sans rôle. Sans lui, une personne
-- connectée sans rôle ne laissait qu'une session, purgée à son expiration : l'administrateur ne
-- pouvait pas la voir, et devait lui demander son identifiant pour lui donner accès.
--
-- `group_roles` donne un rôle à un groupe que le fournisseur met dans le jeton (une organisation ou
-- une équipe de la forge) : quiconque en est membre en hérite. Mêmes règles que `user_roles` : au
-- plus un rôle global et un par projet, `admin` seulement global.
--
-- `sessions.groups` garde les groupes lus à la connexion : c'est sur eux que les rôles de groupe se
-- résolvent, à chaque requête.

-- +goose Up
create table accounts (
    subject    text primary key,
    username   text,
    email      text,
    groups     text[]      not null default '{}',
    first_seen timestamptz not null default now(),
    last_seen  timestamptz not null default now()
);

-- Les personnes déjà connectées avant cette table.
insert into accounts (subject, username, first_seen, last_seen)
select subject, max(username), min(coalesce(last_seen, granted_at)), max(last_seen)
from user_roles
where last_seen is not null
group by subject
on conflict (subject) do nothing;

create table group_roles (
    group_name text        not null,
    project_id bigint references projects (id) on delete cascade,
    role       text        not null check (role in ('viewer', 'operator', 'releaser', 'admin')),
    granted_by text        not null,
    granted_at timestamptz not null default now(),
    constraint group_roles_admin_global check (role <> 'admin' or project_id is null)
);

create unique index group_roles_global_key on group_roles (group_name) where project_id is null;
create unique index group_roles_project_key on group_roles (group_name, project_id) where project_id is not null;

alter table sessions add column groups text[] not null default '{}';

-- +goose Down
alter table sessions drop column if exists groups;
drop table if exists group_roles;
drop table if exists accounts;
