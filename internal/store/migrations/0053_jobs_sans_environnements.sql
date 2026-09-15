-- Le noyau ne référence plus la table des environnements.
--
-- `jobs.environment_id` portait une clé étrangère vers `environments`, qui
-- appartient désormais au plugin MSE (doc 16 §16.17). Une table du noyau ne peut
-- pas dépendre d'une table de plugin : la colonne reste, en texte.

-- +goose Up
alter table jobs drop constraint if exists jobs_environment_id_fkey;

-- +goose Down
-- +goose StatementBegin
do $$
begin
    if to_regclass('public.environments') is not null then
        alter table jobs add constraint jobs_environment_id_fkey
            foreign key (environment_id) references environments (id);
    end if;
end
$$;
-- +goose StatementEnd
