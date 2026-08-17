-- +goose Up
-- Validation humaine d'une release dans un environnement.
--
-- La maquette portait `validation[]` sur l'environnement. Ses données étant
-- figées, la distinction ne pouvait pas se voir : une validation porte sur
-- « cette release, dans cet environnement », et non sur l'environnement seul.
-- Attachée à l'environnement, elle survivrait au déploiement suivant et
-- afficherait « validé par le client » pour une version que le client n'a jamais
-- vue. C'est exactement l'erreur qu'un tableau de bord de recette ne peut pas se
-- permettre. D'où la clé composée.
create table release_signoffs (
    id             bigserial primary key,
    release_id     bigint      not null references releases (id) on delete cascade,
    environment_id text        not null references environments (id),

    -- Qui valide, en toutes lettres : « Client », « Recette », « Exploitation ».
    -- Le champ est libre parce que la liste des intervenants dépend du marché et
    -- non du logiciel ; une énumération figée obligerait une migration à chaque
    -- nouvel intervenant.
    role           text        not null,

    -- Même échelle que le reste de l'interface : `pass` validé, `warn` validé
    -- sous réserve, `fail` refusé. `na` n'est pas stocké — l'attente est
    -- l'absence de ligne, pas une ligne qui dit « en attente ». Enregistrer
    -- l'attente obligerait à créer une ligne par intervenant dès la release,
    -- sans savoir qui doit se prononcer.
    status         text        not null check (status in ('pass', 'warn', 'fail')),

    -- La réserve ou le motif de refus. Obligatoire dans les faits pour `warn` et
    -- `fail`, la contrainte étant portée par l'API : un refus sans motif n'aide
    -- personne, mais le rejeter en base rendrait la donnée irréparable si la
    -- règle changeait.
    detail         text,

    decided_by     text        not null,
    decided_at     timestamptz not null default now(),

    -- Un intervenant se prononce une fois par release et par environnement. Se
    -- prononcer à nouveau remplace son avis, ce qui est le comportement attendu
    -- d'un changement d'avis — l'historique reste dans le journal d'audit.
    unique (release_id, environment_id, role)
);

-- L'accès se fait toujours par release, souvent restreint à un environnement.
create index release_signoffs_release_idx on release_signoffs (release_id, environment_id);

-- +goose Down
drop table if exists release_signoffs;
