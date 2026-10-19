-- The capability a job instantiates, and the version of the plugin that brought it.
--
-- A run is read by the interpreter of the plugin version that launched it: an upgrade between the
-- launch and the end of the run must not change how it is read. Jobs from before carry no version,
-- which reads as "legacy".

-- +goose Up
alter table jobs
    add column capability     text,
    add column plugin_version text;

update jobs set capability = kind where kind like '%/%';

-- +goose Down
alter table jobs
    drop column plugin_version,
    drop column capability;
