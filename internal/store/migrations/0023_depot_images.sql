-- +goose Up
-- Le dépôt d'images d'un projet.

-- Un projet disait quelles étapes construire — `web`, `cli` — et sous quel nom
-- court les publier — `portal`, `portal-cli`. Il ne disait pas **où** : le
-- préfixe de registre venait de la configuration, la même pour tous, et un projet
-- qui publie ailleurs n'était pas décrivable.
--
-- Il manquait aussi le cas le plus simple. Un Dockerfile sans cible ne portait
-- aucun nom d'image : la liste des étapes était masquée, et rien ne remplaçait ce
-- qu'elle aurait dit. Un projet à image unique était donc déclarable sans qu'on
-- sache ce qu'il produit.
--
-- Le préfixe est porté par le projet et non répété par étape : les trois images
-- d'une application vont au même endroit, et les répéter ferait diverger la
-- troisième au premier changement de registre.
alter table projects
    add column image_registry text not null default '';

comment on column projects.image_registry is
    'Registre et projet où publier, sans le nom d''image : reg.cadoles.com/cnous-mse.';

-- Le cas de l'image unique passe par la même liste que les autres, avec une étape
-- sans nom de cible. Un second champ dirait la même chose d'une seconde façon, et
-- il faudrait alors savoir lequel fait foi quand les deux sont remplis.
comment on column projects.targets is
    'Étapes constructibles : [{"stage": "web", "image": "portal"}]. Une seule entrée sans `stage` pour un Dockerfile à image unique.';

-- +goose Down
alter table projects drop column if exists image_registry;
