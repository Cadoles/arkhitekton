-- Le mode `retag` d'un type de release devient `from-source`.
--
-- Une release produite depuis une autre re-tague les images de sa source et reconstruit, sur le
-- même commit et sous son propre tag, ce qui ne se re-tague pas : des binaires par exemple. Le nom
-- `retag` ne disait que la moitié de ce qu'elle fait.

-- +goose Up
update projects
set release_kinds = (
    select jsonb_agg(
        case when kind ->> 'mode' = 'retag' then kind || '{"mode": "from-source"}'::jsonb else kind end
        order by position)
    from jsonb_array_elements(release_kinds) with ordinality as entry (kind, position))
where release_kinds @> '[{"mode": "retag"}]'::jsonb;

alter table projects alter column release_kinds set default '[
  {"id":"pre","label":"Pré-release","mode":"build","tag":"prerelease-required","permission":"release:create"},
  {"id":"final","label":"Finale","mode":"from-source","source":"pre","tag":"prerelease-forbidden","permission":"release:create:final"},
  {"id":"patch","label":"Patch","mode":"build","tag":"prerelease-forbidden","permission":"release:create:final"}
]'::jsonb;

-- +goose Down
update projects
set release_kinds = (
    select jsonb_agg(
        case when kind ->> 'mode' = 'from-source' then kind || '{"mode": "retag"}'::jsonb else kind end
        order by position)
    from jsonb_array_elements(release_kinds) with ordinality as entry (kind, position))
where release_kinds @> '[{"mode": "from-source"}]'::jsonb;

alter table projects alter column release_kinds set default '[
  {"id":"pre","label":"Pré-release","mode":"build","tag":"prerelease-required","permission":"release:create"},
  {"id":"final","label":"Finale","mode":"retag","source":"pre","tag":"prerelease-forbidden","permission":"release:create:final"},
  {"id":"patch","label":"Patch","mode":"build","tag":"prerelease-forbidden","permission":"release:create:final"}
]'::jsonb;
