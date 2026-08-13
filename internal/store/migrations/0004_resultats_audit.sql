-- +goose Up
-- Résultats de tests, exports, identités et audit.

create table test_results (
    id          bigserial primary key,
    run_id      bigint not null references runs (id) on delete cascade,
    release_id  bigint references releases (id) on delete set null,
    family      text   not null check (family in ('unit', 'integration', 'e2e', 'load', 'static', 'security')),
    tool        text   not null,
    status      text   not null check (status in ('pass', 'warn', 'fail', 'skip', 'na')),
    total       int,
    passed      int,
    failed      int,
    skipped     int,
    flaky       int,
    duration_ms bigint,
    summary     text,
    raw_object  text,
    parsed      jsonb,
    parsed_at   timestamptz,
    -- Conditions de mesure, renseignées pour family = 'load'. Le cluster Tekton
    -- héberge aussi la cible de charge : sans ces colonnes, la comparaison à la
    -- baseline confronterait des mesures prises dans des conditions différentes.
    exclusive       boolean,
    concurrent_jobs int,
    created_at  timestamptz not null default now()
);

create index test_results_release_family_idx on test_results (release_id, family);

-- Cas de test individuels : permet de répondre à « ce test échoue-t-il depuis
-- longtemps ? », question qui revient en recette et qu'un stockage JSONB seul
-- rendrait coûteuse.
create table test_cases (
    id              bigserial primary key,
    test_result_id  bigint not null references test_results (id) on delete cascade,
    suite           text   not null,
    name            text   not null,
    status          text   not null check (status in ('passed', 'failed', 'skipped', 'flaky')),
    duration_ms     int,
    failure_message text,
    failure_trace   text
);

create index test_cases_result_status_idx on test_cases (test_result_id, status);
create index test_cases_identity_idx on test_cases (suite, name);

create table artifacts (
    id           bigserial primary key,
    release_id   bigint references releases (id) on delete cascade,
    kind         text not null,
    format       text not null,
    object       text not null,
    size_bytes   bigint,
    generated_by text not null,
    generated_at timestamptz not null default now()
);

-- Les permissions sont gérées manuellement dans le dashboard : Gitea expose bien
-- un scope `groups`, mais l'attribution retenue est explicite.
create table user_roles (
    subject    text primary key,
    username   text,
    role       text not null check (role in ('viewer', 'operator', 'releaser', 'admin')),
    granted_by text not null,
    granted_at timestamptz not null default now(),
    last_seen  timestamptz
);

-- Sessions côté serveur : le navigateur ne détient qu'un identifiant opaque.
-- C'est ce qui permet de révoquer une session, impossible avec un JWT auto-porté.
create table sessions (
    id         text primary key,
    subject    text        not null,
    username   text,
    email      text,
    expires_at timestamptz not null,
    created_at timestamptz not null default now(),
    last_used  timestamptz
);

create index sessions_expiry_idx on sessions (expires_at);

-- Le dashboard déclenche des promotions en production. « Qui a promu quoi, et
-- quand » doit avoir une réponse sans consulter les logs applicatifs.
create table audit_log (
    id      bigserial primary key,
    actor   text not null,
    action  text not null,
    target  text,
    payload jsonb,
    at      timestamptz not null default now()
);

create index audit_log_recent_idx on audit_log (at desc);
create index audit_log_target_idx on audit_log (target, at desc);

-- +goose Down
drop table if exists audit_log;
drop table if exists sessions;
drop table if exists user_roles;
drop table if exists artifacts;
drop table if exists test_cases;
drop table if exists test_results;
