-- A stored result describes itself: who produced it, in which shape, under which group.
--
-- The closed list of families goes: a plugin chooses the group its results are shown under. The
-- shape says how a result is stored and compared, and the producer which plugin version read it,
-- so that a replay reads it the same way. Results from before are attributed to the plugin that now
-- brings their tool, at a version that says they predate it.

-- +goose Up
alter table test_results drop constraint if exists test_results_family_check;
alter table test_results alter column family drop not null;

alter table test_results
    add column producer   text,
    add column shape      text,
    add column fact_group text,
    add column capability text;

update test_results set
    shape = case family when 'security' then 'findings' when 'load' then 'measures' else 'cases' end,
    fact_group = family,
    producer = case
        when tool in ('phpunit', 'phpstan', 'cs-fixer') then 'php@0.0.0-legacy'
        when tool in ('phpunit-integration', 'cypress') then 'mse@0.0.0-legacy'
        when tool = 'security-audit' then 'security@0.0.0-legacy'
        when tool = 'k6' then 'k6@0.0.0-legacy'
        else 'legacy@0.0.0-legacy'
    end;

update test_results t set capability = j.capability
  from runs r join jobs j on j.id = r.job_id
 where r.id = t.run_id;

alter table test_results
    alter column producer set not null,
    alter column shape set not null,
    alter column fact_group set not null,
    add constraint test_results_shape_check check (shape in ('cases', 'findings', 'measures')),
    add constraint test_results_producer_check check (producer ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?@\S+$'),
    add constraint test_results_group_check check (btrim(fact_group) <> '');

create index test_results_release_group_idx on test_results (release_id, fact_group);

-- +goose Down
drop index if exists test_results_release_group_idx;
alter table test_results
    drop column capability,
    drop column fact_group,
    drop column shape,
    drop column producer;
update test_results set family = 'unit' where family is null;
alter table test_results alter column family set not null;
-- Not validated: groups written since may lie outside the old list.
alter table test_results add constraint test_results_family_check
    check (family in ('unit', 'integration', 'e2e', 'load', 'static', 'security')) not valid;
