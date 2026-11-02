-- Les pull requests d'un projet, recopiées depuis sa forge.
--
-- Recopiées et non lues à chaque affichage : les jobs, les résultats et les plans s'y rattachent,
-- l'historique reste après la fermeture, et l'écran ne dépend pas de la forge. Le worker les tient
-- à jour, et émet un fait à chaque transition (ouverte, nouveau commit, fermée, fusionnée,
-- rouverte).
--
-- `pull_request_id` se pose à côté de `release_id`, avec la même mécanique : un job lancé sur une
-- PR, ses résultats, le plan qui l'a ouvert et les artefacts de ce plan.

-- +goose Up
create table pull_requests (
    id            bigserial   primary key,
    project_id    bigint      not null references projects (id) on delete cascade,
    number        bigint      not null check (number > 0),
    title         text        not null default '',
    author        text        not null default '',
    source_branch text        not null default '',
    target_branch text        not null default '',
    -- Le commit en tête de la branche : ce qu'un pipeline lancé sur la PR construit et teste.
    head_sha      text        not null default '',
    -- `merged` et non `closed` pour une PR fusionnée : une fusion et un abandon n'appellent pas la
    -- même réaction.
    state         text        not null check (state in ('open', 'closed', 'merged')),
    url           text        not null default '',
    created_at    timestamptz,
    updated_at    timestamptz,
    closed_at     timestamptz,
    merged_at     timestamptz,
    synced_at     timestamptz not null default now(),
    unique (project_id, number)
);

create index pull_requests_par_projet on pull_requests (project_id, state, updated_at desc);

alter table jobs add column pull_request_id bigint references pull_requests (id) on delete set null;
alter table test_results add column pull_request_id bigint references pull_requests (id) on delete set null;
alter table plan_runs add column pull_request_id bigint references pull_requests (id) on delete cascade;
alter table plan_artifacts add column pull_request_id bigint references pull_requests (id) on delete cascade;

create index jobs_par_pull_request on jobs (pull_request_id) where pull_request_id is not null;
create index test_results_par_pull_request on test_results (pull_request_id, fact_group)
    where pull_request_id is not null;

-- +goose Down
alter table plan_artifacts drop column if exists pull_request_id;
alter table plan_runs drop column if exists pull_request_id;
alter table test_results drop column if exists pull_request_id;
alter table jobs drop column if exists pull_request_id;
drop table if exists pull_requests;
