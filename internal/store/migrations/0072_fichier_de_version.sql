-- Le fichier du dépôt qui porte la version, `version.txt` par exemple. Renseigné, le lancement
-- d'une release y commite son tag sur la branche qu'elle construit. Vide, rien n'est commité.

-- +goose Up
alter table projects add column version_file text not null default '';

-- +goose Down
alter table projects drop column version_file;
