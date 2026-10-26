-- +goose Up
-- Une file par type de job, avec son propre plafond.

-- Le modèle précédent groupait les jobs en **classes d'exclusion** :
-- `parallel` n'excluait rien, `exclusive-<nom>` excluait sa propre classe,
-- `exclusive-cluster` excluait tout. Trois régimes différents pour une seule
-- question — combien de ces exécutions peuvent tourner ensemble — et une réponse
-- qui ne se lisait nulle part : il fallait retrouver quels kinds partageaient une
-- classe pour savoir ce qui attendait quoi.
--
-- Le plafond par type dit la même chose en un nombre. Un type qui ne supporte
-- pas le parallélisme a un plafond de 1, ce que la classe exclusive exprimait ;
-- les autres en ont un supérieur, ce que `parallel` exprimait moins bien, en ne
-- laissant que le plafond global les retenir.
--
-- Ce qui est perdu, et il faut le dire : `exclusive-cluster` réservait le cluster
-- entier pour une mesure de charge. Aucun plafond par type ne reproduit cela. Le
-- constat qui fonde la comparabilité d'une mesure (B16) ne repose donc plus sur
-- une réservation mais sur `concurrent_at_start`, déjà relevé à la réservation :
-- une mesure prise pendant qu'autre chose tournait n'entre pas dans la
-- comparaison, au lieu d'être empêchée de se produire.

create table queue_limits (
    -- Le type de job, tel que le catalogue le nomme. Pas de clé étrangère : le
    -- catalogue vit en ConfigMap, hors de la base, et un type retiré ne doit pas
    -- faire échouer l'écriture d'un réglage voisin.
    kind           text primary key,
    max_concurrent int  not null check (max_concurrent >= 1),
    updated_by     text not null,
    updated_at     timestamptz not null default now()
);

comment on table queue_limits is
    'Plafonds de file réglés à la main. Absent = celui du catalogue.';

-- Seuls les réglages **modifiés** sont stockés. Recopier ici le catalogue au
-- démarrage figerait sa valeur : changer un défaut dans le ConfigMap n''aurait
-- alors plus d''effet, et rien ne dirait pourquoi.

-- La classe n'est plus lue. La colonne reste pour les jobs déjà enregistrés —
-- l'effacer réécrirait l'histoire — mais cesse d'être exigée.
alter table jobs alter column queue_class drop not null;

comment on column jobs.queue_class is
    'Historique : classe d''exclusion du modèle précédent. Plus renseignée, plus lue.';

-- Les motifs d'attente suivent le même mouvement. `class-busy` devient
-- `kind-busy` : ce n'est plus une classe partagée qui est occupée mais la file
-- du type lui-même. `draining` disparaît avec la barrière qu'il expliquait.
alter table jobs drop constraint jobs_queue_reason_check;

update jobs set queue_reason = 'kind-busy' where queue_reason = 'class-busy';
update jobs set queue_reason = null where queue_reason = 'draining';

alter table jobs
    add constraint jobs_queue_reason_check
    check (queue_reason in ('kind-busy', 'capacity'));

-- +goose Down
alter table jobs drop constraint jobs_queue_reason_check;
update jobs set queue_reason = 'class-busy' where queue_reason = 'kind-busy';
alter table jobs
    add constraint jobs_queue_reason_check
    check (queue_reason in ('class-busy', 'capacity', 'draining'));

update jobs set queue_class = kind where queue_class is null;
alter table jobs alter column queue_class set not null;

drop table if exists queue_limits;
