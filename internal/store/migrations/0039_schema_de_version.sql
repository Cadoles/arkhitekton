-- Déclare le schéma de version de chaque projet.
--
-- Le SemVer n'était pas une brique de ce projet, c'était une **hypothèse
-- ambiante** : la forme `MAJEUR.MINEUR.CORRECTIF[-prerelease]` était codée dans
-- `internal/release`, et tout ce qui s'y appuie la supposait acquise sans jamais
-- la nommer (doc 12 §12.4, lot B).
--
-- Elle n'est pas universelle. Le voisinage immédiat en donne l'exemple : les tags
-- de `mse-kustom` s'écrivent `2026.7.27-k8sunstable.1651.91a39c48b`, du
-- calendaire déguisé. D'autres projets numérotent leurs constructions.
--
-- `semver` par défaut, et non une valeur nulle : c'est ce que tous les projets
-- déclarés emploient, et exiger une déclaration explicite aurait rendu invalides
-- des lignes que personne n'a écrites.
--
-- La contrainte énumère plutôt que de laisser libre. Un schéma mal orthographié
-- ferait retomber le projet sur un comportement par défaut sans rien dire, et
-- c'est précisément la classe de panne silencieuse que ce lot supprime ailleurs.

-- +goose Up
alter table projects add column if not exists version_scheme text not null default 'semver';

alter table projects add constraint projects_version_scheme_known
  check (version_scheme in ('semver', 'opaque'));

-- +goose Down
alter table projects drop constraint if exists projects_version_scheme_known;
alter table projects drop column if exists version_scheme;
