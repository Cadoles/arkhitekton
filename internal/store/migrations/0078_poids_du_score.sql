-- Le poids de chaque pipeline dans le score, réglé à la main.
--
-- Le score pèse une case par pipeline lancé. Sans réglage, un pipeline pèse ce que pèse sa nature
-- (tests unitaires, intégration…), et une nature que le score ne connaît pas est hors score. Seuls
-- les réglages sont stockés, comme les plafonds de file : recopier les défauts les figerait.

-- +goose Up
create table if not exists score_weights (
    -- Le pipeline, tel que le catalogue le nomme. Pas de clé étrangère : le catalogue vit hors de
    -- la base, et un pipeline retiré ne doit pas faire échouer un réglage voisin.
    kind       text primary key,
    weight     double precision not null check (weight >= 0 and weight <= 100),
    updated_by text not null,
    updated_at timestamptz not null default now()
);

-- +goose Down
drop table if exists score_weights;
