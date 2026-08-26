-- Déplace l'identité d'une version et d'une release dans le projet qui les porte.
--
-- Le dashboard versionne aujourd'hui **une** application, et cette hypothèse est
-- inscrite dans deux contraintes : un tag de release est unique dans toute la
-- base, un identifiant Redmine aussi. Deux projets suivis ensemble se
-- rencontreraient au premier `1.0.0`, et le second serait refusé sans que rien
-- n'explique pourquoi (doc 12 §12.9, lot B).
--
-- La table `projects` existe depuis la migration 0021 mais n'était que
-- descriptive : elle disait ce qu'un dépôt construit, sans que rien ne s'y
-- rattache. Cette migration la rend **porteuse**.
--
-- Rien d'autre ne bouge, et c'est le point : tout ce qui dépend d'une release —
-- artefacts, jobs, promotions, résultats de tests, signatures — y pend par une
-- clé étrangère, et hérite donc du projet sans porter de colonne. Une seule
-- exception serait à considérer le jour où un job existerait sans release.

-- +goose Up

alter table versions add column if not exists project_id bigint references projects(id);
alter table releases add column if not exists project_id bigint references projects(id);

-- +goose StatementBegin
do $$
declare
  cible bigint;
  candidats int;
begin
  -- Le projet d'accueil est celui qui se construit. Les autres dépôts déclarés
  -- sont clonés par des pipelines — kustomizations, campagne E2E, catalogue
  -- Tekton — et ne portent ni version ni release.
  select count(*) into candidats
  from projects where coalesce(trim(dockerfile), '') <> '';

  if candidats = 0 then
    -- Base sans projet constructible : il n'y a rien à rattacher, et inventer un
    -- projet ici créerait une ligne que personne n'a déclarée. Les colonnes sont
    -- posées vides, la contrainte NOT NULL le sera par la suite.
    raise notice 'aucun projet constructible: colonnes ajoutées sans rattachement';
    return;
  end if;

  if candidats > 1 then
    -- Refuser plutôt que de choisir. Rattacher au hasard mélangerait les
    -- historiques de deux applications, et rien ne permettrait ensuite de les
    -- démêler.
    raise exception 'plusieurs projets constructibles (%): impossible de dire lequel porte les releases existantes', candidats;
  end if;

  select id into cible
  from projects where coalesce(trim(dockerfile), '') <> '';

  update versions set project_id = cible where project_id is null;
  update releases set project_id = cible where project_id is null;
end $$;
-- +goose StatementEnd

-- L'unicité devient relative au projet. Deux applications suivies ensemble
-- publient chacune leur `1.0.0` sans se gêner.
--
-- Les anciennes contraintes sont retirées **après** que les nouvelles existent :
-- l'inverse laisserait une fenêtre pendant laquelle deux tags identiques
-- pourraient entrer.
create unique index if not exists releases_project_tag_key on releases (project_id, tag);
create unique index if not exists versions_project_redmine_key on versions (project_id, redmine_id);

alter table releases drop constraint if exists releases_tag_key;
alter table versions drop constraint if exists versions_redmine_id_key;

-- La recherche par nom normalisé sert au rapprochement d'une release avec sa
-- version. Elle doit se faire dans un projet, sans quoi une release de l'un
-- s'accrocherait à la version de l'autre.
drop index if exists versions_normalized_idx;
create index if not exists versions_project_normalized_idx on versions (project_id, normalized_name);

-- +goose Down
drop index if exists versions_project_normalized_idx;
create index if not exists versions_normalized_idx on versions (normalized_name);

drop index if exists releases_project_tag_key;
drop index if exists versions_project_redmine_key;

alter table releases add constraint releases_tag_key unique (tag);
alter table versions add constraint versions_redmine_id_key unique (redmine_id);

alter table releases drop column if exists project_id;
alter table versions drop column if exists project_id;
