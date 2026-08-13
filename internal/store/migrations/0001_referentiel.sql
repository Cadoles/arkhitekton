-- +goose Up
-- Référentiel : environnements, versions Redmine, demandes.
--
-- Les environnements sont une table et non des constantes : leur libellé, leur
-- rang et surtout leur suffixe de promotion sont propres au déploiement, et
-- `qualif` est désigné `testing` par Flux. Coder cette correspondance en dur
-- reviendrait à promouvoir vers le mauvais environnement au premier oubli.

create table environments (
    id                 text primary key,
    label              text        not null,
    -- LECTURE : anciens noms reconnus en entrée (ex. {'testing'} pour 'qualif').
    aliases            text[]      not null default '{}',
    -- ÉCRITURE : métadonnée SemVer attendue par Flux dans le tag de promotion.
    -- Une erreur ici ne produit pas d'échec visible : le tag est poussé, Flux
    -- l'ignore, et la promotion paraît réussie. D'où la contrainte d'unicité et
    -- la vérification au démarrage contre les ImagePolicy du cluster.
    promotion_suffix   text        not null,
    -- Une plage SemVer sans composante de pré-release ne sélectionne aucune RC.
    -- Promouvoir une pré-release vers un environnement qui n'en accepte pas
    -- produit le même échec silencieux : refusé en amont plutôt que subi.
    accepts_prerelease boolean     not null,
    rank               int         not null,
    namespace          text        not null,
    flux_kustomization text,
    security_policy    text,
    -- false pour la production, dans un cluster indépendant : ni PortalData, ni
    -- Deployment, ni statut Flux n'y sont lisibles.
    observable         boolean     not null default true,
    visible            boolean     not null default true,
    created_at         timestamptz not null default now(),
    updated_at         timestamptz not null default now(),
    unique (rank),
    unique (promotion_suffix)
);

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
drop table if exists environments;
