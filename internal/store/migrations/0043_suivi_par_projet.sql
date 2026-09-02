-- Rend le suivi propre à chaque projet.
--
-- Deux tables valaient pour toute l'instance alors que ce qu'elles portent
-- appartient à un projet : le **curseur** de synchronisation et la **projection**
-- des statuts. Elles ne se traitent pas de la même façon, et la différence est le
-- sujet de cette migration.
--
-- # Le curseur : par projet, sans exception
--
-- Une seule ligne `sync_state` de source `redmine`, avançant à chaque passe. Avec
-- un projet suivi, correct. Avec deux, le curseur avance sur le premier lu et le
-- second reprend depuis un instant où **il** n'a rien lu : son historique est
-- sauté, définitivement, sans qu'aucun message ne le dise.
--
-- Un curseur qui ne dit pas ce qu'il a lu n'a aucune valeur : la colonne est donc
-- obligatoire, et les lignes existantes sont recopiées sur chaque projet suivi.
-- Ce n'est pas une approximation — avant cette migration, ce curseur valait déjà
-- pour tout le monde.
--
-- # La projection : un défaut d'instance, et des écarts par projet
--
-- Ici, effacer aurait été une faute. La migration 0012 pose une projection
-- d'amorçage — trente et un statuts, tirés du workflow réel — et un projet neuf
-- doit continuer d'en hériter. Une projection strictement par projet aurait fait
-- naître chaque projet sans aucune règle, et son taux de complétion aurait compté
-- toutes ses demandes comme non commencées.
--
-- `project_id` reste donc **nullable**, et le nul veut dire « défaut de
-- l'instance ». La lecture superpose : ce que le projet déclare l'emporte, le
-- reste vient du défaut. C'est le même partage que pour les plugins, pour la même
-- raison — le global porte les défauts, le projet porte ses écarts.
--
-- « En revue » ne veut pas dire la même chose chez deux clients : sans cette
-- séparation, un statut déclaré pour l'un déplacerait le taux de complétion
-- affiché à l'autre, un chiffre montré au client changé par une décision prise
-- ailleurs.

-- +goose Up

alter table sync_state add column if not exists project_id bigint
    references projects (id) on delete cascade;

alter table issue_status_mapping add column if not exists project_id bigint
    references projects (id) on delete cascade;

-- Le curseur, recopié sur chaque projet suivi. Le produit cartésien est voulu :
-- c'est la traduction littérale de « cette valeur valait pour tous ».
insert into sync_state (source, cursor, last_run, last_ok, last_error, updated_at, project_id)
select s.source, s.cursor, s.last_run, s.last_ok, s.last_error, s.updated_at, p.id
from sync_state s
cross join projects p
where s.project_id is null and btrim(p.tracker_project) <> '';

delete from sync_state where project_id is null;

alter table sync_state drop constraint if exists sync_state_pkey;
alter table sync_state alter column project_id set not null;
alter table sync_state add primary key (source, project_id);

-- La projection : les lignes existantes **restent** telles quelles, sans projet.
-- Elles deviennent le défaut de l'instance, celui dont hérite tout projet qui n'a
-- rien déclaré. Aucune donnée n'est déplacée ni perdue.
alter table issue_status_mapping drop constraint if exists issue_status_mapping_pkey;

-- Deux unicités partielles plutôt qu'une clé primaire : celle-ci n'admettrait pas
-- le nul, or c'est précisément le nul qui porte le défaut.
create unique index if not exists issue_status_mapping_par_projet
    on issue_status_mapping (project_id, status_name) where project_id is not null;

create unique index if not exists issue_status_mapping_defaut
    on issue_status_mapping (status_name) where project_id is null;

-- +goose Down
drop index if exists issue_status_mapping_defaut;
drop index if exists issue_status_mapping_par_projet;

-- Le retour perd la distinction : les écarts d'un projet s'effacent devant le
-- défaut, qui est ce que la table portait avant. Une descente de migration n'a
-- pas d'autre choix — l'information par projet n'a pas de place où aller.
delete from issue_status_mapping where project_id is not null;
alter table issue_status_mapping drop column if exists project_id;
alter table issue_status_mapping add primary key (status_name);

alter table sync_state drop constraint if exists sync_state_pkey;
delete from sync_state s
using sync_state autre
where s.source = autre.source and s.project_id > autre.project_id;
alter table sync_state drop column if exists project_id;
alter table sync_state add primary key (source);
