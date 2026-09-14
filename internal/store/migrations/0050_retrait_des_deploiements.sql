-- Retrait de `deployments`, créée par 0003 et jamais alimentée.
--
-- Elle devait suivre une promotion de bout en bout — re-tag, rapprochement Flux,
-- rollout, migrations. Ce suivi s'est fait autrement : `image_promotions` porte
-- ce qui a été demandé, `environment_observations` ce qui a été constaté. Aucun
-- code ne lit ni n'écrit cette table (doc 16 §16.1).

-- +goose Up
drop table if exists deployments;

-- +goose Down
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
create unique index deployments_current_idx on deployments (environment_id) where superseded_at is null;
