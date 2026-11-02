-- Une passe demandée avant son heure.
--
-- Une livraison du webhook de la forge (push, pull request) est reçue par l'api, et le suivi des
-- pull requests tourne dans le worker. L'api pose la demande ici, le worker la reprend en
-- quelques secondes au lieu d'attendre sa prochaine passe.

-- +goose Up
alter table sync_state add column requested_at timestamptz;

-- +goose Down
alter table sync_state drop column if exists requested_at;
