-- La promotion devenait un fait du plugin qui déploie : les réactions déclarées sur l'ancien type le
-- suivaient.
--
-- Ce plugin est depuis installé à part : une base neuve n'a aucune réaction à reprendre ici. Le
-- numéro reste pour que les bases qui l'ont déjà appliquée gardent un historique continu.

-- +goose Up
select 1;

-- +goose Down
select 1;
