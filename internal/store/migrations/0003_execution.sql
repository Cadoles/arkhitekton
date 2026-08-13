-- +goose Up
-- File d'exécution, runs Tekton, déploiements observés.

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
    environment_id text   references environments (id),
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

-- Une promotion : le dashboard re-tague, Flux déploie. L'opération est
-- asynchrone et le dashboard la suit sans la piloter.
create table deployments (
    id             bigserial primary key,
    environment_id text   not null references environments (id),
    release_id     bigint not null references releases (id),
    state          text   not null,
    job_id         bigint references jobs (id),
    requested_at   timestamptz not null,
    requested_by   text not null,
    observed_at    timestamptz,
    superseded_at  timestamptz,
    failure_reason text,
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now()
);

-- Un environnement n'a qu'une promotion courante. La contrainte est dans le
-- schéma, pas dans le code applicatif.
create unique index deployments_current_idx on deployments (environment_id) where superseded_at is null;

-- Ce que l'environnement dit réellement, distinct de ce que le dashboard a promu.
create table environment_observations (
    environment_id      text primary key references environments (id),
    observed_at         timestamptz not null,
    -- Preuve d'exécution : PortalData, alimenté par le job de migration. Porte
    -- la version de l'IMAGE, à rapprocher de releases.image_version.
    portal_version      text,
    migration_state     text,
    images              jsonb,
    flux_state          text,
    -- Ce que Flux a commité dans mse-kustom : seule source lisible sans accès au
    -- cluster, donc la couverture principale de la production.
    git_tag             text,
    git_commit          text,
    git_committed_at    timestamptz,
    resolved_release_id bigint references releases (id),
    -- D'où vient l'information : 'portaldata' atteste qu'une version tourne et
    -- que ses migrations ont abouti, 'git' seulement que Flux a retenu ce tag.
    resolution          text not null check (resolution in ('portaldata', 'digest', 'git', 'unobservable')),
    updated_at          timestamptz not null default now()
);

-- +goose Down
drop table if exists environment_observations;
drop table if exists deployments;
drop table if exists task_runs;
drop table if exists runs;
drop table if exists jobs;
