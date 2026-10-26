-- Une rotation de clé se **demande**, elle ne s'exécute pas là où on la demande.
--
-- L'API déclare les projets, mais elle tourne sans jeton de cluster —
-- `automountServiceAccountToken: false` sur son déploiement, et c'est
-- délibéré : c'est la surface exposée. Lui accorder de quoi créer des secrets
-- pour la commodité d'une pose synchrone serait un recul de sécurité déguisé en
-- fonctionnalité.
--
-- La demande est donc posée ici, et le worker — qui a déjà le cluster et le bail
-- de leader — la réalise. C'est aussi ce qui donne au lot G sa boucle de
-- réconciliation : un projet sans clé en reçoit une, une rotation demandée
-- s'exécute, et un redémarrage rattrape ce qu'une panne a interrompu
-- (doc 12 §12.7).
--
-- Séparée de la migration 0034 plutôt qu'ajoutée à elle. Les deux disent la même
-- chose et auraient pu tenir ensemble, mais 0034 avait déjà été appliquée : goose
-- ne rejoue pas une migration enregistrée, et la compléter aurait laissé toute
-- base déjà migrée sans la colonne — un `column does not exist` au premier tour
-- de la boucle, sur un schéma que le fichier décrivait pourtant comme complet.

-- +goose Up
alter table projects add column if not exists deploy_key_rotate_requested_at timestamptz;

-- +goose Down
alter table projects drop column if exists deploy_key_rotate_requested_at;
