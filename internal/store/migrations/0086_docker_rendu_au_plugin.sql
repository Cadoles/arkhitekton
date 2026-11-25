-- Les réglages Docker d'un projet quittent le noyau pour le plugin `docker`.
--
-- `dockerfile`, `build_context` et `registry_secret` deviennent les réglages `dockerfile`,
-- `buildContext` et `registrySecret` du plugin pour ce projet. Une valeur par défaut (`.` pour le
-- contexte) n'est pas recopiée : le plugin la donne déjà.
--
-- `image_registry` n'est pas recopié. Rien ne le lisait : tout publiait dans le registre du
-- déploiement. Le recopier ferait publier ailleurs, du jour au lendemain, un projet qui en affichait
-- un autre.
--
-- `targets` et `multi_stage` disparaissent sans être recopiés : le plan de release déclare déjà les
-- images, une étape de construction chacune. Une cible non promue (l'image de développement) le
-- reste : l'étape `docker/build` du plan qui la construit reçoit `promote: false`, et ses artefacts
-- déjà publiés aussi, pour qu'une finale de candidate existante ne la re-tague pas.
--
-- Les variables d'un plan suivent : `{project.dockerfile}` et `{project.build_context}` deviennent
-- `{docker.dockerfile}` et `{docker.buildContext}`.
--
-- Le plugin est obligatoire : une ligne qui l'éteignait pour un projet est rallumée.

-- +goose Up
alter table plan_artifacts add column if not exists promote boolean not null default true;

insert into project_plugins (project_id, plugin_id, enabled, config)
select id, 'docker', true, settings
from (
    select id, jsonb_strip_nulls(jsonb_build_object(
        'dockerfile', nullif(btrim(dockerfile), ''),
        'buildContext', nullif(nullif(btrim(build_context), ''), '.'),
        'registrySecret', nullif(btrim(registry_secret), ''))) as settings
    from projects
) declared
where settings <> '{}'::jsonb
on conflict (project_id, plugin_id) do update
set config = project_plugins.config || excluded.config;

update project_plugins set enabled = true where plugin_id = 'docker' and not enabled;

create temp table unpromoted (project_id bigint, image text) on commit drop;
insert into unpromoted (project_id, image)
select p.id, t.target ->> 'image'
from projects p, jsonb_array_elements(p.targets) as t (target)
where jsonb_typeof(p.targets) = 'array'
  and (t.target ->> 'promote') = 'false'
  and coalesce(t.target ->> 'image', '') <> '';

-- +goose StatementBegin
create or replace function pg_temp.unpromote(plan jsonb, project bigint) returns jsonb language sql as $$
    select case
        when jsonb_typeof(plan -> 'stages') = 'array' then jsonb_set(plan, '{stages}', coalesce((
            select jsonb_agg(
                case
                    when st.stage ->> 'role' = 'build' and jsonb_typeof(st.stage -> 'gestes') = 'array'
                        then jsonb_set(st.stage, '{gestes}', coalesce((
                            select jsonb_agg(
                                case
                                    when g.geste ->> 'kind' = 'pipeline' and g.geste ->> 'ref' = 'docker/build'
                                        and exists (select 1 from unpromoted u
                                                    where u.project_id = project
                                                      and u.image = g.geste -> 'params' ->> 'image-name')
                                        then g.geste || '{"promote": false}'::jsonb
                                    else g.geste
                                end
                                order by g.position)
                            from jsonb_array_elements(st.stage -> 'gestes') with ordinality as g (geste, position)
                        ), '[]'::jsonb))
                    else st.stage
                end
                order by st.position)
            from jsonb_array_elements(plan -> 'stages') with ordinality as st (stage, position)
        ), '[]'::jsonb))
        else plan
    end
$$;
-- +goose StatementEnd

update release_automations set plan = pg_temp.unpromote(plan, project_id)
where project_id in (select project_id from unpromoted);

-- Une référence d'image se lit `registre/projet/nom:tag` : le nom est le dernier segment, sans tag
-- ni digest.
update plan_artifacts a
set promote = false
from plan_runs r, unpromoted u
where a.run_id = r.id
  and r.project_id = u.project_id
  and a.kind = 'image'
  and regexp_replace(a.reference, '^(.*/)?([^/:@]+)([:@].*)?$', '\2') = u.image;

-- Les variables d'un plan suivent les réglages : `{project.dockerfile}` devient `{docker.dockerfile}`.
update release_automations
set plan = replace(replace(plan::text, '{project.dockerfile}', '{docker.dockerfile}'),
                   '{project.build_context}', '{docker.buildContext}')::jsonb
where plan::text like '%{project.%';
update reactions
set plan = replace(replace(plan::text, '{project.dockerfile}', '{docker.dockerfile}'),
                   '{project.build_context}', '{docker.buildContext}')::jsonb
where plan::text like '%{project.%';
update schedules
set plan = replace(replace(plan::text, '{project.dockerfile}', '{docker.dockerfile}'),
                   '{project.build_context}', '{docker.buildContext}')::jsonb
where plan::text like '%{project.%';

alter table projects drop constraint if exists projects_targets_nomment_une_image;
alter table projects drop column if exists dockerfile;
alter table projects drop column if exists build_context;
alter table projects drop column if exists image_registry;
alter table projects drop column if exists registry_secret;
alter table projects drop column if exists multi_stage;
alter table projects drop column if exists targets;

-- +goose Down
alter table projects add column if not exists dockerfile text not null default '';
alter table projects add column if not exists build_context text not null default '.';
alter table projects add column if not exists image_registry text not null default '';
alter table projects add column if not exists registry_secret text not null default '';
alter table projects add column if not exists multi_stage boolean not null default false;
alter table projects add column if not exists targets jsonb not null default '[]'::jsonb;

update projects p
set dockerfile = coalesce(pp.config ->> 'dockerfile', ''),
    build_context = coalesce(nullif(pp.config ->> 'buildContext', ''), '.'),
    registry_secret = coalesce(pp.config ->> 'registrySecret', '')
from project_plugins pp
where pp.project_id = p.id and pp.plugin_id = 'docker';

-- Les cibles se relisent dans les étapes de construction du plan de release, une par image.
update projects p
set targets = declared.targets,
    multi_stage = declared.multi
from (
    select ra.project_id,
           jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
               'stage', coalesce(g.geste -> 'params' ->> 'target', ''),
               'image', g.geste -> 'params' ->> 'image-name',
               'promote', coalesce((g.geste ->> 'promote')::boolean, true)))
             order by st.position, g.position) as targets,
           bool_or(coalesce(g.geste -> 'params' ->> 'target', '') <> '') as multi
    from release_automations ra,
         jsonb_array_elements(ra.plan -> 'stages') with ordinality as st (stage, position),
         jsonb_array_elements(st.stage -> 'gestes') with ordinality as g (geste, position)
    where st.stage ->> 'role' = 'build'
      and g.geste ->> 'kind' = 'pipeline' and g.geste ->> 'ref' = 'docker/build'
      and coalesce(g.geste -> 'params' ->> 'image-name', '') <> ''
    group by ra.project_id
) declared
where declared.project_id = p.id;

alter table projects
  add constraint projects_targets_nomment_une_image check (
    not jsonb_path_exists(targets, '$[*] ? (!exists(@.image) || @.image == "")')
  );

update project_plugins
set config = config - 'dockerfile' - 'buildContext' - 'registrySecret' - 'registry'
where plugin_id = 'docker';

update release_automations
set plan = replace(replace(plan::text, '{docker.dockerfile}', '{project.dockerfile}'),
                   '{docker.buildContext}', '{project.build_context}')::jsonb
where plan::text like '%{docker.%';
update reactions
set plan = replace(replace(plan::text, '{docker.dockerfile}', '{project.dockerfile}'),
                   '{docker.buildContext}', '{project.build_context}')::jsonb
where plan::text like '%{docker.%';
update schedules
set plan = replace(replace(plan::text, '{docker.dockerfile}', '{project.dockerfile}'),
                   '{docker.buildContext}', '{project.build_context}')::jsonb
where plan::text like '%{docker.%';

alter table plan_artifacts drop column if exists promote;
