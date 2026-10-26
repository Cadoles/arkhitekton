-- L'automatisation d'une release : ce qu'un projet construit, puis vérifie.
--
-- # Ce que cette table retire du code
--
-- Le plan d'une release était composé en Go : une étape de construction depuis
-- la colonne `projects.targets`, une étape de vérification depuis les familles
-- du catalogue filtrées par `project_pipelines`. Deux sources, deux formes, et
-- aucune ne pouvait exprimer autre chose qu'« un Dockerfile à étapes ».
--
-- La table porte un **plan**, la même structure que `reactions.plan` : des
-- étapes en séquence, des gestes en parallèle. Un projet qui produit un binaire
-- ou une archive entre donc sans que le modèle change (doc 15 §15.5).
--
-- # Une par projet, et pas une liste
--
-- `reactions` est une liste : plusieurs réactions écoutent plusieurs faits. Ici
-- la question est unique — « que fait-on quand une release est déclarée ? » — et
-- deux réponses ouvriraient deux exécutions dont chacune ferait avancer l'état de
-- la release. La clé primaire est donc le projet.
--
-- # Pourquoi la ligne existe même vide
--
-- Un projet qui n'a pas encore d'automatisation n'a **pas de ligne** : c'est ce
-- qui permet à la reprise du worker d'en composer une depuis `targets`, une
-- seule fois. Un projet dont quelqu'un a vidé l'automatisation a une ligne avec
-- un plan vide.
--
-- Sans cette distinction, vider l'automatisation d'un projet la ferait
-- réapparaître au redémarrage suivant, et rien ne dirait pourquoi.

-- +goose Up
create table if not exists release_automations (
    project_id bigint      primary key references projects (id) on delete cascade,
    plan       jsonb       not null,
    updated_by text        not null default '',
    updated_at timestamptz not null default now()
);

-- Le plan est un objet, jamais une liste ni une chaîne. Un tableau traverserait
-- le décodage Go en rendant un plan sans étapes, et le projet cesserait de
-- construire sans que rien ne le signale.
alter table release_automations add constraint release_automations_plan_objet
    check (jsonb_typeof(plan) = 'object');

-- +goose Down
drop table if exists release_automations;
