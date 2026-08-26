-- Permet à un projet de n'avoir aucun outil de suivi.
--
-- Le dashboard supposait qu'une version vient toujours d'un outil de gestion de
-- projet. `versions` en est la projection — `redmine_id`, `status`, `due_date`,
-- `synced_at` — et `releases.version_id` est `not null` : toute release pend donc
-- d'une ligne qu'un outil externe est censé confirmer.
--
-- Deux projets réels n'en ont pas. `hydra-sql-mse-theme` et
-- `hydra-dispatcher-mse-theme` versionnent par tags Git, sans Redmine ni
-- équivalent. Pour eux, `EnsureVersion` fabriquait une version provisoire à
-- `redmine_id` négatif, en attente d'une adoption qui ne serait jamais venue :
-- l'état d'anomalie devenait l'état normal, et l'écran des versions aurait
-- affiché en permanence des lignes en retard sur un outil inexistant
-- (doc 12 §12.9, lot B).
--
-- Deux changements, et un seul concept nouveau.
--
-- `projects.tracker_project` dit si un projet a un suivi, et lequel. Vide, il n'en
-- a pas. C'est aussi ce que le multi-projet exige : la configuration globale
-- `REDMINE_PROJECT` ne pouvait donner qu'une réponse pour tous.
--
-- `versions.origin` sépare ce que le signe de `redmine_id` encodait à lui seul.
-- Il portait deux états — « vient du suivi » et « provisoire, en attente » — et il
-- en faut un troisième : « propre au dashboard, définitive ». Sans lui, une
-- version d'un projet sans suivi serait indiscernable d'une provisoire, et
-- l'adoption tenterait de la rapprocher de quelque chose qui n'existe pas.

-- +goose Up

alter table projects add column if not exists tracker_project text not null default '';

alter table versions add column if not exists origin text;

-- Les lignes existantes portent leur état dans le signe de `redmine_id`. La
-- traduction est exacte : c'était la seule lecture possible jusqu'ici.
update versions
set origin = case when redmine_id > 0 then 'tracker' else 'pending' end
where origin is null;

alter table versions alter column origin set not null;
alter table versions alter column origin set default 'tracker';

alter table versions add constraint versions_origin_check
  check (origin in ('tracker', 'pending', 'own'));

-- L'adoption ne regarde que les provisoires. L'index le dit, et évite de
-- parcourir les versions d'un projet sans suivi à chaque synchronisation.
create index if not exists versions_pending_idx on versions (project_id)
  where origin = 'pending';

-- +goose Down
drop index if exists versions_pending_idx;
alter table versions drop constraint if exists versions_origin_check;
alter table versions drop column if exists origin;
alter table projects drop column if exists tracker_project;
