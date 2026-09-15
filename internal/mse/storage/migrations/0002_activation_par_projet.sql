-- Le plugin MSE se choisit par projet (doc 16 §16.18).
--
-- Un projet sans décision hérite de l'instance : sans cette migration, les cinq
-- projets déclarés garderaient le plugin. Seul `mse-test-project` relève du
-- portail MSE ; les autres le reçoivent éteint, par une décision explicite qu'un
-- administrateur peut revoir depuis la page du projet.
--
-- Nommés par leur slug : sur une base où ils n'existent pas, rien n'est écrit.
-- `updated_by` marque les lignes posées ici, pour que le retour arrière ne retire
-- que celles-là.

-- +goose Up
insert into project_plugins (project_id, plugin_id, enabled, updated_by)
select id, 'mse', false, 'migration mse 0002'
from projects
where slug in ('mse-kustom-test-project', 'mse-tekton', 'hydra-sql-mse-theme', 'hydra-dispatcher-mse-theme')
on conflict (project_id, plugin_id) do nothing;

-- +goose Down
delete from project_plugins where plugin_id = 'mse' and updated_by = 'migration mse 0002';
