-- When a run was read, and by which plugin version.
--
-- Reading a run is triggered by both the PipelineRun and its TaskRuns, in no guaranteed order, and
-- again at every resync: the mark is what makes it happen once. Every run already finished was read
-- by the ingestion that came before; marking them keeps the informer from reading them all again.

-- +goose Up
alter table runs
    add column interpreted_at timestamptz,
    add column interpreted_by text;

update runs set interpreted_at = coalesce(completed_at, now()), interpreted_by = 'legacy'
 where status in ('succeeded', 'failed', 'cancelled', 'timedout');

-- +goose Down
alter table runs
    drop column interpreted_by,
    drop column interpreted_at;
