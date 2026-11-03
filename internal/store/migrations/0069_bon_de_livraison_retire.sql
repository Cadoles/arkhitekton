-- Le bon de livraison quitte le cœur : il appartient au plugin MSE, qui le produit et le garde
-- dans ses propres tables.
--
-- Les bons produits jusqu'ici l'étaient sous une mise en page provisoire, marquée comme telle sur
-- chaque page, et ne pouvaient pas être transmis : ils ne sont pas repris. Leurs objets restent
-- dans le stockage, sans ligne qui les nomme.
--
-- Les réglages du plugin `bon-de-livraison` ne portaient qu'un interrupteur. Gardés, ils
-- nommeraient un plugin que plus aucune version ne connaît, et l'export de la configuration les
-- écrirait.

-- +goose Up
drop table if exists artifacts;
delete from project_plugins where plugin_id = 'bon-de-livraison';
delete from plugin_settings where plugin_id = 'bon-de-livraison';

-- +goose Down
create table artifacts (
    id           bigserial primary key,
    release_id   bigint references releases (id) on delete cascade,
    kind         text not null,
    format       text not null,
    object       text not null,
    size_bytes   bigint,
    generated_by text not null,
    generated_at timestamptz not null default now()
);
