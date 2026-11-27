-- Réglages du déployeur rendus opaques dans `environments.deployer_settings`.
--
-- Son contenu appartient désormais au plugin qui déploie, qui crée ses tables dans leur
-- état final. Le numéro reste
-- pour que les bases qui l'ont déjà appliquée gardent un historique continu.

-- +goose Up
select 1;

-- +goose Down
select 1;
