-- +goose Up
-- Nom du PipelineRun décidé pour un job.
--
-- La colonne existe pour rendre impossible un run orphelin. La transition
-- `queued → running` et la création dans le cluster ne peuvent pas être atomiques
-- — base et cluster sont deux systèmes. L'ordre retenu est donc : décider le nom,
-- le valider en base avec la transition, **puis** créer dans le cluster
-- (doc 06 §6.3).
--
-- Si la création échoue, le job repasse `queued` et le nom est réattribué. Si
-- elle réussit mais que le worker meurt avant d'en prendre acte, la
-- réconciliation retrouve le run par ce nom au lieu de le laisser tourner sans
-- que personne ne le rattache. L'ordre inverse — créer puis marquer — produirait
-- des runs invisibles du dashboard.
alter table jobs add column run_name text;

-- Un nom de PipelineRun ne peut désigner qu'un job : le contraire signifierait
-- que deux jobs se croient propriétaires du même run.
create unique index jobs_run_name_idx on jobs (run_name) where run_name is not null;

-- +goose Down
drop index if exists jobs_run_name_idx;
alter table jobs drop column if exists run_name;
