-- Déclare la pile technique de chaque projet.
--
-- Elle décide de la **bibliothèque de référence** que le dashboard pose pour lui :
-- les Task et Pipeline propres à un langage — lancer PHPUnit, lire un rapport
-- PHPStan — qui n'ont aucun sens pour un projet écrit autrement
-- (doc 12 §12.6, lot G).
--
-- C'est le critère de propriété de §12.7 : « l'état désiré de cette ressource
-- change-t-il quand quelqu'un agit dans l'interface ? » Oui — un projet déclare
-- sa pile —, donc le dashboard la pose et la réconcilie.
--
-- # Vide est la valeur normale
--
-- Un projet qui apporte ses propres pipelines ne déclare aucune pile, et c'est
-- parfaitement valide : le normatif est le contrat de doc 13, pas la
-- bibliothèque. Le défaut est donc le vide et non `php` — donner d'office une
-- bibliothèque PHP à tout projet déclaré poserait des Task inutiles dans le
-- namespace, et ferait croire qu'elles sont attendues.
--
-- Pas de contrainte d'énumération : les piles embarquées sont celles du binaire,
-- et une valeur inconnue est refusée à la déclaration, avec un message qui les
-- nomme. L'écrire aussi en base obligerait à une migration par bibliothèque
-- ajoutée.

-- +goose Up
alter table projects add column if not exists stack text not null default '';

-- +goose Down
alter table projects drop column if exists stack;
