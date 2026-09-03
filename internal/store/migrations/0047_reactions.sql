-- Les réactions déclarées : « sur tel fait, exécuter tel plan ».
--
-- Sixième tranche du chantier des automatisations (doc 14). Les trois précédentes
-- ont posé les faits, le moteur et les actions ; il manquait ce qui les relie et
-- qu'un humain peut écrire.
--
-- La table a été **délibérément différée** jusqu'ici : sa forme appartient à
-- l'éditeur, et la deviner plus tôt aurait figé des colonnes qu'on n'avait pas
-- éprouvées. `plan_runs.trigger` porte depuis le début une chaîne — `builtin:…`
-- ou `reaction:<id>` — précisément pour que cette table puisse arriver sans
-- migration de ce qui la précède.
--
-- # Par projet, comme les plugins
--
-- C'est là que vivent les spécificités (doc 12 §12.5). Une réaction déclarée sur
-- un projet ne regarde que ses faits ; deux clients qui veulent la même
-- notification l'écrivent chacun, et peuvent la corriger sans se gêner.
--
-- # Ce que le schéma ne vérifie pas
--
-- Ni que le type de fait existe, ni que les gestes du plan désignent quelque
-- chose. Les registres vivent dans le binaire — plugins, actions, catalogue — et
-- les recopier en base demanderait une migration à chaque ajout, pour apprendre
-- une seconde fois ce que le code sait déjà.
--
-- Ces trois contrôles ont lieu **à l'enregistrement**, dans la couche qui a les
-- registres sous la main. Une réaction dont le plugin a été retiré d'une version
-- reste donc lisible en base et ne se déclenche plus, ce qui est le comportement
-- voulu : éteindre n'est pas supprimer.

-- +goose Up

create table if not exists reactions (
    id         bigserial primary key,
    project_id bigint      not null references projects (id) on delete cascade,
    -- Le nom sert à la retrouver dans une liste. « Réaction 7 » n'apprend rien.
    name       text        not null,
    -- Le fait écouté : `release.verified`, ou `gitea.pull_request.opened` pour un
    -- fait de plugin.
    event_type text        not null,
    enabled    boolean     not null default true,
    -- L'appariement sur la charge du fait. Vide : la réaction part sur tout fait
    -- de ce type.
    match      jsonb       not null default '{}'::jsonb,
    plan       jsonb       not null,
    created_by text        not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

-- Deux réactions d'un projet ne portent pas le même nom : c'est ce qui rend une
-- liste lisible, et une correction sans ambiguïté sur laquelle des deux.
create unique index if not exists reactions_nom_par_projet
    on reactions (project_id, lower(btrim(name)));

-- La question que pose chaque fait : qui écoute ce type, dans ce projet ?
create index if not exists reactions_par_fait
    on reactions (project_id, event_type) where enabled;

-- L'appariement est une carte plate de chaînes, comme la charge qu'il apparie.
-- Une valeur imbriquée traverserait le décodage Go sans erreur pour ne jamais
-- apparier, et rien ne dirait pourquoi.
alter table reactions add constraint reactions_match_plate
    check (not jsonb_path_exists(match, '$.* ? (@.type() <> "string")'));

alter table reactions add constraint reactions_nom_non_vide
    check (btrim(name) <> '');

alter table reactions add constraint reactions_fait_non_vide
    check (btrim(event_type) <> '');

-- +goose Down
drop table if exists reactions;
