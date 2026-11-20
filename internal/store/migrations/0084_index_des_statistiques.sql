-- Les statistiques comptent les releases et les jobs d'un ou plusieurs projets sur une période.
--
-- Les jobs et les exécutions ne sont jamais supprimés : sans ces index, chaque lecture parcourrait
-- tout l'historique. Celui des jobs ne porte que sur les jobs terminés, les seuls comptés.

-- +goose Up
create index if not exists releases_par_projet_et_date on releases (project_id, created_at);
create index if not exists jobs_termines_par_projet on jobs (project_id, finished_at)
    where finished_at is not null;

-- +goose Down
drop index if exists jobs_termines_par_projet;
drop index if exists releases_par_projet_et_date;
