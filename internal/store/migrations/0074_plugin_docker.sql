-- Les pipelines d'image quittent le plugin `core` pour le plugin `docker`.
--
-- Comme en 0055, tout ce qui nomme un pipeline bouge d'un coup : les noms se comparent par égalité,
-- et en oublier un éteindrait une vérification ou lèverait un plafond sans rien dire. S'y ajoutent
-- ce qui est né depuis (planifications, liaisons enregistrées, capacité des jobs) et les réglages
-- du plugin lui-même.

-- Remplace la fonction de 0055 : dans une même connexion, le schéma temporaire la garde encore.

-- +goose Up
create temp table kind_map (old text primary key, new text not null) on commit drop;
insert into kind_map (old, new) values
    ('core/build', 'docker/build'),
    ('core/retag', 'docker/retag');

-- +goose StatementBegin
create or replace function pg_temp.requalify(plan jsonb) returns jsonb language sql as $$
    select case
        when jsonb_typeof(plan -> 'stages') = 'array' then jsonb_set(plan, '{stages}', coalesce((
            select jsonb_agg(
                case
                    when jsonb_typeof(st.stage -> 'gestes') = 'array' then jsonb_set(st.stage, '{gestes}', coalesce((
                        select jsonb_agg(
                            case
                                when g.geste ->> 'kind' = 'pipeline' and m.new is not null
                                    then g.geste || jsonb_build_object('ref', m.new)
                                else g.geste
                            end
                            order by g.position)
                        from jsonb_array_elements(st.stage -> 'gestes') with ordinality as g (geste, position)
                        left join kind_map m on m.old = g.geste ->> 'ref'
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

update jobs j set kind = m.new from kind_map m where j.kind = m.old;
update jobs j set capability = m.new from kind_map m where j.capability = m.old;
update project_pipelines p set kind = m.new from kind_map m where p.kind = m.old;
update queue_limits q set kind = m.new from kind_map m where q.kind = m.old;
update capability_bindings c set ref = m.new from kind_map m where c.ref = m.old;
update plan_steps s set ref = m.new from kind_map m where s.kind = 'pipeline' and s.ref = m.old;
update release_automations set plan = pg_temp.requalify(plan);
update reactions set plan = pg_temp.requalify(plan);
update schedules set plan = pg_temp.requalify(plan);
update plan_runs set plan = pg_temp.requalify(plan);
update plugin_settings set plugin_id = 'docker' where plugin_id = 'core';
update project_plugins set plugin_id = 'docker' where plugin_id = 'core';

-- +goose Down
create temp table kind_map (old text primary key, new text not null) on commit drop;
insert into kind_map (old, new) values
    ('docker/build', 'core/build'),
    ('docker/retag', 'core/retag');

-- +goose StatementBegin
create or replace function pg_temp.requalify(plan jsonb) returns jsonb language sql as $$
    select case
        when jsonb_typeof(plan -> 'stages') = 'array' then jsonb_set(plan, '{stages}', coalesce((
            select jsonb_agg(
                case
                    when jsonb_typeof(st.stage -> 'gestes') = 'array' then jsonb_set(st.stage, '{gestes}', coalesce((
                        select jsonb_agg(
                            case
                                when g.geste ->> 'kind' = 'pipeline' and m.new is not null
                                    then g.geste || jsonb_build_object('ref', m.new)
                                else g.geste
                            end
                            order by g.position)
                        from jsonb_array_elements(st.stage -> 'gestes') with ordinality as g (geste, position)
                        left join kind_map m on m.old = g.geste ->> 'ref'
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

update jobs j set kind = m.new from kind_map m where j.kind = m.old;
update jobs j set capability = m.new from kind_map m where j.capability = m.old;
update project_pipelines p set kind = m.new from kind_map m where p.kind = m.old;
update queue_limits q set kind = m.new from kind_map m where q.kind = m.old;
update capability_bindings c set ref = m.new from kind_map m where c.ref = m.old;
update plan_steps s set ref = m.new from kind_map m where s.kind = 'pipeline' and s.ref = m.old;
update release_automations set plan = pg_temp.requalify(plan);
update reactions set plan = pg_temp.requalify(plan);
update schedules set plan = pg_temp.requalify(plan);
update plan_runs set plan = pg_temp.requalify(plan);
update plugin_settings set plugin_id = 'core' where plugin_id = 'docker';
update project_plugins set plugin_id = 'core' where plugin_id = 'docker';
