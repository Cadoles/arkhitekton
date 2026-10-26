-- +goose Up
-- Cinquième seau : `client-test`, entre `doing` et `done`.
--
-- Il répond à l'ambiguïté signalée par la migration 0012. Cinq statuts de l'instance
-- — `Livré`, `Livré DEV`, `Prêt à livrer`, `Testé`, `Installation` — ne sont ni « en
-- cours » ni « terminé » : le développement y est fini, la validation du client non.
-- Faute d'un seau intermédiaire, ils étaient rangés en `doing` par prudence, ce qui
-- sous-estimait l'avancement réel, ou en `done`, ce qui aurait annoncé une version
-- complète avant toute recette.
--
-- **Ce seau ne compte pas comme terminé.** Il entre au dénominateur de la complétion,
-- jamais au numérateur : une demande en attente de validation client n'est pas
-- livrée. C'est ce qui distingue « fini de développer » de « accepté », et le
-- confondre est exactement ce que le taux de complétion doit éviter.
--
-- **Aucun statut n'est déplacé ici.** Ranger un statut dans ce seau déplace un taux
-- affiché au client, et cette décision lui appartient. Elle se prend depuis l'écran
-- d'administration, où la réaffectation réapplique la règle aux demandes déjà
-- enregistrées et journalise qui a tranché.

-- PostgreSQL ne modifie pas une contrainte `check` : il faut la retirer et la
-- reposer. Les contraintes de 0001 étaient anonymes, donc nommées par PostgreSQL
-- d'après la table et la colonne — `issues_status_bucket_check` et
-- `issue_status_mapping_bucket_check`, vérifiés sur la base déployée.
--
-- Le mode de panne à éviter est silencieux : si le nom différait, le `drop ... if
-- exists` ne retirerait rien, la nouvelle contrainte s'ajouterait sous un autre nom,
-- et l'ancienne continuerait de refuser `client-test`. Le symptôme serait une erreur
-- à l'affectation d'un statut, longtemps après la migration. D'où la vérification
-- finale, qui fait échouer la migration plutôt que de laisser la base dans cet état.
alter table issues drop constraint if exists issues_status_bucket_check;
alter table issues add constraint issues_status_bucket_check
    check (status_bucket in ('todo', 'doing', 'client-test', 'done', 'rejected'));

alter table issue_status_mapping drop constraint if exists issue_status_mapping_bucket_check;
alter table issue_status_mapping add constraint issue_status_mapping_bucket_check
    check (bucket in ('todo', 'doing', 'client-test', 'done', 'rejected'));

-- +goose StatementBegin
do $$
declare
    stale text;
begin
    -- Une seule contrainte de vérification doit subsister par table. Deux
    -- signifieraient qu'une ancienne a survécu et refuse toujours la nouvelle valeur.
    select string_agg(conrelid::regclass || '.' || conname, ', ')
      into stale
      from pg_constraint
     where contype = 'c'
       and conrelid in ('issues'::regclass, 'issue_status_mapping'::regclass)
       and conname not in ('issues_status_bucket_check', 'issue_status_mapping_bucket_check');

    if stale is not null then
        raise exception
            'contrainte de vérification résiduelle (%) : elle refuserait la valeur client-test', stale;
    end if;
end
$$;
-- +goose StatementEnd

-- +goose Down
-- Les demandes rangées dans le nouveau seau reviennent en `doing`, faute de quoi la
-- contrainte restaurée refuserait de s'appliquer. C'est le seau d'où elles venaient
-- avant l'existence de celui-ci, et le choix conserve le biais de prudence : la
-- descente ne doit pas faire apparaître des demandes comme terminées.
update issues set status_bucket = 'doing' where status_bucket = 'client-test';
update issue_status_mapping set bucket = 'doing' where bucket = 'client-test';

alter table issues drop constraint if exists issues_status_bucket_check;
alter table issues add constraint issues_status_bucket_check
    check (status_bucket in ('todo', 'doing', 'done', 'rejected'));

alter table issue_status_mapping drop constraint if exists issue_status_mapping_bucket_check;
alter table issue_status_mapping add constraint issue_status_mapping_bucket_check
    check (bucket in ('todo', 'doing', 'done', 'rejected'));
