-- Sort le nom de Redmine du modèle des versions.
--
-- `versions.redmine_id` nommait un outil dans une colonne. Il portait en plus
-- deux informations distinctes sous un seul entier : **quel** identifiant, et —
-- par son signe — si la version venait vraiment du suivi. La migration 0028 a
-- extrait la seconde dans `origin` ; celle-ci extrait la première
-- (doc 12 §12.9, lot C).
--
-- Deux colonnes remplacent l'entier :
--
--   `tool`        nomme l'outil — `redmine`, et rien d'autre aujourd'hui. Vide
--                 quand la version n'en vient pas.
--
--                 **Pas `tracker`**, bien que ce soit le mot du contrat de
--                 fournisseur : `issues.tracker` existe déjà et désigne le *type*
--                 d'une demande — « Anomalie », « Évolution ». Les deux tables se
--                 joignent en permanence, et deux colonnes homonymes de sens
--                 différents dans une même requête sont une faute qu'on ne voit
--                 qu'en la relisant à froid.
--   `external_id` est l'identifiant **dans** cet outil, en texte : Redmine
--                 numérote, Jira écrit `MSE-1234`, et un entier fermait la porte
--                 au second sans qu'aucune erreur ne le dise.
--
-- `external_id` est **nul** pour une version que le dashboard s'est donnée. C'est
-- ce qui supprime le contournement du `redmine_id` négatif : il n'existait que
-- pour satisfaire un `not null` sur une colonne qui n'avait pas de valeur à
-- porter, et il obligeait à lire un signe pour connaître une nature.

-- +goose Up

alter table versions add column if not exists tool text not null default '';
alter table versions add column if not exists external_id text;

-- Les lignes existantes viennent toutes de Redmine ou n'en viennent pas du tout.
-- La traduction est exacte : c'était la seule lecture possible jusqu'ici.
update versions
set tool        = case when origin = 'tracker' then 'redmine' else '' end,
    external_id = case when origin = 'tracker' then redmine_id::text end;

-- L'identifiant est unique **dans son outil et dans son projet**. Deux projets
-- peuvent suivre deux instances, ou deux projets de la même : rien n'interdit que
-- le même numéro y désigne deux versions distinctes.
drop index if exists versions_project_redmine_key;
create unique index if not exists versions_project_external_key
  on versions (project_id, tool, external_id) where external_id is not null;

-- Un identifiant sans outil ne désigne rien, et un outil sans identifiant décrit
-- une version que rien ne confirmera : les deux vont ensemble ou pas du tout.
alter table versions add constraint versions_tool_coherent
  check ((tool = '') = (external_id is null));

alter table versions drop column if exists redmine_id;

-- `redmine_updated_on` nommait le même outil. Le curseur de fraîcheur n'a rien de
-- propre à Redmine : c'est la date que la source rapporte.
alter table versions rename column redmine_updated_on to external_updated_on;

-- +goose Down
alter table versions rename column external_updated_on to redmine_updated_on;
alter table versions drop constraint if exists versions_tool_coherent;
drop index if exists versions_project_external_key;

alter table versions add column if not exists redmine_id bigint;
update versions
set redmine_id = case
    when external_id ~ '^[0-9]+$' then external_id::bigint
    else -id
end;
alter table versions alter column redmine_id set not null;
create unique index if not exists versions_project_redmine_key
  on versions (project_id, redmine_id);

alter table versions drop column if exists external_id;
alter table versions drop column if exists tool;
