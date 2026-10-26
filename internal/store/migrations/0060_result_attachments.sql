-- Files kept with a result, and free attributes.
--
-- A fourth shape joins cases, findings and measures: attachments, shown as they are and never
-- compared. Any result may also carry some, like the videos of an end-to-end campaign beside its
-- cases. The file itself lives in object storage; the row says what it is and where.

-- +goose Up
alter table test_results drop constraint test_results_shape_check;
alter table test_results add constraint test_results_shape_check
    check (shape in ('cases', 'findings', 'measures', 'attachments'));
alter table test_results add column attributes jsonb not null default '{}'::jsonb;

create table result_attachments (
    id             bigserial primary key,
    test_result_id bigint not null references test_results (id) on delete cascade,
    name           text   not null check (btrim(name) <> ''),
    media_type     text   not null check (media_type like '%/%'),
    object         text   not null check (btrim(object) <> ''),
    size_bytes     bigint not null default 0 check (size_bytes >= 0),
    -- The case the file illustrates, named by its identity: suite and name, or neither.
    case_suite     text,
    case_name      text,
    attributes     jsonb  not null default '{}'::jsonb,
    check ((case_suite is null) = (case_name is null))
);

create index result_attachments_result_idx on result_attachments (test_result_id);

-- +goose Down
drop table result_attachments;
alter table test_results drop column attributes;
delete from test_results where shape = 'attachments';
alter table test_results drop constraint test_results_shape_check;
alter table test_results add constraint test_results_shape_check
    check (shape in ('cases', 'findings', 'measures'));
