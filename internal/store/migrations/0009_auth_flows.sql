-- +goose Up
-- Transactions d'authentification en cours, partagées entre répliques.
--
-- Elles vivaient en mémoire du processus, ce qui était défendable tant que l'api
-- tournait en un seul exemplaire. Avec deux répliques et sans affinité de
-- session, `/auth/login` est servi par l'une et `/auth/callback` par l'autre :
-- l'état est introuvable, et une connexion sur deux échoue sur « session
-- d'authentification expirée ».
--
-- Les sessions étaient déjà en base pour cette raison. Les états de transition
-- devaient l'être aussi.
create table auth_flows (
    -- Le `state` est le seul de ces éléments qui transite par le navigateur, et
    -- il est opaque. Le vérificateur PKCE et le nonce restent côté serveur : les
    -- confier au client reviendrait à les offrir à qui intercepte la redirection.
    state text primary key,
    nonce text not null,
    code_verifier text not null,
    -- La page demandée avant l'authentification. Ramener l'utilisateur à
    -- l'accueil après une reconnexion lui fait perdre son contexte, souvent au
    -- milieu d'une opération.
    redirect_after text,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null
);

-- Index de la purge. Sans elle, chaque connexion commencée puis abandonnée
-- laisserait une ligne à vie.
create index auth_flows_expiry_idx on auth_flows (expires_at);

-- +goose Down
drop table if exists auth_flows;
