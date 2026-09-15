-- Rattachement d'une promotion au job qui l'exécute.
--
-- Son contenu appartient désormais au plugin MSE, qui crée ses tables dans leur
-- état final (`internal/mse/storage/migrations`, doc 16 §16.17). Le numéro reste
-- pour que les bases qui l'ont déjà appliquée gardent un historique continu.

-- +goose Up
select 1;

-- +goose Down
select 1;
