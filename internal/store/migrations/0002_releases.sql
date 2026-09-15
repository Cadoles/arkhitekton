-- +goose Up
-- Releases et images. Les promotions appartiennent au plugin MSE (doc 16 §16.17).

create table releases (
    id          bigserial primary key,
    version_id  bigint not null references versions (id),
    -- Identité de la release : '1.8.0-rc2', '1.8.0'.
    tag         text   not null unique,
    kind        text   not null check (kind in ('pre', 'final', 'patch')),
    state       text   not null,
    git_ref     text   not null,
    git_branch  text,
    -- Une release finale réutilise l'image de la RC validée (re-tag) : ce qui
    -- part en production est bit pour bit ce qui a été validé en pré-production.
    -- Le version.txt embarqué reste donc celui de la RC, et c'est cette valeur
    -- que PortalData rapportera. Comparer PortalData.version à `tag` échouerait
    -- systématiquement sur toute release finale.
    image_version     text   not null,
    source_release_id bigint references releases (id),
    forge_release_url text,
    notes_md          text,
    quality_score     int,
    -- Actions manuelles : champ texte libre, renseigné au fil de la production
    -- du livrable, repris dans le bon de livraison.
    manual_actions_md         text,
    manual_actions_updated_by text,
    manual_actions_updated_at timestamptz,
    created_by  text        not null,
    released_at timestamptz,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);

create index releases_version_idx on releases (version_id, created_at desc);
create index releases_image_version_idx on releases (image_version);

create table release_images (
    release_id bigint not null references releases (id) on delete cascade,
    name       text   not null,
    repository text   not null,
    -- Tag de version, sans métadonnée de promotion.
    tag        text   not null,
    digest     text   not null,
    size_bytes bigint,
    created_at timestamptz not null default now(),
    primary key (release_id, name)
);

create index release_images_digest_idx on release_images (digest);

-- +goose Down
drop table if exists release_images;
drop table if exists releases;
