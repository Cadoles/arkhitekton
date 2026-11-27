-- Deux plugins compilés dans un déploiement fusionnaient ici en un seul, le plugin qui déploie.
--
-- Il est depuis installé à part, et migre ses propres réglages : une base neuve n'a rien à reprendre
-- ici. Le numéro reste pour que les bases qui l'ont déjà appliquée gardent un historique continu.

-- +goose Up
select 1;

-- +goose Down
select 1;
