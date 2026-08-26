-- Permet à une étape de nommer son propre Dockerfile.
--
-- Le modèle ne savait dire qu'une chose : « telle **étape** du Dockerfile du
-- projet produit telle image ». C'est la forme du dépôt de MSE — un Dockerfile
-- multi-étapes, une image par cible — et elle a été prise pour une vérité
-- générale.
--
-- Deux projets réels disent le contraire. `hydra-dispatcher-mse-theme` porte
-- `Dockerfile.dev` et `Dockerfile.prod` à sa racine : deux fichiers, deux images,
-- aucune étape. `hydra-sql-mse-theme` n'en porte qu'un, sans étape non plus.
--
-- La colonne n'est pas ajoutée : `targets` est du JSON, et un champ y apparaît
-- sans migration. Ce fichier existe pour la **contrainte** — une étape doit
-- désigner une image, sans quoi elle ne publie rien — et pour que le changement
-- de modèle laisse une trace datée (doc 12 §12.9, lot B).

-- +goose Up

-- +goose StatementBegin
do $$
declare
  fautives int;
begin
  select count(*) into fautives
  from projects p, jsonb_array_elements(p.targets) as t
  where coalesce(trim(t ->> 'image'), '') = '';

  if fautives > 0 then
    raise exception '% étape(s) sans nom d''image: elles ne publieraient rien', fautives;
  end if;
end $$;
-- +goose StatementEnd

-- Exprimée en jsonpath et non par une sous-requête : `check` ne les accepte pas,
-- et `jsonb_path_exists` est un appel de fonction, donc admis. Elle attrape les
-- deux formes fautives — clé absente et valeur vide.
alter table projects
  add constraint projects_targets_nomment_une_image check (
    not jsonb_path_exists(targets, '$[*] ? (!exists(@.image) || @.image == "")')
  );

-- +goose Down
alter table projects drop constraint if exists projects_targets_nomment_une_image;
