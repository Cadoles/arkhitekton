-- Un identifiant unique par projet, et le nom de ses Secrets enregistré.
--
-- Les Secrets d'un projet étaient nommés d'après son slug. Or un slug se réutilise : un projet
-- supprimé puis recréé, ou une base réinitialisée, retombe sur le Secret de l'ancien, que le
-- dashboard refuse d'adopter faute de pouvoir le lire ; un projet renommé ne trouve plus sa clé.
-- Les nouveaux Secrets sont nommés d'après l'identifiant, qui ne change jamais, et le nom de la
-- clé est enregistré à sa création : c'est lui qu'on relit, plus le slug.
--
-- Les clés existantes gardent leur nom, recopié ici : le dashboard ne peut pas lire un Secret pour
-- le recopier sous un autre nom, et en changer voudrait dire redéclarer chaque clé sur sa forge.
-- Elles prennent un nom tiré de l'identifiant à leur prochaine rotation.
--
-- La raison d'un échec à poser la clé est gardée pour l'écran du projet : elle ne se lisait que
-- dans les journaux du worker. Et les Secrets d'un projet supprimé sont inscrits pour que le
-- worker, seul à avoir un jeton de cluster, les supprime.

-- +goose Up
alter table projects add column if not exists uid uuid not null default gen_random_uuid();
create unique index if not exists projects_uid_idx on projects (uid);

alter table projects add column if not exists deploy_key_secret text not null default '';
update projects set deploy_key_secret = 'arkhitekton-project-' || slug || '-git'
 where deploy_key_public <> '' and deploy_key_secret = '';
alter table projects add constraint projects_deploy_key_secret_coherent
    check ((deploy_key_public = '') = (deploy_key_secret = ''));

alter table projects add column if not exists deploy_key_error text not null default '';

create table if not exists project_secrets_to_delete (
    name         text primary key,
    requested_at timestamptz not null default now()
);

-- +goose Down
drop table if exists project_secrets_to_delete;
alter table projects drop column if exists deploy_key_error;
alter table projects drop constraint if exists projects_deploy_key_secret_coherent;
alter table projects drop column if exists deploy_key_secret;
drop index if exists projects_uid_idx;
alter table projects drop column if exists uid;
