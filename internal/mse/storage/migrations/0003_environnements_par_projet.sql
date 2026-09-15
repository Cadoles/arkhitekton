-- Les environnements appartiennent à un projet (doc 16 §16.18, niveau 2).
--
-- `environments` était une table d'instance, clé `id`. Deux projets qui déploient
-- vers leurs propres environnements pourraient avoir chacun un `qualif` : la clé
-- devient `(project_id, id)`, et les trois tables qui la référencent suivent.
--
-- # Le rattachement de l'existant
--
-- Les environnements vont au projet `mse-test-project`, seul à les employer ; à
-- défaut, au premier projet déclaré. Une promotion et une validation prennent le
-- projet de **leur release**. Si une release d'un autre projet visait un
-- environnement, celui-ci est dupliqué pour ce projet : la donnée existait dans le
-- modèle d'instance, elle ne doit pas être perdue en passant au modèle par projet.

-- +goose Up
alter table image_promotions drop constraint if exists image_promotions_environment_id_fkey;
alter table release_signoffs drop constraint if exists release_signoffs_environment_id_fkey;
alter table environment_observations drop constraint if exists environment_observations_environment_id_fkey;
alter table environment_observations drop constraint if exists environment_observations_pkey;
alter table environments drop constraint if exists environments_pkey;
alter table environments drop constraint if exists environments_rank_key;

alter table environments add column project_id bigint references projects (id) on delete cascade;
update environments set project_id = coalesce(
    (select id from projects where slug = 'mse-test-project'),
    (select min(id) from projects));

alter table image_promotions add column project_id bigint;
update image_promotions p set project_id = r.project_id from releases r where r.id = p.release_id;

alter table release_signoffs add column project_id bigint;
update release_signoffs s set project_id = r.project_id from releases r where r.id = s.release_id;

alter table environment_observations add column project_id bigint;
update environment_observations o set project_id = e.project_id from environments e where e.id = o.environment_id;

insert into environments (project_id, id, label, aliases, rank, namespace, security_policy,
                          observable, visible, deployer_settings)
select distinct on (u.project_id, e.id)
       u.project_id, e.id, e.label, e.aliases, e.rank, e.namespace, e.security_policy,
       e.observable, e.visible, e.deployer_settings
from (select project_id, environment_id from image_promotions
      union
      select project_id, environment_id from release_signoffs) u
join environments e on e.id = u.environment_id
where u.project_id is not null
  and not exists (select 1 from environments present
                  where present.project_id = u.project_id and present.id = e.id);

-- Sans aucun projet, un environnement ne peut appartenir à personne.
delete from environment_observations where project_id is null;
delete from environments where project_id is null;

alter table environments alter column project_id set not null;
alter table image_promotions alter column project_id set not null;
alter table release_signoffs alter column project_id set not null;
alter table environment_observations alter column project_id set not null;

alter table environments add constraint environments_pkey primary key (project_id, id);
alter table environments add constraint environments_project_id_rank_key unique (project_id, rank);
alter table environment_observations add constraint environment_observations_pkey
    primary key (project_id, environment_id);

alter table image_promotions add constraint image_promotions_environment_fkey
    foreign key (project_id, environment_id) references environments (project_id, id);
alter table release_signoffs add constraint release_signoffs_environment_fkey
    foreign key (project_id, environment_id) references environments (project_id, id);
alter table environment_observations add constraint environment_observations_environment_fkey
    foreign key (project_id, environment_id) references environments (project_id, id);

-- +goose Down
-- Retour au modèle d'instance. Un identifiant partagé par plusieurs projets n'y
-- garde qu'une ligne, celle du projet le plus ancien.
alter table image_promotions drop constraint if exists image_promotions_environment_fkey;
alter table release_signoffs drop constraint if exists release_signoffs_environment_fkey;
alter table environment_observations drop constraint if exists environment_observations_environment_fkey;
alter table environment_observations drop constraint if exists environment_observations_pkey;
alter table environments drop constraint if exists environments_pkey;
alter table environments drop constraint if exists environments_project_id_rank_key;

delete from environment_observations o
where exists (select 1 from environments e where e.id = o.environment_id and e.project_id < o.project_id);
delete from environments e using environments older
where older.id = e.id and older.project_id < e.project_id;

alter table environment_observations drop column project_id;
alter table release_signoffs drop column project_id;
alter table image_promotions drop column project_id;
alter table environments drop column project_id;

alter table environments add constraint environments_pkey primary key (id);
alter table environments add constraint environments_rank_key unique (rank);
alter table environment_observations add constraint environment_observations_pkey primary key (environment_id);
alter table image_promotions add constraint image_promotions_environment_id_fkey
    foreign key (environment_id) references environments (id);
alter table release_signoffs add constraint release_signoffs_environment_id_fkey
    foreign key (environment_id) references environments (id);
alter table environment_observations add constraint environment_observations_environment_id_fkey
    foreign key (environment_id) references environments (id);
