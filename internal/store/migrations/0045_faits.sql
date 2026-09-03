-- Les faits que le dashboard constate.
--
-- Première pierre du chantier des automatisations (doc 14). Personne ne les
-- écoute encore : cette tranche les émet, les persiste et les affiche, sans
-- changer le moindre comportement.
--
-- Le livrable paraît pauvre et ne l'est pas. Il éprouve deux choses qu'on ne peut
-- pas éprouver en lisant du code — que les points d'émission sont les bons, et que
-- la déduplication tient à travers un redémarrage du worker — et il les éprouve
-- là où la correction ne coûte rien.
--
-- # Pourquoi persister un fait
--
-- Parce qu'il est rejouable, et parce que la déduplication a besoin d'une trace.
-- L'observateur redélivre les runs terminés — resynchronisation de l'informer,
-- redémarrage du worker —, et sans mémoire des faits déjà émis, chaque
-- redélivrance en produirait de nouveaux. Aujourd'hui cela ne coûterait rien ; le
-- jour où une réaction enverra une notification, cela en enverrait une par
-- redémarrage.
--
-- # Ce que la table n'est pas
--
-- Ni un journal d'audit, ni une file. Le journal d'audit dit **qui a fait quoi**
-- et sert au contrôle des attributions ; celle-ci dit **ce qui est arrivé**, y
-- compris ce que personne n'a demandé — un run qui se termine, une construction
-- qui échoue. Les deux se recoupent sans se confondre, et les fondre aurait fait
-- entrer dans un journal de contrôle des lignes que rien ne contrôle.

-- +goose Up

create table if not exists events (
    id          bigserial primary key,
    type        text        not null,
    -- Distingue le noyau d'un plugin. Vide pour le noyau.
    --
    -- Explicite plutôt que déduit du préfixe du type : un plugin retiré d'une
    -- version laisse des faits qu'on doit encore savoir attribuer, et les
    -- reconnaître à leur nom demanderait de connaître les plugins disparus.
    source      text        not null default '',
    -- Nul pour un fait qui ne vise aucun projet — un run antérieur à la
    -- migration 0035. `set null` et non `cascade` : effacer un projet ne doit
    -- pas effacer l'histoire de ce qui lui est arrivé.
    project_id  bigint      references projects (id) on delete set null,
    -- Ce sur quoi porte le fait, dans le vocabulaire de l'écran : un tag, un nom
    -- de run. Pour le lire, jamais pour le retrouver.
    subject     text        not null default '',
    payload     jsonb       not null default '{}'::jsonb,
    -- L'instant du **fait**, jamais celui de sa lecture. La distinction comptera
    -- pour les sondes, qui rattraperont des jours d'historique.
    occurred_at timestamptz not null,
    -- Profondeur d'enchaînement, contre les boucles. Zéro tant que rien ne
    -- réagit ; la colonne existe dès maintenant parce qu'un champ ajouté après
    -- coup laisse une histoire dont la profondeur est inconnue.
    depth       integer     not null default 0,
    -- La clé identifie le **fait**, pas sa lecture. C'est elle qui rend une
    -- redélivrance inoffensive.
    dedup_key   text        not null,
    recorded_at timestamptz not null default now()
);

-- L'unicité qui porte tout le mécanisme. Sans elle, la déduplication ne serait
-- qu'une intention du code appelant, et le premier chemin qui l'oublierait
-- rouvrirait la porte aux doublons.
create unique index if not exists events_dedup on events (dedup_key);

-- La charge est une carte plate de chaînes, comme `environments.deployer_settings`
-- et la configuration des plugins. Une valeur imbriquée traverserait le décodage
-- Go sans erreur pour échouer chez un consommateur, loin de sa cause.
alter table events add constraint events_payload_plate
    check (not jsonb_path_exists(payload, '$.* ? (@.type() <> "string")'));

alter table events add constraint events_type_non_vide
    check (btrim(type) <> '');

alter table events add constraint events_cle_non_vide
    check (btrim(dedup_key) <> '');

-- La lecture réelle : le fil d'un projet, du plus récent au plus ancien. C'est
-- ce que l'écran demande, et la seule requête que cette tranche introduit.
create index if not exists events_par_projet
    on events (project_id, occurred_at desc, id desc);

-- +goose Down
drop table if exists events;
