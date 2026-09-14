-- Flux et PortalData deviennent un seul plugin, `mse` (doc 16).
--
-- Le réglage de Flux est repris tel quel : son activation et son namespace. Un
-- `flux` éteint à la main donne un `mse` éteint — une décision humaine ne se perd
-- pas dans un renommage.
--
-- PortalData n'avait aucun réglage, et son activation par projet disparaît : le
-- plugin MSE vaut pour toute l'instance.

-- +goose Up
insert into plugin_settings (plugin_id, enabled, config, updated_at, updated_by)
select 'mse', enabled, config, updated_at, updated_by
from plugin_settings where plugin_id = 'flux'
on conflict (plugin_id) do nothing;

delete from plugin_settings where plugin_id in ('flux', 'portaldata');
delete from project_plugins where plugin_id in ('flux', 'portaldata', 'mse');

-- +goose Down
insert into plugin_settings (plugin_id, enabled, config, updated_at, updated_by)
select 'flux', enabled, config, updated_at, updated_by
from plugin_settings where plugin_id = 'mse'
on conflict (plugin_id) do nothing;

insert into plugin_settings (plugin_id, enabled)
select 'portaldata', enabled from plugin_settings where plugin_id = 'mse'
on conflict (plugin_id) do nothing;

delete from plugin_settings where plugin_id = 'mse';
