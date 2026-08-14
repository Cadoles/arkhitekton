-- +goose Up
-- Rattachement d'une promotion au job qui l'exécute, et digest devenu facultatif.
--
-- Les deux changements viennent du même constat : une promotion est **demandée**
-- avant d'être faite. Pousser le tag ne déploie rien — c'est `promote-image` qui
-- re-tague, puis Flux qui constate et décide (B7).
--
-- `job_id` : sans lui, une promotion enregistrée dit qu'on a demandé quelque
-- chose, sans permettre d'atteindre la preuve de ce qui s'est passé. Le lien vers
-- le job donne accès au run, à ses journaux et à son issue.
alter table image_promotions add column job_id bigint references jobs (id) on delete set null;

-- `digest` : il n'est pas connu au moment de la demande. Le re-tag n'a pas encore
-- eu lieu, et l'exiger obligerait soit à interroger le registre en amont — ce qui
-- rendrait la promotion impossible sans identifiants de registre —, soit à
-- inscrire une chaîne vide, qui se confondrait avec un digest inconnu.
alter table image_promotions alter column digest drop not null;

create index image_promotions_job_idx on image_promotions (job_id) where job_id is not null;

-- +goose Down
drop index if exists image_promotions_job_idx;
alter table image_promotions alter column digest set not null;
alter table image_promotions drop column if exists job_id;
