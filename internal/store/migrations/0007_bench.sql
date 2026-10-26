-- +goose Up
-- Mesures de charge, baselines, et conditions de mesure constatées.

-- Conditions constatées au démarrage d'un job.
--
-- `test_results` porte déjà `exclusive` et `concurrent_jobs`, mais ces colonnes
-- sont renseignées à l'arrivée du résultat — c'est-à-dire minutes après le
-- démarrage, quand plus rien ne dit ce qui tournait au moment de la mesure. Le
-- constat doit donc être relevé à la prise du job et conservé jusque-là.
--
-- Deux colonnes et non une : la barrière draine avant de lancer, donc
-- `exclusive` vrai devrait impliquer zéro run concurrent. Si les deux divergent,
-- c'est le constat qui fait foi, et il faut pouvoir voir la divergence (B16).
alter table jobs
    add column exclusive_at_start  boolean,
    add column concurrent_at_start int;

-- Mesures d'une campagne de charge.
--
-- Table dédiée plutôt que `test_cases` : un cas de test porte un statut et une
-- durée, une mesure porte une valeur, une unité et un sens de comparaison. Les
-- loger ensemble obligerait à laisser la moitié des colonnes nulles de chaque
-- côté.
create table bench_measures (
    id             bigserial primary key,
    test_result_id bigint not null references test_results (id) on delete cascade,
    -- Le scénario est l'unité d'historique : comparer « panier » à « recherche »
    -- n'a aucun sens, et c'est par scénario que la tendance se lit.
    scenario text not null,
    -- `http_req_duration:p(95)`. Le nom de la métrique et la statistique retenue
    -- ensemble : la moyenne et le 95e centile d'une même métrique sont deux
    -- mesures distinctes, avec deux baselines distinctes.
    metric text   not null,
    label  text   not null,
    unit   text   not null,
    value  double precision not null,
    -- Recopiés depuis le catalogue de métriques : une mesure enregistrée doit
    -- pouvoir être relue et comparée sans reconstruire un catalogue qui aura pu
    -- changer entre-temps.
    lower_better boolean not null,
    volume       boolean not null default false,
    -- Verdict rendu par k6 sur ses propres seuils. Distinct de la comparaison à
    -- la baseline : k6 dit « le contrat est tenu », la baseline dit « c'est aussi
    -- bon qu'avant ». Null quand aucun seuil n'est déclaré.
    threshold_ok boolean,
    created_at   timestamptz not null default now()
);

create index bench_measures_result_idx on bench_measures (test_result_id);
-- L'index de l'historique : les six derniers points d'un scénario et d'une
-- métrique, ce que le Sparkline affiche.
create index bench_measures_history_idx on bench_measures (scenario, metric, created_at desc);

-- Baseline épinglée, et non calculée.
--
-- Le choix est celui qui rend la dérive visible. Une baseline glissante — la
-- moyenne des dernières campagnes, ou la campagne précédente — compare chaque
-- release à la précédente : la performance peut être divisée par deux en dix
-- releases sans qu'aucune comparaison ne dépasse jamais les 10 % de tolérance.
-- Chaque verdict serait vert, et la régression invisible.
--
-- Une baseline épinglée désigne une campagne de référence choisie par un humain.
-- Elle vieillit, et c'est justement ce qui la rend utile : l'écart s'accumule au
-- lieu de se réinitialiser à chaque mesure.
create table bench_baselines (
    scenario   text not null,
    metric     text not null,
    -- La mesure de référence, et non sa seule valeur : il faut pouvoir remonter à
    -- la campagne, à ses conditions et à sa date pour juger si la référence est
    -- encore pertinente.
    measure_id bigint not null references bench_measures (id) on delete cascade,
    pinned_by  text not null,
    pinned_at  timestamptz not null default now(),
    -- Une seule baseline par couple scénario / métrique : deux références
    -- concurrentes rendraient le verdict indéterminé.
    primary key (scenario, metric)
);

-- +goose Down
drop table if exists bench_baselines;
drop table if exists bench_measures;
alter table jobs
    drop column if exists exclusive_at_start,
    drop column if exists concurrent_at_start;
