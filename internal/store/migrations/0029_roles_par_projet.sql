-- Permet qu'un rôle vaille pour un projet plutôt que pour toute l'instance.
--
-- `user_roles` avait `subject` pour clé primaire : un rôle, et un seul, valable
-- partout. Tant qu'un seul projet existait, la question ne se posait pas. Trois
-- sont déclarés, dont deux thèmes qui n'ont rien à voir avec l'application : y
-- donner `releaser` à quelqu'un parce qu'il l'est sur MSE est une autorisation
-- que personne n'a demandée (doc 12 §12.9, lot B).
--
-- **`admin` reste global**, et la contrainte l'impose. Il gouverne les comptes,
-- les dépôts déclarés, la synchronisation et les plafonds de la file — rien de
-- tout cela n'appartient à un projet, et un « administrateur du projet X »
-- n'aurait aucun écran à administrer.
--
-- Un `project_id` nul vaut **partout**. C'est ce que deviennent les attributions
-- existantes : elles ont été accordées quand la question ne se posait pas, et les
-- restreindre au passage retirerait des droits sans que personne ne l'ait décidé.

-- +goose Up

alter table user_roles add column if not exists project_id bigint references projects(id) on delete cascade;

-- La clé primaire portait `subject` seul. Deux index partiels la remplacent, et
-- ils disent la règle plus précisément qu'une clé composite ne le ferait : au
-- plus un rôle global, au plus un rôle par projet. Une clé sur
-- `(subject, project_id)` laisserait passer plusieurs globaux, `null` n'étant
-- jamais égal à `null`.
alter table user_roles drop constraint if exists user_roles_pkey;

create unique index if not exists user_roles_global_key
  on user_roles (subject) where project_id is null;
create unique index if not exists user_roles_project_key
  on user_roles (subject, project_id) where project_id is not null;

-- Lire les rôles d'un sujet est l'opération de chaque requête authentifiée.
create index if not exists user_roles_subject_idx on user_roles (subject);

alter table user_roles add constraint user_roles_admin_global
  check (role <> 'admin' or project_id is null);

-- +goose Down
alter table user_roles drop constraint if exists user_roles_admin_global;
drop index if exists user_roles_subject_idx;
drop index if exists user_roles_project_key;
drop index if exists user_roles_global_key;
delete from user_roles where project_id is not null;
alter table user_roles drop column if exists project_id;
alter table user_roles add primary key (subject);
