-- Le réveil du worker quand une clé de déploiement est à poser ou un Secret à reprendre.
--
-- Le worker ne passait qu'une fois par minute : un projet déclaré attendait sa clé jusqu'à une
-- minute. L'API n'a pas de jeton du cluster et ne peut pas la poser elle-même ; elle peut en
-- revanche prévenir. Le signal part de la base plutôt que de chaque écriture côté Go : un projet
-- déclaré par la configuration, une clé oubliée ou un projet supprimé réveillent le worker de la
-- même façon, sans qu'un appelant oublié laisse attendre.
--
-- Sur une mise à jour, seulement le passage à « en attente » : l'échec d'une pose n'écrit pas ces
-- colonnes, et ne relance donc pas la pose en boucle. Un signal perdu est rattrapé par le passage de
-- chaque minute.

-- +goose Up
-- +goose StatementBegin
create or replace function notify_deploy_keys() returns trigger language plpgsql as $$
begin
  perform pg_notify('deploy_keys', '');
  return null;
end;
$$;
-- +goose StatementEnd

create trigger projects_key_awaited_on_insert
  after insert on projects
  for each row
  when (new.deploy_key_public = '' or new.deploy_key_rotate_requested_at is not null)
  execute function notify_deploy_keys();

create trigger projects_key_awaited_on_update
  after update of deploy_key_public, deploy_key_rotate_requested_at on projects
  for each row
  when ((new.deploy_key_public = '' and old.deploy_key_public <> '')
     or (new.deploy_key_rotate_requested_at is not null
         and new.deploy_key_rotate_requested_at is distinct from old.deploy_key_rotate_requested_at))
  execute function notify_deploy_keys();

create trigger secrets_to_delete_added
  after insert on project_secrets_to_delete
  for each statement
  execute function notify_deploy_keys();

-- +goose Down
drop trigger if exists secrets_to_delete_added on project_secrets_to_delete;
drop trigger if exists projects_key_awaited_on_update on projects;
drop trigger if exists projects_key_awaited_on_insert on projects;
drop function if exists notify_deploy_keys();
