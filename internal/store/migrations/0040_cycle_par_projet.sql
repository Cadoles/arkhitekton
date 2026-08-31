-- Déclare le cycle de release de chaque projet.
--
-- `check (kind in ('pre','final','patch'))` — trois noms pour **deux**
-- comportements, et l'impossibilité d'en déclarer un quatrième. Un projet en
-- `alpha` / `beta` / `rc` / `ga` n'entrait dans aucune des trois cases ; un
-- projet qui ne distingue rien devait en choisir une arbitrairement
-- (doc 12 §12.4, lot B).
--
-- Le tableau des traits montrait le défaut : `pre` et `patch` ne diffèrent que
-- par la forme du tag et la permission. Leur comportement est le même —
-- construire depuis du code.
--
-- # Ce que le défaut préserve
--
-- Chaque projet reçoit le cycle historique, traits pour traits. Un projet
-- déclaré avant cette bascule continue donc sans que personne n'ait rien à
-- écrire, et les releases existantes gardent une nature déclarée.
--
-- # Pourquoi la contrainte disparaît
--
-- `releases.kind` cesse d'être une énumération : sa valeur est vérifiée contre le
-- cycle **du projet**, ce qu'un `check` de colonne ne sait pas exprimer. La
-- garder interdirait la nature `ga` d'un projet qui la déclare, sans qu'aucun
-- écran ne dise pourquoi.

-- +goose Up
-- Le **défaut de la colonne** est le cycle historique, et non un tableau vide.
--
-- Un défaut qui viole sa propre contrainte est un piège : toute insertion qui
-- n'énumère pas la colonne — un test, une reprise, un script d'exploitation —
-- échouerait sur une contrainte que l'auteur n'a pas vue passer. Le défaut doit
-- être une valeur **utilisable**.
alter table projects add column if not exists release_kinds jsonb not null default '[
  {"id":"pre","label":"Pré-release","mode":"build","tag":"prerelease-required","permission":"release:create"},
  {"id":"final","label":"Finale","mode":"retag","source":"pre","tag":"prerelease-forbidden","permission":"release:create:final"},
  {"id":"patch","label":"Patch","mode":"build","tag":"prerelease-forbidden","permission":"release:create:final"}
]'::jsonb;

-- Les lignes existantes reçoivent le même cycle : `add column` applique le défaut
-- aux lignes déjà présentes, mais l'écrire ici rend le propos lisible sans
-- connaître cette subtilité de PostgreSQL.
update projects set release_kinds = '[
  {"id":"pre","label":"Pré-release","mode":"build","tag":"prerelease-required","permission":"release:create"},
  {"id":"final","label":"Finale","mode":"retag","source":"pre","tag":"prerelease-forbidden","permission":"release:create:final"},
  {"id":"patch","label":"Patch","mode":"build","tag":"prerelease-forbidden","permission":"release:create:final"}
]'::jsonb
where jsonb_array_length(release_kinds) = 0;

-- Un cycle vide ne décrit aucune release créable : le projet serait déclaré et
-- inutilisable, sans que rien ne l'explique.
alter table projects add constraint projects_release_kinds_non_vide
  check (jsonb_array_length(release_kinds) > 0);

-- La nature d'une release est désormais vérifiée contre le cycle de son projet.
alter table releases drop constraint if exists releases_kind_check;

-- +goose Down
alter table releases add constraint releases_kind_check
  check (kind in ('pre', 'final', 'patch'));
alter table projects drop constraint if exists projects_release_kinds_non_vide;
alter table projects drop column if exists release_kinds;
