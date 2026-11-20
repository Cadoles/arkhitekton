-- Le projet d'une action du journal d'audit.
--
-- Une action nomme sa cible, un tag le plus souvent, que plusieurs projets peuvent porter : sans son
-- projet, le journal ne se filtre pas par projet. Rempli pour les actions faites sur une route de
-- projet ; les actions d'instance (rôles, plugins) et l'historique restent sans projet.

-- +goose Up
alter table audit_log add column project_id bigint references projects (id) on delete set null;

create index audit_log_project_idx on audit_log (project_id, at desc) where project_id is not null;

-- +goose Down
drop index if exists audit_log_project_idx;
alter table audit_log drop column if exists project_id;
