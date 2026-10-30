-- Les planifications : « à ces heures, exécuter tel plan ».
--
-- Les CronJobs qui créaient des PipelineRun directement contournaient la file, ses plafonds et
-- tout ce que le dashboard lit d'un run. Une planification ouvre un plan, comme une réaction :
-- ses pipelines passent par la file.
--
-- Par projet, comme les réactions, et pour la même raison : une cadence est le choix d'un projet,
-- et deux projets qui veulent la même l'écrivent chacun.
--
-- `next_fire_at` est ce que la boucle du worker interroge. Il est recalculé depuis l'instant
-- présent à chaque passage, jamais depuis le tic manqué : après une indisponibilité, une
-- planification ne tire pas une salve pour rattraper ce qu'elle n'a pas fait.

-- +goose Up
create table schedules (
    id            bigserial primary key,
    project_id    bigint      not null references projects (id) on delete cascade,
    name          text        not null check (btrim(name) <> ''),
    -- L'expression cron, cinq champs, lue dans `timezone` (vide : UTC).
    cron          text        not null check (btrim(cron) <> ''),
    timezone      text        not null default '',
    -- La branche ou le tag que prennent les pipelines, résolu en commit au tic. Vide : la branche
    -- par défaut du dépôt.
    ref           text        not null default '',
    enabled       boolean     not null default true,
    plan          jsonb       not null,
    -- Nul pour une planification éteinte : elle ne tire rien, et ne rattrape rien en se rallumant.
    next_fire_at  timestamptz,
    last_fired_at timestamptz,
    -- Ce que le dernier tic a donné, lisible tel quel : lancé, sauté, manqué, ou l'erreur.
    last_outcome  text        not null default '',
    created_by    text        not null default '',
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);

create unique index schedules_nom_par_projet on schedules (project_id, lower(btrim(name)));
create index schedules_a_tirer on schedules (next_fire_at) where enabled;

-- +goose Down
drop table if exists schedules;
