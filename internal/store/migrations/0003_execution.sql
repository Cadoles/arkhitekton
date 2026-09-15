-- +goose Up
-- File d'exécution et runs Tekton.
--
-- `deployments` et `environment_observations` étaient créées ici : la première a
-- été retirée (0050), la seconde appartient au plugin MSE (doc 16 §16.17).

create table jobs (
    id             bigserial primary key,
    kind           text   not null,
    -- Classe d'exclusion mutuelle. 'parallel' n'exclut rien ; 'exclusive-<nom>'
    -- exclut sa propre classe ; 'exclusive-cluster' est une barrière qui exclut
    -- tous les jobs — le cluster Tekton héberge aussi la cible des tests de
    -- charge, donc tout job concurrent fausse la mesure.
    queue_class    text   not null,
    priority       int    not null default 100,
    state          text   not null,
    params         jsonb  not null,
    release_id     bigint references releases (id) on delete set null,
    -- L'environnement visé, en texte : la table des environnements appartient au
    -- plugin MSE, et le noyau ne la référence pas.
    environment_id text,
    requested_by   text   not null,
    requested_at   timestamptz not null default now(),
    started_at     timestamptz,
    finished_at    timestamptz,
    attempt        int    not null default 1,
    -- Une file qui n'explique pas son attente engendre des relances inutiles.
    queue_reason   text check (queue_reason in ('class-busy', 'capacity', 'draining')),
    cancel_reason  text,
    error          text,
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now()
);

-- Index de l'ordonnanceur : sélection FIFO par priorité parmi les jobs en attente.
create index jobs_queue_idx on jobs (queue_class, priority, requested_at) where state = 'queued';

create table runs (
    id               bigserial primary key,
    job_id           bigint references jobs (id) on delete set null,
    pipeline_name    text not null,
    pipelinerun_name text not null unique,
    namespace        text not null,
    status           text not null,
    reason           text,
    started_at       timestamptz,
    completed_at     timestamptz,
    results          jsonb,
    -- true = run déclenché hors dashboard (webhook Gitea, CronJob) : affiché en
    -- lecture seule, jamais piloté.
    observed         boolean not null default false,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now()
);

create table task_runs (
    id           bigserial primary key,
    run_id       bigint  not null references runs (id) on delete cascade,
    name         text    not null,
    display_name text,
    status       text    not null,
    started_at   timestamptz,
    completed_at timestamptz,
    pod_name     text,
    log_object   text,
    log_sealed   boolean not null default false,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),
    unique (run_id, name)
);

-- +goose Down
drop table if exists task_runs;
drop table if exists runs;
drop table if exists jobs;
