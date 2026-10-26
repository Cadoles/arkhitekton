-- L'exécution des plans, et son avancement.
--
-- Troisième tranche du chantier des automatisations (doc 14). Le modèle de plan
-- existe et exprime la chaîne de vérification ; il lui manquait une mémoire.
-- L'exécution était synchrone et marquait un geste « réussi » dès qu'il était
-- **parti** — un pis-aller assumé, puisque l'avancement ne survivait pas à
-- l'appel et que personne ne venait le corriger.
--
-- # Ce qui déclenche, nommé par une chaîne
--
-- Doc 14 §14.8 prévoyait `reaction_runs (reaction_id, event_id, …)`, avec une
-- table de réactions déclarées. Celle-ci n'existe pas encore — sa forme appartient
-- à l'éditeur, et la deviner maintenant reviendrait à figer des colonnes qu'on
-- n'a pas éprouvées.
--
-- `trigger` porte donc une chaîne : `builtin:verification` aujourd'hui,
-- `reaction:42` quand les réactions déclarées arriveront. C'est la même unicité
-- que celle du document, sous une forme qui n'exige pas de deviner une table.
--
-- # Le plan est **copié** dans son exécution
--
-- Et ce n'est pas une commodité. Le plan est composé à partir du catalogue et de
-- ce que le projet déclare vérifier ; les deux se modifient depuis
-- l'administration, pendant qu'une exécution est en cours. Le relire à chaque
-- avancement ferait glisser les positions des gestes sous les états déjà écrits —
-- une famille retirée décalerait tout ce qui la suit, et un geste conclu se
-- retrouverait rattaché au voisin.
--
-- Une exécution s'achève donc sur le plan qui l'a commencée.

-- +goose Up

create table if not exists plan_runs (
    id bigserial primary key,
    -- Ce qui a déclenché : `builtin:<nom>` ou `reaction:<id>`.
    trigger    text        not null,
    -- La clé du fait déclencheur, celle de `events.dedup_key`.
    --
    -- Une chaîne et non une clé étrangère : l'émission d'un fait peut échouer
    -- sans que l'enchaînement doive s'arrêter, et une contrainte référentielle
    -- rendrait alors l'exécution impossible pour une trace manquante.
    event_key  text        not null,
    project_id bigint      references projects (id) on delete cascade,
    release_id bigint      references releases (id) on delete cascade,
    -- Le plan tel qu'il était au départ. Voir plus haut.
    plan       jsonb       not null,
    -- La charge du fait déclencheur, qui lie les paramètres des gestes.
    --
    -- Le plan **nomme** les gestes ; ce qui dépend du fait — la révision
    -- construite, le tag — est lié à l'exécution. Un plan qui figerait ces
    -- valeurs ne servirait qu'une fois, et c'est ce contexte qui le rend
    -- réutilisable d'une release à l'autre.
    context    jsonb       not null default '{}'::jsonb,
    -- `running`, `succeeded`, `failed`.
    state      text        not null default 'running',
    created_at timestamptz not null default now(),
    finished_at timestamptz
);

-- La borne qui rend une redélivrance inoffensive : le même fait, retraité, ne
-- crée pas une seconde exécution. C'est l'équivalent, pour les plans, de la clé
-- de déduplication des faits.
create unique index if not exists plan_runs_par_fait
    on plan_runs (trigger, event_key);

-- Une carte plate de chaînes, comme partout ailleurs : une valeur imbriquée
-- traverserait le décodage Go sans erreur pour échouer chez le geste, loin de sa
-- cause.
alter table plan_runs add constraint plan_runs_context_plate
    check (not jsonb_path_exists(context, '$.* ? (@.type() <> "string")'));

create index if not exists plan_runs_ouverts
    on plan_runs (state) where state = 'running';

create table if not exists plan_steps (
    id       bigserial primary key,
    run_id   bigint not null references plan_runs (id) on delete cascade,
    -- La position dans le plan. C'est elle qui rattache un état à un geste, et
    -- c'est pourquoi le plan est figé : une position qui glisse rattache l'état
    -- au voisin.
    stage    integer not null,
    position integer not null,
    kind     text    not null,
    ref      text    not null,
    -- `pending`, `running`, `succeeded`, `failed`, `skipped`.
    state    text    not null default 'pending',
    -- Le job enfilé pour un geste de type pipeline. C'est le point de couture
    -- avec la file : un geste de pipeline **est** un job, et n'a pas d'exécuteur
    -- à lui.
    job_id   bigint  references jobs (id) on delete set null,
    detail   text    not null default '',
    started_at  timestamptz,
    finished_at timestamptz
);

create unique index if not exists plan_steps_position
    on plan_steps (run_id, stage, position);

-- La question que pose la conclusion d'un job : quel geste attend ce job ?
create unique index if not exists plan_steps_par_job
    on plan_steps (job_id) where job_id is not null;

-- +goose Down
drop table if exists plan_steps;
drop table if exists plan_runs;
