-- Rattache un job à son projet.
--
-- La file était restée aveugle au multi-projet du lot B : un job portait son
-- kind, ses paramètres et parfois sa release, mais rien ne disait pour quel
-- dépôt il s'exécutait. C'était sans conséquence tant qu'une seule clé de
-- déploiement ouvrait tous les dépôts.
--
-- Ce n'est plus le cas. Le lot G donne une clé par projet, et l'ordonnanceur
-- doit savoir laquelle monter au moment de composer le PipelineRun. Sans ce
-- rattachement, il ne peut que retomber sur la clé partagée — c'est-à-dire
-- annuler le bénéfice de la séparation (doc 12 §12.7).
--
-- **Nullable, et délibérément.** Les jobs déjà en base n'ont pas de projet
-- lisible : la plupart sont terminés, et leur en inventer un serait une donnée
-- fausse dans un historique. Un job sans projet monte la clé partagée, ce qui
-- est exactement ce qu'il a fait de son vivant.

-- +goose Up
alter table jobs add column if not exists project_id bigint references projects(id) on delete set null;

-- Le projet d'un job qui vise une release se lit sur elle. Les autres restent
-- nuls : rien ne permet de les rattacher, et le deviner rendrait un historique
-- faux plutôt qu'incomplet.
update jobs j
set project_id = r.project_id
from releases r
where j.release_id = r.id and j.project_id is null;

-- `on delete set null` et non `cascade` : supprimer un projet ne doit pas effacer
-- l'historique de ce qui a été exécuté pour lui. Le job perd son rattachement,
-- pas son existence.
create index if not exists jobs_project_idx on jobs (project_id) where project_id is not null;

-- +goose Down
drop index if exists jobs_project_idx;
alter table jobs drop column if exists project_id;
