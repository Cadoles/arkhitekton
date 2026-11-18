-- Un job attend aussi qu'un autre pipeline libère le verrou qu'il partage avec lui : deux
-- pipelines qui déploient dans le même namespace ne tournent jamais ensemble.

-- +goose Up
alter table jobs drop constraint jobs_queue_reason_check;
alter table jobs
    add constraint jobs_queue_reason_check
    check (queue_reason in ('kind-busy', 'capacity', 'lock-held'));

-- +goose Down
alter table jobs drop constraint jobs_queue_reason_check;
update jobs set queue_reason = 'kind-busy' where queue_reason = 'lock-held';
alter table jobs
    add constraint jobs_queue_reason_check
    check (queue_reason in ('kind-busy', 'capacity'));
