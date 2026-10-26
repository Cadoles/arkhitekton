-- +goose Up
-- Demandes Redmine rattachées à une release, pour sa note.

-- La table est un **rattachement**, pas une copie : le sujet, le tracker et le
-- statut d'une demande vivent dans `issues` et changent avec elle. Les
-- recopier ici figerait une note au moment de sa génération, et un sujet corrigé
-- dans Redmine n'apparaîtrait jamais.
--
-- Ce qui appartient en propre à la note, et qui est donc stocké : quelle demande
-- en fait partie, comment elle y est entrée, et qui l'a ajoutée.
create table release_notes (
    id         bigserial primary key,
    release_id bigint not null references releases (id) on delete cascade,
    -- L'identifiant Redmine, et non la clé locale de `issues` : une
    -- demande peut être rattachée avant que la synchronisation ne l'ait vue, et
    -- une clé étrangère l'interdirait. La jointure se fait sur ce numéro.
    redmine_id bigint not null,

    -- `auto` : détectée à la création par le rapprochement avec les PR fusionnées.
    -- `manual` : ajoutée à la main, parce que la convention `#1234` n'a pas été
    -- suivie ou que la demande n'était pas encore en revue.
    --
    -- La distinction est conservée parce qu'elle se lit : une note entièrement
    -- manuelle dit que la détection n'a rien trouvé, ce qui est un signal.
    source text not null check (source in ('auto', 'manual')),

    -- La pull request qui a motivé le rattachement automatique. Vide sur un ajout
    -- manuel. Sert à retrouver le code d'une ligne de note.
    pull_request text not null default '',

    added_by text not null,
    added_at timestamptz not null default now(),

    -- Une demande n'apparaît qu'une fois dans une note. Sans cette contrainte, un
    -- second passage de détection la doublerait, et la note compterait deux fois
    -- la même chose.
    unique (release_id, redmine_id)
);

create index release_notes_release_idx on release_notes (release_id);

comment on table release_notes is
    'Demandes Redmine rattachées à une release. Rattachement seul : le contenu vit dans issues.';

-- +goose Down
drop table if exists release_notes;
