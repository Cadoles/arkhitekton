-- A TaskRun's reason and its steps' exit codes.
--
-- They are what tells a tool that failed the code from a tool that did not run: phpstan exits 1
-- when it finds violations, a killed container exits 137 and writes nothing. Recorded here before
-- anything reads them, so that runs in flight at the next upgrade already carry them.

-- +goose Up
alter table task_runs
    add column reason text,
    add column steps  jsonb not null default '[]'
        constraint task_runs_steps_is_array check (jsonb_typeof(steps) = 'array');

-- +goose Down
alter table task_runs
    drop column steps,
    drop column reason;
