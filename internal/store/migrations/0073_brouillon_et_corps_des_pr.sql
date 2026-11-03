-- Une demande de fusion en brouillon n'appelle pas encore de vérification, et son corps peut porter
-- des directives (`mse-kustom: <branche>`) que les plans lisent.

-- +goose Up
alter table pull_requests
    add column draft boolean not null default false,
    add column body text not null default '';

-- +goose Down
alter table pull_requests drop column body, drop column draft;
