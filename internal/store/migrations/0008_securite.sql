-- +goose Up
-- Findings de sécurité normalisés.
--
-- Le contrat vient de `pkg/security` du manager, verrouillé par P7 : le
-- dashboard ne renormalise rien. Réimplémenter la fusion des sévérités, la
-- déduplication ou l'empreinte donnerait deux vérités sur les mêmes findings —
-- celle du pipeline qui décide de l'échec, et celle de l'interface qui l'affiche.
create table security_findings (
    id             bigserial primary key,
    test_result_id bigint not null references test_results (id) on delete cascade,
    -- L'empreinte est calculée par le manager à partir de la cible, la règle, le
    -- fichier et le paquet. C'est elle qui rend un finding suivable d'un run à
    -- l'autre, et donc « nouveau » ou « résolu » distinguable de « toujours là ».
    fingerprint text not null,
    run_id      text not null,
    target_kind text not null,
    target_name text not null,
    tool        text not null,
    category    text not null,
    rule_id     text not null,
    -- L'échelle est celle de `pkg/security`, qui y ramène tous les outils. La
    -- contrainte la reprend telle quelle : une valeur hors échelle signalerait
    -- une divergence de contrat, et vaut mieux qu'une insertion silencieuse.
    severity    text not null check (severity in ('CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'INFO')),
    cvss        double precision,
    title       text not null,
    description text,
    location    text,
    package_name      text,
    -- `installed` et `fixed`, et non une « version » unique : ce qui décide
    -- d'une action est l'écart entre les deux.
    package_installed text,
    package_fixed     text,
    fixable     boolean not null default false,
    -- `new`, `existing` ou `resolved`, tel que le manager l'a établi en comparant
    -- au run précédent.
    status text not null,
    first_seen text,
    -- Un finding supprimé reste enregistré : une exception est une décision
    -- humaine datée, et la trace de ce qui a été écarté vaut autant que la liste
    -- de ce qui reste. Il ne compte simplement pas dans le score.
    suppressed         boolean not null default false,
    suppression_reason text,
    suppression_expires text,
    created_at timestamptz not null default now()
);

create index security_findings_result_idx on security_findings (test_result_id);
-- L'index du suivi : l'historique d'un finding donné, à travers les runs.
create index security_findings_fingerprint_idx on security_findings (fingerprint, created_at desc);
-- L'index du tri d'affichage : les critiques non supprimés d'abord.
create index security_findings_severity_idx on security_findings (test_result_id, severity)
    where not suppressed;

-- Agrégats d'un audit, tels que `summary.json` les porte.
--
-- Stockés plutôt que recalculés depuis les findings : le manager y met des
-- informations que les findings ne portent pas — quels scanners ont échoué,
-- quelle politique s'appliquait, combien de findings ont été supprimés. Les
-- recalculer perdrait précisément ce qui distingue « l'audit n'a rien trouvé »
-- de « l'audit n'a pas pu conclure ».
create table security_audits (
    test_result_id bigint primary key references test_results (id) on delete cascade,
    run_id         text not null,
    generated_at   text,
    total     int not null default 0,
    critical  int not null default 0,
    high      int not null default 0,
    medium    int not null default 0,
    low       int not null default 0,
    suppressed int not null default 0,
    delta_new      int not null default 0,
    delta_existing int not null default 0,
    delta_resolved int not null default 0,
    -- Scanners en échec, nommés. Un audit dont Trivy n'a pas tourné ne peut pas
    -- prétendre qu'aucune vulnérabilité n'existe : le signal correspondant
    -- devient « non mesuré » et son poids est redistribué (B11).
    failed_scanners text[] not null default '{}',
    scanner_status  jsonb,
    policy          jsonb,
    previous_run_id text,
    created_at timestamptz not null default now()
);

-- +goose Down
drop table if exists security_audits;
drop table if exists security_findings;
