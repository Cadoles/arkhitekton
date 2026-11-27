-- Trois réglages du projet quittent le noyau.
--
-- `tracker_project` doublait le réglage « Identifiant du projet » du plugin de suivi, qui le lisait
-- en premier : sa valeur y est recopiée quand il ne la porte pas encore. `version_file` nommait le
-- fichier qu'un plugin commitait au lancement d'une release : ce plugin, installé à part, garde
-- désormais ce réglage. `stack` choisissait une bibliothèque de pipelines que les plugins
-- installent désormais eux-mêmes : plus rien ne le lisait.
--
-- Une ligne créée ici est allumée : sans ligne, le projet suit déjà l'instance, et un plugin éteint
-- à l'instance le reste quoi qu'en dise le projet.

-- +goose Up
insert into project_plugins (project_id, plugin_id, enabled, config)
select id, 'redmine', true, jsonb_build_object('project', btrim(tracker_project))
from projects
where btrim(tracker_project) <> ''
on conflict (project_id, plugin_id) do update
set config = project_plugins.config || excluded.config
where coalesce(btrim(project_plugins.config->>'project'), '') = '';

alter table projects drop column if exists tracker_project;
alter table projects drop column if exists version_file;
alter table projects drop column if exists stack;

-- +goose Down
alter table projects add column if not exists stack text not null default '';
alter table projects add column if not exists version_file text not null default '';
alter table projects add column if not exists tracker_project text not null default '';

update projects p
set tracker_project = coalesce(pp.config->>'project', '')
from project_plugins pp
where pp.project_id = p.id and pp.plugin_id = 'redmine';

