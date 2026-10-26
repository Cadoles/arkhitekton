-- +goose Up
-- Amorçage des données : ce qui est posé une fois, puis géré en base.

-- Deux jeux de données naissaient d'une variable d'environnement relue à chaque
-- fois : les administrateurs, testés à chaque connexion contre `BOOTSTRAP_ADMINS`,
-- et les dépôts pilotés, redéclarés à chaque démarrage de l'api.
--
-- Relire indéfiniment une configuration d'amorçage a deux défauts. La base cesse
-- d'être la source : retirer un dépôt de l'interface le voyait revenir au
-- redémarrage suivant, et rien à l'écran n'expliquait pourquoi. Et la
-- configuration devient un pouvoir permanent — qui peut l'éditer peut se
-- redonner un rôle administrateur, indéfiniment, sans laisser de trace autre
-- qu'une connexion.
--
-- L'amorçage a donc lieu **une fois**, avec la migration du schéma, et se
-- souvient de l'avoir fait.
create table bootstrap_steps (
    -- Le nom de l'étape. Une table plutôt qu'un drapeau unique : les étapes
    -- s'ajoutent avec le temps, et une base déjà amorcée doit pouvoir en
    -- recevoir une nouvelle sans rejouer les précédentes.
    step       text primary key,
    applied_at timestamptz not null default now(),
    -- Ce qui a été posé, pour que le journal de démarrage ne soit pas la seule
    -- trace de ce que l'amorçage a décidé.
    detail text not null default ''
);

comment on table bootstrap_steps is
    'Étapes d''amorçage déjà appliquées. Une étape présente n''est jamais rejouée.';

-- Les rôles déclarés à l'avance, en attente d'un sujet.
--
-- `user_roles` est indexée par `subject`, l'identifiant que le fournisseur met
-- dans le jeton — un nombre, chez Gitea. Il n'est connaissable qu'après la
-- première connexion : une ligne écrite à l'avance viserait un sujet inexistant,
-- et l'utilisateur réel se connecterait sans aucune permission. Le projet l'a
-- fait, et l'a retiré.
--
-- Cette table porte donc l'intention — « ce compte-là doit être administrateur »
-- — et la connexion la transforme en rôle. C'est la seule façon de déclarer un
-- rôle avant de connaître celui à qui il revient.
create table bootstrap_grants (
    -- Le `subject` si on le connaît, le nom de compte sinon. Les deux sont
    -- acceptés à la résolution : le premier est immuable et donc plus sûr, le
    -- second est la seule valeur qu'on puisse écrire à l'avance.
    identifier text primary key,
    role       text not null check (role in ('viewer', 'operator', 'releaser', 'admin')),

    -- Consommée à la première connexion qui correspond. Conservée plutôt que
    -- supprimée : « ce rôle a été réclamé par ce sujet, ce jour-là » est
    -- exactement ce qu'on cherche quand on se demande d'où vient une permission.
    claimed_at timestamptz,
    claimed_by text,

    created_at timestamptz not null default now()
);

comment on table bootstrap_grants is
    'Rôles déclarés avant que le sujet de leur titulaire ne soit connu. Consommés à la première connexion.';

-- +goose Down
drop table if exists bootstrap_grants;
drop table if exists bootstrap_steps;
