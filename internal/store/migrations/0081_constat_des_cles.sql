-- Ce que la vérification d'un projet a constaté sur sa clé, gardé avec le constat d'accès.
--
-- La note sur la clé n'était rendue qu'au test qu'on venait de lancer, puis perdue : la page d'un
-- projet ne pouvait pas dire, plus tard, pourquoi la clé n'était pas constatée. L'adresse des clés
-- du dépôt est celle où la coller, que la forge donne.

-- +goose Up
alter table projects add column key_note text not null default '';
alter table projects add column keys_url text not null default '';

-- +goose Down
alter table projects drop column if exists keys_url;
alter table projects drop column if exists key_note;
