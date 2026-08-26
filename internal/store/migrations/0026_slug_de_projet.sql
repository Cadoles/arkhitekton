-- Donne à chaque projet un nom utilisable dans une URL.
--
-- Les routes deviennent `/projects/{projet}/releases/{tag}` : le projet doit donc
-- s'écrire dans un segment de chemin. Aucune colonne existante ne convient —
-- `repository` porte une barre oblique (`vcarroy/mse`), `label` n'est pas unique,
-- et l'identifiant numérique ferait des URL que personne ne peut lire ni deviner.
--
-- Ces URL sont **mises en favori et collées dans des tickets**. Un identifiant
-- numérique y serait un choix qu'on paierait longtemps : il ne survit pas à une
-- réinstallation, et il ne dit rien de ce qu'il désigne (doc 12 §12.9, lot B).
--
-- Le slug est dérivé du dernier segment du dépôt, ce qui donne `mse-test-project`
-- pour `vcarroy/mse-test-project`. Modifiable ensuite depuis l'administration :
-- c'est un nom d'usage, pas une identité — celle-ci reste l'identifiant.

-- +goose Up

alter table projects add column if not exists slug text;

-- +goose StatementBegin
do $$
declare
  ligne record;
  candidat text;
  suffixe int;
begin
  for ligne in select id, repository from projects where coalesce(slug, '') = '' order by id loop
    -- Dernier segment du dépôt, ramené à ce qu'un chemin accepte. Tout ce qui
    -- n'est ni lettre, ni chiffre, ni tiret devient un tiret ; les tirets en
    -- bordure sont retirés.
    candidat := trim(both '-' from
      regexp_replace(lower(split_part(ligne.repository, '/', -1)), '[^a-z0-9-]+', '-', 'g'));

    if candidat = '' then
      candidat := 'projet-' || ligne.id;
    end if;

    -- Collision : suffixer plutôt qu'échouer. Deux dépôts homonymes chez deux
    -- propriétaires différents sont un cas normal, et refuser la migration pour
    -- cela empêcherait un déploiement de démarrer.
    suffixe := 1;
    while exists (select 1 from projects where slug = candidat) loop
      suffixe := suffixe + 1;
      candidat := trim(both '-' from
        regexp_replace(lower(split_part(ligne.repository, '/', -1)), '[^a-z0-9-]+', '-', 'g'))
        || '-' || suffixe;
    end loop;

    update projects set slug = candidat where id = ligne.id;
  end loop;
end $$;
-- +goose StatementEnd

-- Un slug vide rendrait une route `/projects//releases` que le routeur ne
-- rattacherait à rien : la contrainte le refuse à l'écriture plutôt que de
-- produire une URL morte.
alter table projects
  add constraint projects_slug_forme check (slug ~ '^[a-z0-9][a-z0-9-]*$');

create unique index if not exists projects_slug_key on projects (slug);
alter table projects alter column slug set not null;

-- +goose Down
alter table projects drop constraint if exists projects_slug_forme;
drop index if exists projects_slug_key;
alter table projects drop column if exists slug;
