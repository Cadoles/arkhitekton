-- N'emploie la clé d'un projet qu'une fois qu'elle ouvre vraiment son dépôt.
--
-- La pose d'une clé et son **usage** sont deux instants distincts, séparés par
-- un geste humain que rien n'automatise : coller la moitié publique dans les
-- réglages du dépôt, côté forge. Entre les deux, la clé existe, elle est montée,
-- et elle n'ouvre rien.
--
-- Sans cette colonne, la migration 0034 basculait d'un coup tous les projets
-- déclarés sur une clé que la forge ignorait : le prochain build aurait échoué
-- au clone, sur un dépôt qui fonctionnait la veille, avec un message qui désigne
-- le dépôt et non la clé. Exactement le mode de défaillance que le lot G existe
-- pour supprimer, reproduit par sa propre mise en service.
--
-- Défaut à faux, y compris pour les projets qui recevront leur clé dans la
-- minute : c'est à celui qui l'a collée dans la forge de dire qu'elle y est.

-- +goose Up
alter table projects add column if not exists deploy_key_active boolean not null default false;

-- Une clé active suppose une clé posée.
alter table projects add constraint projects_deploy_key_active_coherent
  check (not deploy_key_active or deploy_key_public <> '');

-- +goose Down
alter table projects drop constraint if exists projects_deploy_key_active_coherent;
alter table projects drop column if exists deploy_key_active;
