-- +goose Up
-- Identité de cette base, pour distinguer deux générations de données.

-- Les noms de PipelineRun se composent de l'identifiant du job : `mse-build-12`.
-- Ce choix est délibéré — deux essais du même job donnent le même nom, et la
-- seconde création se heurte à un conflit que l'ordonnanceur lit comme « c'était
-- déjà fait » plutôt que de créer un doublon.
--
-- Mais les identifiants repartent à 1 sur une base neuve, alors que les
-- PipelineRun, eux, survivent dans le cluster. Chaque nouveau job s'accrochait
-- donc à un run existant : le conflit était lu comme un succès, aucun run n'était
-- créé, et l'interface affichait indéfiniment l'échec d'une exécution vieille de
-- plusieurs heures. Constaté après une remise à zéro accidentelle.
--
-- Cette table porte un identifiant tiré une fois, à la création de la base. Il
-- entre dans le nom des runs, qui redeviennent uniques d'une génération à l'autre
-- sans cesser d'être déterministes pour un job donné.
create table instance (
    -- Colonne verrou : la contrainte garantit une seule ligne, et rend donc
    -- l'identifiant unique par construction plutôt que par convention.
    lock_id    boolean primary key default true check (lock_id),
    id         text    not null,
    created_at timestamptz not null default now()
);

-- Six caractères hexadécimaux : assez pour que deux bases voisines ne
-- collisionnent pas, assez court pour qu'un nom de run reste lisible dans
-- `kubectl`, et déjà conforme à ce que Kubernetes accepte dans un nom.
insert into instance (id)
values (lower(substr(md5(random()::text || clock_timestamp()::text), 1, 6)));

comment on table instance is
    'Identité de cette base. Entre dans les noms de PipelineRun pour qu''une remise à zéro ne produise pas d''homonymes.';

-- +goose Down
drop table if exists instance;
