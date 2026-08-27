-- Sort le nom de Redmine du modèle des demandes.
--
-- Suite de la migration 0030, qui a fait le même travail sur les versions. Les
-- demandes portaient encore `redmine_id` — un entier dont le nom disait de quel
-- produit il venait, et dont le **type** supposait que tout outil numérote.
-- Jira écrit `MSE-1234` (doc 12 §12.9, lot C).
--
-- Trois tables, une même règle : l'outil se nomme dans `tool`, son identifiant
-- vit dans `external_id`, en texte.
--
-- `release_notes` ne porte pas d'outil : une note appartient à une release, donc
-- à un projet, et c'est le projet qui déclare son suivi. Dupliquer le nom de
-- l'outil ici créerait deux vérités à tenir d'accord.

-- +goose Up

-- `issues` ne portait aucun projet, et son unicité était donc globale : deux
-- projets suivant deux instances Redmine se seraient écrasés au premier numéro
-- commun. Le paramètre existait pourtant déjà — il servait à borner la recherche
-- de la version — mais n'était pas conservé.
alter table issues add column if not exists project_id bigint references projects(id) on delete cascade;

-- +goose StatementBegin
do $$
declare
  defaut bigint;
  candidats int;
begin
  -- Le projet d'une demande rattachée se lit sur sa version.
  update issues i
  set project_id = v.project_id
  from versions v
  where i.version_id = v.id and i.project_id is null;

  -- Les orphelines — celles qu'aucune version ne réclame — n'ont pas de projet
  -- lisible. Elles viennent nécessairement du seul projet suivi : c'est lui qui
  -- a fait la synchronisation qui les a déposées.
  select count(*) into candidats
  from projects where coalesce(trim(tracker_project), '') <> '';

  if candidats = 1 then
    select id into defaut
    from projects where coalesce(trim(tracker_project), '') <> '';
    update issues set project_id = defaut where project_id is null;
  elsif candidats > 1 and exists (select 1 from issues where project_id is null) then
    -- Refuser plutôt que de choisir : rattacher au hasard mélangerait les
    -- demandes de deux clients, et rien ne permettrait de les démêler.
    raise exception 'demandes sans projet et plusieurs projets suivis: rattachement indécidable';
  end if;
end $$;
-- +goose StatementEnd

alter table issues add column if not exists tool text not null default '';
alter table issues add column if not exists external_id text;

update issues set tool = 'redmine', external_id = redmine_id::text;

alter table issues alter column external_id set not null;

-- L'identifiant est unique dans son outil et dans son projet : deux instances,
-- ou deux projets d'une même instance, peuvent numéroter pareil.
alter table issues drop constraint if exists issues_redmine_id_key;
create unique index if not exists issues_project_external_key
  on issues (project_id, tool, external_id);

alter table issues drop column if exists redmine_id;
alter table issues rename column redmine_updated_on to external_updated_on;

-- Posée après le rattachement : les lignes existantes doivent d'abord recevoir
-- leur projet.
alter table issues alter column project_id set not null;

-- Les notes désignent une demande par le même identifiant.
alter table release_notes add column if not exists external_id text;
update release_notes set external_id = redmine_id::text;
alter table release_notes alter column external_id set not null;

alter table release_notes drop constraint if exists release_notes_release_id_redmine_id_key;
alter table release_notes add constraint release_notes_release_external_key
  unique (release_id, external_id);

alter table release_notes drop column if exists redmine_id;

-- +goose Down
alter table release_notes add column if not exists redmine_id bigint;
update release_notes
set redmine_id = case when external_id ~ '^[0-9]+$' then external_id::bigint else 0 end;
alter table release_notes alter column redmine_id set not null;
alter table release_notes drop constraint if exists release_notes_release_external_key;
alter table release_notes add constraint release_notes_release_id_redmine_id_key
  unique (release_id, redmine_id);
alter table release_notes drop column if exists external_id;

alter table issues rename column external_updated_on to redmine_updated_on;
alter table issues add column if not exists redmine_id bigint;
update issues
set redmine_id = case when external_id ~ '^[0-9]+$' then external_id::bigint else id end;
alter table issues alter column redmine_id set not null;
drop index if exists issues_project_external_key;
alter table issues add constraint issues_redmine_id_key unique (redmine_id);
alter table issues drop column if exists external_id;
alter table issues drop column if exists tool;
alter table issues drop column if exists project_id;
