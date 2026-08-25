-- Renseigne, sur les étapes déjà déclarées, si un re-tag les propage.
--
-- La distinction existait en Go, dans une liste `Promotable` qui énumérait
-- `portal` et `portal-cli` en excluant `portal-dev`. La table décrit désormais ce
-- que chaque dépôt construit ; elle doit donc aussi dire ce qu'un re-tag propage,
-- sinon le dashboard ne peut pas se passer de la liste (doc 12 §12.9).
--
-- Les lignes existantes sont muettes sur ce point, et une entrée muette est lue
-- comme promue. Sans cette migration, l'image de développement partirait en
-- préproduction au premier re-tag : elle serait annoncée comme livrable alors
-- qu'aucun environnement du flux ne la déploie.
--
-- La règle nomme la cible `dev`, qui est du vocabulaire propre au Dockerfile de
-- `mse`. C'est assumé : une migration de données porte sur les données qui
-- existent, et celles-ci sont les siennes. Ce qui suit n'est pas une règle du
-- produit — le champ, lui, est libre, et rien n'oblige un autre déploiement à
-- appeler `dev` ce qu'il ne promeut pas.

-- +goose Up
update projects
set targets = (
      select jsonb_agg(
               target || jsonb_build_object('promote', target ->> 'stage' is distinct from 'dev')
               order by ordinality)
      from jsonb_array_elements(targets) with ordinality as elements(target, ordinality)
    ),
    updated_at = now()
where jsonb_typeof(targets) = 'array'
  and jsonb_array_length(targets) > 0
  -- Idempotent : une étape qui porte déjà le drapeau a été décidée depuis
  -- l'interface, et la réécrire effacerait ce choix.
  and exists (
        select 1 from jsonb_array_elements(targets) as target
        where not (target ? 'promote'));

-- +goose Down
update projects
set targets = (
      select jsonb_agg(target - 'promote' order by ordinality)
      from jsonb_array_elements(targets) with ordinality as elements(target, ordinality)
    ),
    updated_at = now()
where jsonb_typeof(targets) = 'array'
  and jsonb_array_length(targets) > 0;
