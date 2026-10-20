-- The closed list of families is gone: a result is shown under its group.
--
-- `parsed` and `parsed_at` were never written.

-- +goose Up
drop index if exists test_results_release_family_idx;
alter table test_results
    drop column family,
    drop column parsed,
    drop column parsed_at;

-- +goose Down
alter table test_results
    add column family    text,
    add column parsed    jsonb,
    add column parsed_at timestamptz;
update test_results set family = fact_group;
create index test_results_release_family_idx on test_results (release_id, family);
