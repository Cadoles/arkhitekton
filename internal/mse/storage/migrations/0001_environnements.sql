-- Les tables du plugin MSE : environnements, promotions demandées, relevés,
-- validations de recette (doc 16 §16.17).
--
-- Elles étaient créées par les migrations du noyau 0001, 0002, 0003, 0006, 0011,
-- 0032 et 0033, retirées depuis de son historique. Cette migration reprend leur
-- schéma **final** tel qu'il est en service, et ne crée que ce qui manque : sur
-- une base existante elle ne fait rien, sur une base neuve elle pose tout.
--
-- Les noms de contraintes sont ceux que PostgreSQL a donnés en service. Les
-- reprendre ici est ce qui permet à une migration future de les nommer sans
-- savoir d'où vient la base.

-- +goose Up
create table if not exists environments (
    id                text primary key,
    label             text        not null,
    -- LECTURE : anciens noms reconnus en entrée (ex. {'testing'} pour 'qualif').
    aliases           text[]      not null default '{}',
    rank              int         not null unique,
    namespace         text        not null,
    security_policy   text,
    -- false pour la production, dans un cluster indépendant.
    observable        boolean     not null default true,
    visible           boolean     not null default true,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    -- Les réglages de Flux, dans son vocabulaire. Une carte de chaînes : une
    -- valeur imbriquée ou numérique échouerait chez le déployeur, loin de sa cause.
    deployer_settings jsonb       not null default '{}'::jsonb
        constraint environments_deployer_settings_plates
        check (not jsonb_path_exists(deployer_settings, '$.* ? (@.type() <> "string")'))
);

-- Ce qui a été **demandé** : pousser le tag ne déploie rien, Flux constate ensuite.
create table if not exists image_promotions (
    id             bigserial primary key,
    release_id     bigint      not null references releases (id) on delete cascade,
    environment_id text        not null references environments (id),
    image_name     text        not null,
    tag            text        not null,
    -- Inconnu au moment de la demande : le re-tag n'a pas encore eu lieu.
    digest         text,
    pushed_at      timestamptz not null,
    created_at     timestamptz not null default now(),
    job_id         bigint      references jobs (id) on delete set null,
    unique (release_id, environment_id, image_name)
);

create index if not exists image_promotions_job_idx on image_promotions (job_id) where job_id is not null;

-- Ce que l'environnement dit réellement, distinct de ce qui a été promu.
create table if not exists environment_observations (
    environment_id      text primary key references environments (id),
    observed_at         timestamptz not null,
    -- La version de l'IMAGE, à rapprocher de releases.image_version.
    portal_version      text,
    migration_state     text,
    images              jsonb,
    declared_state      text,
    git_tag             text,
    git_commit          text,
    git_committed_at    timestamptz,
    resolved_release_id bigint references releases (id),
    resolution          text not null check (resolution in ('portaldata', 'digest', 'git', 'unobservable')),
    updated_at          timestamptz not null default now()
);

-- Une validation porte sur « cette release, dans cet environnement ».
create table if not exists release_signoffs (
    id             bigserial primary key,
    release_id     bigint      not null references releases (id) on delete cascade,
    environment_id text        not null references environments (id),
    role           text        not null,
    status         text        not null check (status in ('pass', 'warn', 'fail')),
    detail         text,
    decided_by     text        not null,
    decided_at     timestamptz not null default now(),
    unique (release_id, environment_id, role)
);

create index if not exists release_signoffs_release_idx on release_signoffs (release_id, environment_id);

-- +goose Down
drop table if exists release_signoffs;
drop table if exists environment_observations;
drop table if exists image_promotions;
drop table if exists environments;
