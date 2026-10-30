-- Les faits qu'une source pousse, en attente d'être routés vers les réactions.
--
-- Un webhook arrive à l'`api`, qui l'authentifie et enregistre ses faits. Les réactions, elles,
-- partent du `worker` : c'est lui qui porte le transport sortant des actions, et l'`api` n'a pas
-- à l'avoir. La colonne est le passage de l'un à l'autre, et elle ne vaut que pour ces faits :
-- une sonde ou l'observateur routent ce qu'ils viennent d'écrire, sans attendre.
--
-- Un fait reçu deux fois n'est enregistré qu'une fois, par sa clé de déduplication : il n'est
-- donc routé qu'une fois. C'est ce qui tient lieu d'anti-rejeu.

-- +goose Up
alter table events add column dispatch_pending boolean not null default false;
create index events_dispatch_pending on events (id) where dispatch_pending;

-- +goose Down
drop index if exists events_dispatch_pending;
alter table events drop column if exists dispatch_pending;
