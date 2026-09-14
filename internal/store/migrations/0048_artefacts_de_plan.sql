-- Les artefacts qu'une exécution de plan a produits.
--
-- # Pourquoi une table, et pas les résultats du run
--
-- Les résultats Tekton sont déjà stockés sur `runs`. Les relire par job à chaque
-- besoin ferait dépendre l'étape de test de la projection d'un run — donc du
-- moment où l'observateur l'a vue passer — alors que ce qu'elle veut est une
-- **propriété du plan** : « ce que le geste nommé `portal` a produit ».
--
-- La table donne cette lecture directement, et survit à ce que Tekton oublie :
-- les PipelineRun sont ramassés, les artefacts d'une release livrée ne doivent
-- pas disparaître avec eux.
--
-- # La clé
--
-- Un geste est désigné par sa position dans le plan **figé** — étape, rang —
-- exactement comme `plan_steps`. Le libellé du geste sert à le nommer dans les
-- gabarits, mais il ne fait pas la clé : deux gestes peuvent le partager, et un
-- plan renommé ne doit pas orpheliner ce qu'il a produit.

-- +goose Up
create table plan_artifacts (
    id          bigserial primary key,
    run_id      bigint not null references plan_runs (id) on delete cascade,
    stage       int    not null,
    position    int    not null,
    -- Name est le nom du résultat rendu par le pipeline : `image`, `tag`.
    name        text   not null,
    -- Kind reprend le vocabulaire de `produces` du catalogue — `image`,
    -- `results` — et vaut '' quand le pipeline n'en déclare aucun. Descriptif :
    -- rien ne s'y branche, mais un artefact sans nature ne se relit pas.
    kind        text   not null default '',
    -- Reference est ce qui désigne l'artefact : pour une image, sa référence
    -- complète avec son tag.
    reference   text   not null,
    -- ReleaseID duplique ce que l'exécution porte déjà, et c'est délibéré :
    -- l'écran d'une release liste ses artefacts sans avoir à joindre les plans,
    -- et une exécution purgée ne doit pas emporter ce qu'elle a publié.
    release_id  bigint references releases (id) on delete cascade,
    created_at  timestamptz not null default now(),

    -- Un geste ne produit qu'une fois chaque nom. Une redélivrance réécrit
    -- plutôt que d'empiler : le second passage constate la même chose.
    unique (run_id, stage, position, name)
);

create index plan_artifacts_par_release on plan_artifacts (release_id, created_at desc)
    where release_id is not null;

-- +goose Down
drop table plan_artifacts;
