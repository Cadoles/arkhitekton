-- Pipelines are named by the capability they instantiate: `build` becomes `core/build`.
--
-- Every stored reference to a pipeline kind moves at once: the queue, the per-project switches,
-- the queue ceilings, the steps of the plans that ran, and the plans themselves, declared or
-- frozen. Kinds are compared by equality in all these places; leaving one behind would quietly
-- switch a verification off or lift a ceiling. Action steps keep their references.

-- +goose Up
create temp table kind_map (old text primary key, new text not null) on commit drop;
insert into kind_map (old, new) values
    ('build', 'core/build'),
    ('retag', 'core/retag'),
    ('static-tests', 'php/static-tests'),
    ('unit-tests', 'php/unit-tests'),
    ('integration-tests', 'mse/integration-tests'),
    ('e2e-tests', 'mse/e2e-tests'),
    ('security-audit', 'security/security-audit'),
    ('load-tests', 'k6/load');

-- +goose StatementBegin
create function pg_temp.requalify(plan jsonb) returns jsonb language sql as $$
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
update project_pipelines p set kind = m.new from kind_map m where p.kind = m.old;
update queue_limits q set kind = m.new from kind_map m where q.kind = m.old;
update plan_steps s set ref = m.new from kind_map m where s.kind = 'pipeline' and s.ref = m.old;
update release_automations set plan = pg_temp.requalify(plan);
update reactions set plan = pg_temp.requalify(plan);
update plan_runs set plan = pg_temp.requalify(plan);

-- +goose Down
create temp table kind_map (old text primary key, new text not null) on commit drop;
insert into kind_map (old, new) values
    ('core/build', 'build'),
    ('core/retag', 'retag'),
    ('php/static-tests', 'static-tests'),
    ('php/unit-tests', 'unit-tests'),
    ('mse/integration-tests', 'integration-tests'),
    ('mse/e2e-tests', 'e2e-tests'),
    ('security/security-audit', 'security-audit'),
    ('k6/load', 'load-tests');

-- +goose StatementBegin
create function pg_temp.requalify(plan jsonb) returns jsonb language sql as $$
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
update project_pipelines p set kind = m.new from kind_map m where p.kind = m.old;
update queue_limits q set kind = m.new from kind_map m where q.kind = m.old;
update plan_steps s set ref = m.new from kind_map m where s.kind = 'pipeline' and s.ref = m.old;
update release_automations set plan = pg_temp.requalify(plan);
update reactions set plan = pg_temp.requalify(plan);
update plan_runs set plan = pg_temp.requalify(plan);
