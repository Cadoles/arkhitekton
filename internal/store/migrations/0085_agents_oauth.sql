-- Les agents IA autorisés par OAuth à lire et piloter le dashboard par son serveur MCP (`/mcp`).
--
-- Un client s'enregistre seul (RFC 7591) : rien ne l'atteste, son nom est celui qu'il se donne.
-- Ce qui l'autorise est le consentement d'une personne connectée, gardé dans `oauth_grants` avec
-- le sujet et les groupes de sa session : les rôles se résolvent à chaque appel, un rôle retiré
-- coupe donc l'agent aussitôt.
--
-- Les jetons ne sont gardés que sous leur empreinte SHA-256 : une lecture de la base ne donne
-- aucun jeton utilisable.

-- +goose Up
create table oauth_clients (
    id            text primary key,
    name          text not null,
    redirect_uris text[] not null,
    created_at    timestamptz not null default now()
);

-- Une demande d'autorisation attend le consentement, dix minutes au plus.
create table oauth_requests (
    id             text primary key,
    client_id      text not null references oauth_clients (id) on delete cascade,
    redirect_uri   text not null,
    state          text not null default '',
    code_challenge text not null,
    scopes         text[] not null,
    expires_at     timestamptz not null
);

create table oauth_grants (
    id            bigserial primary key,
    client_id     text not null references oauth_clients (id) on delete cascade,
    subject       text not null,
    username      text not null default '',
    email         text not null default '',
    groups        text[] not null default '{}',
    scopes        text[] not null,
    created_at    timestamptz not null default now(),
    last_used     timestamptz,
    -- Le jeton d'accès courant, et le jeton de rafraîchissement courant et celui qu'il a remplacé :
    -- celui-ci revenu, l'autorisation est révoquée.
    access_hash      bytea unique,
    access_expires   timestamptz,
    refresh_hash     bytea unique,
    refresh_expires  timestamptz,
    previous_refresh bytea unique
);

create index oauth_grants_subject on oauth_grants (subject);

-- Un code d'autorisation sert une fois, dix minutes au plus.
create table oauth_codes (
    hash           bytea primary key,
    grant_id       bigint not null references oauth_grants (id) on delete cascade,
    redirect_uri   text not null,
    code_challenge text not null,
    expires_at     timestamptz not null
);

alter table audit_log add column agent text;

-- +goose Down
alter table audit_log drop column agent;
drop table oauth_codes;
drop table oauth_grants;
drop table oauth_requests;
drop table oauth_clients;
