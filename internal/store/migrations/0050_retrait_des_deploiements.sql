-- Retrait de `deployments`, créée par 0003 et jamais alimentée.
--
-- Elle devait suivre une promotion de bout en bout — re-tag, rapprochement Flux,
-- rollout, migrations. Ce suivi s'est fait autrement : `image_promotions` porte
-- ce qui a été demandé, `environment_observations` ce qui a été constaté. Aucun
-- code ne lit ni n'écrit cette table (doc 16 §16.1).

-- +goose Up
drop table if exists deployments;

-- +goose Down
-- Rien à recréer : `deployments` n'est plus créée par 0003, et elle référençait
-- `environments`, qui appartient au plugin MSE (doc 16 §16.17).
select 1;
