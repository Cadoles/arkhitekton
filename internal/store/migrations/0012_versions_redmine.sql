-- +goose Up
-- Forme comparable du nom d'une version, et projection des statuts réels.

-- B8 et B17 posaient l'invariant « nom de version Redmine = tag de release ».
-- L'instance réelle porte quatre conventions simultanées dans le même projet :
--
--   MSE3-1.5 … MSE3-1.21   treize versions closes — convention historique
--   MSE 3.1.0 … MSE 3.1.2  trois versions closes
--   v3.1.1 … v3.2.0        quatre versions ouvertes — convention courante
--
-- Le nom reste stocké tel quel : l'identité d'une version est son `redmine_id`, et
-- réécrire le nom masquerait ce que l'utilisateur lit dans Redmine. La colonne
-- ci-dessous porte la forme comparable, calculée en Go par
-- `release.NormalizeVersionName` — une seule implémentation, plutôt qu'une colonne
-- générée qui dupliquerait la règle en SQL et divergerait au premier ajustement.
alter table versions add column normalized_name text;

-- Rattrapage des lignes existantes. Toutes sont des versions provisoires créées par
-- `EnsureVersion` à partir de `release.Version.Base`, qui est toujours un `X.Y.Z`
-- nu : la normalisation y est l'identité, et recopier le nom est donc exact et non
-- une approximation. Aucune version réelle n'est encore en base, la synchronisation
-- n'ayant jamais tourné.
update versions set normalized_name = trim(name) where normalized_name is null;

-- **Index non unique, à dessein.** `MSE 3.1.2` et `v3.1.2` se réduisent tous deux à
-- `3.1.2` et coexistent réellement, l'une close et l'autre ouverte. Une contrainte
-- d'unicité ferait échouer la synchronisation sur des données parfaitement
-- légitimes côté client. L'ambiguïté est signalée dans l'écran d'administration,
-- pas résolue par une contrainte.
create index versions_normalized_idx on versions (normalized_name);

-- Projection des statuts de l'instance vers les quatre seaux du dashboard.
--
-- **Cette table encode un jugement qui déplace le taux de complétion affiché au
-- client, et attend sa validation.** La règle appliquée ici : seuls les états
-- terminaux sans ambiguïté vont dans `done`, tout état intermédiaire ou discutable
-- reste dans `doing`. Ce biais est délibéré — sous-estimer la complétion se voit et
-- provoque une question, la surestimer annonce une version complète alors que la
-- recette n'a pas eu lieu.
--
-- Les cas à trancher avec le client, tous placés en `doing` par prudence :
-- `Livré`, `Livré DEV`, `Prêt à livrer`, `Testé`, `Installation`.
--
-- `on conflict do nothing` : une correction faite en base ou depuis l'écran
-- d'administration ne doit pas être écrasée si cette migration est rejouée.
insert into issue_status_mapping (status_name, bucket) values
    -- Rien n'a commencé.
    ('Brouillon',           'todo'),
    ('Nouveau',             'todo'),
    ('Ready',               'todo'),
    ('En attente',          'todo'),
    ('En Standby',          'todo'),
    -- Deux statuts ne différant que par la casse coexistent dans l'instance. Les
    -- deux sont projetés, faute de quoi l'un tomberait en « non mappé ».
    ('En StandBy',          'todo'),
    ('Non exécuté',         'todo'),

    -- En cours, au sens large.
    ('Analyse',             'doing'),
    ('Pris en charge',      'doing'),
    ('En cours',            'doing'),
    ('Développement',       'doing'),
    ('En revue',            'doing'),
    ('Test',                'doing'),
    ('Test en échec',       'doing'),
    ('Exécuté en échec',    'doing'),
    ('Commentaire',         'doing'),
    ('Documentation',       'doing'),
    ('Test usine',          'doing'),
    ('A livrer usine',      'doing'),
    -- À trancher : livré n'est pas nécessairement recetté, et Redmine ne déclare
    -- aucun de ces états comme clos.
    ('Livré DEV',           'doing'),
    ('Prêt à livrer',       'doing'),
    ('Livré',               'doing'),
    ('Testé',               'doing'),
    ('Installation',        'doing'),
    -- `Ré-ouvert` est déclaré **clos** par la configuration de l'instance, ce qui
    -- ressemble à une erreur : une demande réouverte est en cours. Le drapeau de
    -- Redmine est conservé tel quel dans `issues.is_closed`, la projection dit
    -- l'inverse, et l'écart est signalé dans l'écran d'administration plutôt que
    -- corrigé en silence chez le client.
    ('Ré-ouvert',           'doing'),

    -- États terminaux sans ambiguïté.
    ('Résolu',              'done'),
    ('Fermé',               'done'),
    ('Done',                'done'),
    ('Exécuté avec succès', 'done'),

    -- Hors périmètre : ne pèse ni au numérateur ni au dénominateur (doc 04 §4.5).
    ('Rejeté',              'rejected')
on conflict (status_name) do nothing;

-- +goose Down
drop index if exists versions_normalized_idx;
alter table versions drop column if exists normalized_name;
-- Le mapping n'est pas vidé : il a pu être corrigé à la main depuis, et une
-- migration descendante n'a pas à emporter le travail de quelqu'un d'autre.
