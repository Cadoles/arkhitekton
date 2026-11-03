-- Une release naît brouillon, et seul son lancement déclenche la suite : la construction, les
-- vérifications, les réactions. Le brouillon laisse le temps d'y rattacher des tickets.
--
-- `launched_by` et `launched_at` disent qui a lancé, et quand. Les releases d'avant les brouillons
-- ont été lancées à leur création, par leur auteur : c'est ce que la reprise écrit. Une release
-- restée en `draft` faute d'avoir pu mettre une construction en file devient un vrai brouillon,
-- qu'on lance ou qu'on supprime.
--
-- `release_note_dismissals` retient les tickets retirés à la main d'une note : relancer la
-- détection sur un brouillon ne doit pas les y remettre.

-- +goose Up
alter table releases
    add column launched_by text,
    add column launched_at timestamptz;

update releases set launched_by = created_by, launched_at = created_at where state <> 'draft';

create table release_note_dismissals (
    release_id   bigint      not null references releases (id) on delete cascade,
    external_id  text        not null,
    dismissed_by text        not null,
    dismissed_at timestamptz not null default now(),
    primary key (release_id, external_id)
);

-- +goose Down
drop table release_note_dismissals;
alter table releases drop column launched_at, drop column launched_by;
