# 04 — Modèle de données

## 4.1 Entités et vocabulaire

Le vocabulaire est la première source de confusion du projet : « version » désigne à la
fois un jalon Redmine, un numéro sémantique, un tag Git et un tag d'image. Convention
retenue dans tout le code et l'interface :

| Terme | Définition |
|---|---|
| **Version** | jalon fonctionnel, miroir d'une *version* Redmine. Porte un numéro (`1.8.0`), un périmètre (les demandes), un état. C'est l'unité de planification. |
| **Release** | matérialisation d'une version à un instant : un tag Git, un jeu d'images, des résultats de tests. Une version peut avoir plusieurs releases (`1.8.0-rc1`, `1.8.0-rc2`, `1.8.0`). |
| **Deployment** | fait qu'une release donnée tourne sur un environnement donné à partir d'une date. |
| **Job** | demande d'exécution soumise par le dashboard ; possède un état de file. |
| **Run** | `PipelineRun` Tekton correspondant à un job (ou observé sans job, s'il a été déclenché hors dashboard). |
| **TestResult** | résultat parsé d'une famille de tests pour un run. |

Une release est immuable une fois publiée ; seuls ses résultats de tests et ses
déploiements s'y ajoutent.

## 4.2 États

**Version** (dérivé de Redmine, non stocké comme état propre) :
`open` → `locked` → `closed`, plus un pourcentage de complétion calculé.

**Release** :

Deux chemins, selon que la release est **construite** ou **dérivée**.

```
construite (pre, patch)
  draft ──▶ building ──▶ verifying ──▶ ready ──▶ released
               │             │           │
               └─────────────┴───────────┴──────▶ failed
                                                  │
  (à tout moment, si abandonnée) ────────────────▶ cancelled

dérivée (final, re-tag d'une RC validée)
  draft ──▶ ready ──▶ released
```

- `draft` — créée dans le dashboard, tag Git pas encore posé.
- `building` — tag posé, `build-image` en cours.
- `verifying` — images disponibles, batterie de tests en cours ou partielle.
- `ready` — tous les tests obligatoires terminés, décision humaine en attente.
- `released` — validée, promouvable sur les environnements de rang supérieur.
- `failed` — un pipeline obligatoire a échoué ; relançable, ce qui ramène en `verifying`.

**Le chemin dérivé ne passe ni par `building` ni par `verifying`** : rien n'est construit,
donc rien ne s'exécute ([B17](02-blocages-et-decisions.md#b17)). La release finale entre
directement en `ready` avec la validation héritée de sa RC source. Modéliser un
`building` fictif ferait attendre l'interface sur un pipeline qui ne viendra jamais.

La transition `draft → ready` d'une release dérivée est conditionnée : la RC source doit
être en `released`, et un avertissement est présenté si ses tests ne sont pas au vert —
le re-tag transporte la validation, donc aussi son absence.

*`verifying` plutôt que `testing` : `testing` est l'ancien nom de l'environnement
`qualif` ([B13](02-blocages-et-decisions.md#b13)) et reste présent dans le code
existant. Deux sens pour un même mot dans le même système est une confusion gratuite.*

Les transitions autorisées vivent dans `internal/domain/release` et sont testées
unitairement. Aucune écriture d'état ne contourne cette table de transitions.

**Job** :

```
queued ──▶ running ──▶ succeeded
   │           │
   │           ├─────▶ failed
   │           └─────▶ cancelled
   └─────────────────▶ cancelled   (annulé avant démarrage)
```

## 4.3 Schéma PostgreSQL

Colonnes techniques omises pour la lisibilité : chaque table porte
`created_at timestamptz not null default now()` et `updated_at timestamptz not null default now()`.

### Référentiel

> **Doc 16 §16.17.** `environments`, `image_promotions`, `environment_observations` et
> `release_signoffs` appartiennent au plugin MSE : elles sont créées par ses migrations
> (`internal/mse/storage/migrations`), et `jobs.environment_id` n'a plus de clé étrangère.
> Les définitions ci-dessous décrivent leur schéma, pas l'endroit où il est posé.

Les réglages propres à l'outil de déploiement vivent dans une carte que le
dashboard **transporte sans la lire** : trois colonnes les portaient jusqu'à la
migration 0033, et elles nommaient Flux dans un schéma que tout projet doit
pouvoir remplir. Seul `internal/mse/flux` connaît ses clés — `promotionSuffix`,
`acceptsPrerelease`, `kustomization` — et c'est lui qui les valide, contre les
politiques réelles du cluster (doc 12 §12.9, lot D).

```sql
-- Environnements : table de configuration, pas de constantes en dur (cf. B13)
create table environments (
  id              text primary key,           -- 'unstable', 'qualif', 'preprod', 'prod'
  label           text not null,              -- 'Pré-production'
  aliases         text[] not null default '{}', -- LECTURE : anciens noms reconnus en entrée
  rank            int  not null,              -- ordre dans le flux de promotion
  namespace       text not null,              -- namespace Kubernetes
  -- Réglages du déployeur, dans SON vocabulaire. Une carte de chaînes : c'est
  -- ce qu'une configuration transporte, et la contrainte le vérifie.
  deployer_settings jsonb not null default '{}'::jsonb
                    check (not jsonb_path_exists(
                        deployer_settings, '$.* ? (@.type() <> "string")')),
  security_policy text,                       -- clé dans policies/severity.yaml
  observable      boolean not null default true, -- false pour prod (cluster séparé, cf. B8)
  visible         boolean not null default true,
  unique (rank)
  -- L'unicité des suffixes de promotion n'est plus une contrainte du schéma :
  -- c'est une règle du déployeur, vérifiée par `flux.CheckPromotionTargets`.
);
```

| id | libellé | `promotionSuffix` | tag produit | RC | rang |
|---|---|---|---|:--:|---|
| `unstable` | Unstable | `unstable` | `1.8.0-rc1_unstable` | ● | 1 |
| `qualif` | Qualification | **`testing`** | `1.8.0-rc1_testing` | ● | 2 |
| `preprod` | Pré-production | `preprod` | `1.8.0-rc1_preprod` | ● | 3 |
| `prod` | Production | `prod` | `1.8.0_prod` | | 4 |

La frontière RC / release finale se situe **entre `preprod` et `prod`** : les trois
premiers environnements reçoivent des pré-releases, seule la production exige une version
stable.

La **sensibilité** n'est pas une colonne : elle se déclare au catalogue (`sensitive`) et
réserve la promotion au rôle `releaser`. `preprod` et `prod` sont sensibles ; un
environnement qui ne la déclare pas l'est aussi (doc 16 §16.15).

`acceptsPrerelease` n'est pas une préférence d'affichage : une plage SemVer sans
composante de pré-release (`>=0.0.0`) ne sélectionne aucune RC. Promouvoir une
pré-release vers `prod` produirait un tag valide, poussé sans erreur, que Flux
ignorerait — un échec silencieux. Le dashboard refuse donc l'opération en amont, et
vérifie au démarrage que ce réglage correspond à la plage réellement configurée dans
l'`ImagePolicy` ([B7](02-blocages-et-decisions.md#b7)).

**`aliases` et `promotionSuffix` ne sont pas redondants** — ils vont dans des directions
opposées ([B13](02-blocages-et-decisions.md#b13)). `aliases` traduit une valeur héritée
rencontrée *en entrée* ; `promotionSuffix` produit le suffixe de tag *en sortie*, celui
sur lequel Flux route le déploiement. Une erreur sur le premier affiche un mauvais
libellé, une erreur sur le second **promeut vers le mauvais environnement** : d'où
l'unicité exigée et la vérification au démarrage contre les `ImagePolicy` du cluster,
toutes deux portées par le déployeur depuis le lot D.

Le tag de promotion est produit par un gabarit configurable, de défaut
`{version}_{suffix}` : `+` est interdit dans un tag OCI, l'underscore est le séparateur
retenu ([B7](02-blocages-et-decisions.md#b7)).

Il n'y a pas de `floating_tag` : chaque promotion produit un tag immuable et distinct,
pointant sur le digest déjà construit.

Le dashboard n'affiche jamais qu'un identifiant canonique et passe toujours
l'environnement explicitement aux pipelines, sans s'appuyer sur leurs valeurs par défaut
(`environment: dev` du pipeline `e2e-test` désigne l'environnement local de
développement, hors périmètre).

### Versions et demandes du suivi

L'outil de suivi se nomme dans `tool`, et son identifiant vit dans `external_id`,
**en texte** : Redmine numérote, un autre outil peut écrire `MSE-1234`. Un entier
fermait la porte au second sans qu'aucune erreur ne le dise. L'identifiant est
unique dans son outil **et dans son projet** : deux projets peuvent suivre deux
instances, ou deux projets d'une même instance, où le même numéro désigne deux
choses (migrations 0030 et 0031, doc 12 §12.9 lot C).

`external_id` est **nul** pour une version que le dashboard s'est donnée —
provisoire posée par `EnsureVersion`, ou version d'un projet sans suivi. C'est ce
qui a remplacé le contournement de l'identifiant négatif : la nature d'une
version se lit dans `origin`, jamais dans le signe d'un nombre.

```sql
create table versions (
  id                 bigserial primary key,
  project_id         bigint not null references projects(id) on delete cascade,
  tool               text   not null default '',   -- '' quand la version ne vient pas d'un suivi
  external_id        text,                         -- identifiant dans cet outil, nul sinon
  origin             text   not null               -- tracker | pending | own
                     check (origin in ('tracker','pending','own')),
  name               text   not null,              -- '1.8.0'
  description        text,
  status             text   not null,              -- open | locked | closed
  due_date           date,
  external_updated_on timestamptz,                 -- pour la synchro incrémentale
  synced_at          timestamptz not null,
  check ((tool = '') = (external_id is null))
);
create unique index on versions (project_id, tool, external_id) where external_id is not null;

create table issues (
  id                 bigserial primary key,
  project_id         bigint not null references projects(id) on delete cascade,
  tool               text not null default '',
  external_id        text not null,
  version_id         bigint references versions(id) on delete set null,
  tracker            text not null,            -- 'Evolution', 'Anomalie', ...
  subject            text not null,
  status_name        text not null,
  status_bucket      text not null             -- projection configurable
                     check (status_bucket in ('todo','doing','done','rejected')),
  priority           text,
  assignee           text,
  done_ratio         int  not null default 0,
  is_closed          boolean not null default false,
  external_updated_on timestamptz,
  synced_at          timestamptz not null
);
create unique index on issues (project_id, tool, external_id);
create index on issues (version_id, status_bucket);
create index on issues (version_id, tracker) where is_closed = false;

-- Projection statut Redmine → bucket. Éditable sans redéploiement.
create table issue_status_mapping (
  status_name text primary key,
  bucket      text not null check (bucket in ('todo','doing','done','rejected'))
);
```

Le mapping est une table et non une constante : les statuts Redmine sont propres au
workflow du client et changent sans prévenir. Un statut inconnu tombe dans `todo` et
génère un avertissement visible dans l'interface d'administration, plutôt que d'être
silencieusement ignoré.

### Releases et déploiements

```sql
create table releases (
  id            bigserial primary key,
  version_id    bigint not null references versions(id),
  tag           text   not null unique,        -- identité de la release : '1.8.0-rc2', '1.8.0'
  kind          text   not null                -- pre|final|patch
                check (kind in ('pre','final','patch')),
  state         text   not null,
  git_ref       text   not null,               -- sha du commit taggé
  git_branch    text,
  -- Une release finale réutilise l'image de la RC validée (re-tag, cf. B17).
  -- image_version = ce que version.txt contient réellement, donc ce que PortalData
  -- rapportera. Égal à `tag` pour une release construite.
  image_version     text   not null,
  source_release_id bigint references releases(id),  -- la RC dont l'image est reprise
  forge_release_url text,
  notes_md      text,                          -- note de release générée puis éditable
  quality_score int,                           -- null tant que non calculable
  created_by    text not null,                 -- subject OIDC
  released_at   timestamptz
);
create index on releases (version_id, created_at desc);
create index on releases (image_version);

create table release_images (
  release_id bigint not null references releases(id) on delete cascade,
  name       text   not null,                  -- 'portal', 'portal-cli'
  repository text   not null,                  -- 'reg.cadoles.com/cnous-mse/portal'
  tag        text   not null,                  -- tag de version, SANS métadonnée
  digest     text   not null,                  -- identité stable (cf. B8)
  size_bytes bigint,
  primary key (release_id, name)
);
create index on release_images (digest);

-- Tags de promotion : même digest, un tag par environnement atteint.
-- Promouvoir ne reconstruit pas l'image, cela ajoute un tag (cf. B7).
create table image_promotions (
  id             bigserial primary key,
  release_id     bigint not null references releases(id) on delete cascade,
  environment_id text   not null references environments(id),
  image_name     text   not null,
  tag            text   not null,              -- ex. '1.8.0-rc1_testing'
  digest         text   not null,
  pushed_at      timestamptz not null,
  unique (release_id, environment_id, image_name)
);

-- Une « promotion » : le dashboard re-tague, Flux déploie. L'opération est
-- asynchrone et le dashboard la suit sans la piloter (cf. B7).
--
-- Table retirée par la migration 0050 : elle n'a jamais été alimentée. Le suivi
-- s'est fait par `image_promotions` (ce qui est demandé) et
-- `environment_observations` (ce qui est constaté) — voir doc 16 §16.1.
create table deployments (
  id             bigserial primary key,
  environment_id text   not null references environments(id),
  release_id     bigint not null references releases(id),
  state          text   not null,
  job_id         bigint references jobs(id),   -- run retag-image
  requested_at   timestamptz not null,
  requested_by   text not null,
  observed_at    timestamptz,                  -- version constatée sur l'environnement
  superseded_at  timestamptz,                  -- null = promotion courante
  failure_reason text
);
create unique index on deployments (environment_id)
  where superseded_at is null;
```

L'index unique partiel garantit structurellement qu'un environnement n'a qu'une
promotion courante — la contrainte est dans le schéma, pas dans le code applicatif.

États d'une promotion, reflétant la chaîne `retag-image → registre → Flux → rollout →
migrations` :

```
requested ──▶ tagged ──▶ reconciling ──▶ rolling-out ──▶ migrating ──▶ live
    │           │             │               │              │
    └───────────┴─────────────┴───────────────┴──────────────┴──▶ failed
```

- `requested` — job `retag-image` en file.
- `tagged` — images re-taguées et poussées. **Fin du périmètre d'action du dashboard.**
- `reconciling` — Flux a détecté le changement.
- `rolling-out` — les pods se remplacent.
- `migrating` — pods à jour, `PortalData` signale les migrations en cours.
- `live` — `PortalData` annonce la version attendue, migrations terminées.

Les états après `tagged` sont **constatés**, pas commandés.

Un environnement non observable (`environments.observable = false`, cas de la production)
progresse jusqu'à **`reconciling`** — le commit de Flux dans `mse-kustom` est lisible sans
accès au cluster ([B8](02-blocages-et-decisions.md#b8)) — puis s'arrête là. Les états
`rolling-out`, `migrating` et `live` exigent `PortalData` et le statut `Kustomization`,
donc un accès au cluster cible. L'interface le signale explicitement plutôt que de
laisser une promotion paraître bloquée en cours de route.

```sql
-- Ce que l'environnement dit réellement, distinct de ce que le dashboard a promu (B8)
create table environment_observations (
  environment_id  text primary key references environments(id),
  observed_at     timestamptz not null,
  -- Preuve d'exécution : le CRD PortalData, alimenté par le job de migration.
  -- Porte la version de l'IMAGE, à rapprocher de releases.image_version (cf. B17).
  portal_version  text,                        -- version.txt bakée dans l'image
  migration_state text,                        -- statut rapporté par PortalData
  -- Recoupement : ce que le cluster exécute réellement
  images          jsonb,                       -- [{workload, container, image, digest}]
  -- Recoupement : ce que l'outil de déploiement DÉCLARE avoir appliqué. Une
  -- intention, jamais un constat — d'où le nom, repris du contrat de
  -- fournisseur (`ResolutionDeclared`) plutôt que de l'outil (migration 0032).
  declared_state  text,
  -- Recoupement : ce que Flux a commité dans mse-kustom (lisible sans le cluster)
  git_tag         text,
  git_commit      text,
  git_committed_at timestamptz,
  resolved_release_id bigint references releases(id),
  resolution      text not null                -- 'portaldata'|'digest'|'git'|'unobservable'
);
```

`resolution` dit **d'où vient l'information**, ce qui n'est pas un détail : `portaldata`
atteste qu'une version tourne et que ses migrations ont abouti, `git` atteste seulement
que Flux a retenu ce tag et l'a commité. C'est la meilleure source disponible pour la
production ([B8](02-blocages-et-decisions.md#b8)), mais l'interface doit la présenter
pour ce qu'elle est.

**Ordre de résolution.** L'identité de la release déployée vient du **tag promu**
(`git_tag`, ou à défaut le tag présent au registre) : le digest est partagé entre une RC
et la release finale qui la re-tague, il ne suffit donc pas à trancher
([B17](02-blocages-et-decisions.md#b17)). `PortalData` sert ensuite de **confirmation**,
par la comparaison :

```
portal_version  ==  release.image_version
```

Une égalité confirme que l'image attendue tourne. Une inégalité est une vraie divergence —
image non remplacée, rollback, déploiement manuel — et non l'artefact du re-tag, que
`image_version` absorbe.

Les trois champs `git_*` sont renseignés pour **tous** les environnements, pas seulement
la production : l'écart entre `git_committed_at` et l'état de `portal_version` est ce qui
révèle un déploiement bloqué.

### File d'exécution et runs

```sql
create table jobs (
  id            bigserial primary key,
  kind          text   not null,               -- clé du catalogue de pipelines
  queue_class   text,                          -- historique : classe du modèle précédent
  priority      int    not null default 100,   -- plus petit = plus prioritaire
  state         text   not null,
  params        jsonb  not null,               -- paramètres du PipelineRun
  release_id    bigint references releases(id) on delete set null,
  environment_id text  references environments(id),
  requested_by  text   not null,
  requested_at  timestamptz not null default now(),
  started_at    timestamptz,
  finished_at   timestamptz,
  attempt       int    not null default 1,
  queue_reason  text,                          -- 'class-busy'|'capacity'|'draining'
  cancel_reason text,
  error         text
);
create index on jobs (state, kind, priority, requested_at)
  where state = 'queued';

create table runs (
  id                bigserial primary key,
  job_id            bigint references jobs(id) on delete set null,
  pipeline_name     text not null,
  pipelinerun_name  text not null unique,      -- nom réel dans le cluster
  namespace         text not null,
  status            text not null,             -- Running|Succeeded|Failed|Cancelled|Unknown
  reason            text,
  started_at        timestamptz,
  completed_at      timestamptz,
  results           jsonb,                     -- results Tekton du PipelineRun
  observed          boolean not null default false  -- true = non déclenché par le dashboard
);

create table task_runs (
  id            bigserial primary key,
  run_id        bigint not null references runs(id) on delete cascade,
  name          text not null,                 -- nom de la task dans le pipeline
  display_name  text,
  status        text not null,
  started_at    timestamptz,
  completed_at  timestamptz,
  pod_name      text,
  log_object    text,                          -- clé MinIO du log scellé
  log_sealed    boolean not null default false,
  unique (run_id, name)
);
```

### Résultats de tests

```sql
create table test_results (
  id          bigserial primary key,
  run_id      bigint not null references runs(id) on delete cascade,
  release_id  bigint references releases(id) on delete set null,
  family      text not null                    -- unit|integration|e2e|load|static|security
              check (family in ('unit','integration','e2e','load','static','security')),
  tool        text not null,                   -- phpunit|phpstan|cs-fixer|cypress|k6|...
  status      text not null                    -- pass|warn|fail|skip|na
              check (status in ('pass','warn','fail','skip','na')),
  total       int, passed int, failed int, skipped int, flaky int,
  duration_ms bigint,
  summary     text,                            -- ligne lisible, ex. 'OK (1 301 tests)'
  raw_object  text,                            -- clé MinIO de l'artefact brut
  parsed      jsonb,                           -- détail structuré (cas de test, findings)
  parsed_at   timestamptz,
  -- Conditions de mesure. Renseignées pour family = 'load' (cf. B16) : le cluster
  -- Tekton héberge aussi la cible de charge, donc un job concurrent fausse la mesure.
  exclusive        boolean,                    -- le run tournait-il seul ?
  concurrent_jobs  int                         -- jobs constatés au démarrage
);
create index on test_results (release_id, family);

-- Cas de test individuels : extraits de `parsed` uniquement pour les familles
-- volumineuses, afin de permettre la recherche et l'historique par test.
create table test_cases (
  id             bigserial primary key,
  test_result_id bigint not null references test_results(id) on delete cascade,
  suite          text not null,
  name           text not null,
  status         text not null,                -- passed|failed|skipped|flaky
  duration_ms    int,
  failure_message text,
  failure_trace   text
);
create index on test_cases (test_result_id, status);
create index on test_cases (suite, name);
```

`test_cases` permet de répondre à « ce test échoue-t-il depuis longtemps ? », question
qui revient systématiquement en recette et qu'un simple stockage JSONB rendrait
coûteuse.

### Héritage des résultats par une release finale

Une release finale issue d'un re-tag n'est pas construite : **aucun run ne s'exécute sur
elle**, donc aucune ligne de `test_results` ne lui est rattachée. Une lecture naïve
afficherait une section qualité vide sur exactement la release qui part en production.

Les résultats sont donc lus en remontant `source_release_id`, ce qui est rigoureusement
exact — c'est le même artefact. La requête de lecture résout la chaîne :

```sql
-- résultats effectifs d'une release, en suivant l'héritage
with resolved as (
  select coalesce(source_release_id, id) as effective_id, id
  from releases where tag = $1
)
select tr.*, (r.effective_id <> r.id) as inherited
from test_results tr join resolved r on tr.release_id = r.effective_id;
```

Le drapeau `inherited` remonte jusqu'à l'interface, qui affiche « résultats hérités de
`1.8.0-rc3` » plutôt que de les présenter comme siens. Le `quality_score` suit la même
règle.

Conséquence à traiter comme un garde-fou et non comme un détail : **créer une release
finale depuis une RC dont les tests ne sont pas au vert est signalé à l'utilisateur**. Le
re-tag transporte la validation, donc aussi son absence.

### Actions manuelles, exports, audit

```sql
-- Actions manuelles : champ texte libre, pas de checklist structurée.
-- Renseigné au fil de la production du livrable, repris dans le bon de livraison.
alter table releases
  add column manual_actions_md text,
  add column manual_actions_updated_by text,
  add column manual_actions_updated_at timestamptz;

create table artifacts (
  id          bigserial primary key,
  release_id  bigint references releases(id) on delete cascade,
  kind        text not null,                   -- 'delivery-note'|'test-report'|'security-report'
  format      text not null,                   -- 'pdf'|'html'|'json'
  object      text not null,                   -- clé MinIO
  size_bytes  bigint,
  generated_by text not null,
  generated_at timestamptz not null default now()
);

create table audit_log (
  id         bigserial primary key,
  actor      text not null,
  action     text not null,                    -- 'release.create', 'job.enqueue', ...
  target     text,
  payload    jsonb,
  at         timestamptz not null default now()
);
create index on audit_log (at desc);
create index on audit_log (target, at desc);
```

L'audit n'est pas optionnel : le dashboard déclenche des déploiements en production. La
question « qui a déployé quoi, quand » doit avoir une réponse sans consulter les logs
applicatifs.

### Validation de recette

```sql
create table release_signoffs (
  id             bigserial primary key,
  release_id     bigint not null references releases(id) on delete cascade,
  environment_id text not null references environments(id),
  role           text not null,                 -- 'Client', 'Recette', 'Exploitation'
  status         text not null check (status in ('pass','warn','fail')),
  detail         text,                           -- réserve ou motif de refus
  decided_by     text not null,
  decided_at     timestamptz not null default now(),
  unique (release_id, environment_id, role)
);
create index on release_signoffs (release_id, environment_id);
```

**La clé est composée, et c'est le point qui compte.** La maquette portait
`validation[]` sur l'environnement seul ; ses données étant figées, la distinction ne
pouvait pas se voir. Une validation porte sur « cette release, dans cet environnement » :
attachée à l'environnement, elle survivrait au déploiement suivant et afficherait
« validé par le client » pour une version que le client n'a jamais vue.

**`na` n'est pas stocké.** L'attente est l'absence de ligne, pas une ligne qui dit « en
attente » — l'enregistrer obligerait à créer une entrée par intervenant dès la release,
sans savoir qui doit se prononcer.

**Le motif obligatoire pour `warn` et `fail` est porté par l'API, pas par la base.** Un
refus sans motif n'aide personne, mais le rejeter en base rendrait les données déjà
écrites irréparables si la règle changeait.

Se prononcer à nouveau remplace son propre avis, ce qui est le comportement attendu d'un
changement d'avis après correction. L'historique reste dans `audit_log`
(`release.signoff`, `release.signoff.clear`) : c'est lui, et non cette table, qui répond
à « qui avait validé, avant de se rétracter ».

La moitié automatique du sign-off — les contrôles CI décrits en
[doc 01 §1.2](01-analyse-existant.md) — n'a pas de table : elle se dérive des
`test_results` déjà enregistrés. La concaténation des deux moitiés se fait à l'affichage,
là où les deux sont présentes.

### Cache des appels externes

```sql
create table sync_state (
  source     text primary key,                 -- 'redmine.versions', 'gitea.tags', ...
  cursor     text,                             -- dernier updated_on, ETag, page
  last_run   timestamptz,
  last_ok    timestamptz,
  last_error text
);
```

`last_ok` distinct de `last_run` : l'interface doit pouvoir afficher « données Redmine
datant de 14 minutes, dernière synchronisation en échec » plutôt qu'un chiffre muet.

## 4.4 Arborescence MinIO

Bucket `arkhitekton`, distinct du bucket `mse-pipeline` utilisé par les pipelines
autonomes (CronJobs, `sec-normalize`).

```
raw/<pipelinerun-name>/<artefact>                        # écrits par les pipelines
raw/<pipelinerun-name>/meta/<outil>.json                 #   via URL pré-signée
raw/<pipelinerun-name>/e2e/videos/<nom>.mp4
logs/<pipelinerun-name>/<taskrun-name>/<step>.log        # collectés en direct
artifacts/releases/<release-tag>/delivery-note.pdf
artifacts/releases/<release-tag>/test-report.pdf
artifacts/releases/<release-tag>/security-report.json
```

Le layout de `raw/` est **décidé par le dashboard**, qui calcule chaque clé avant de
créer le `PipelineRun` et fournit l'URL pré-signée correspondante
([B14](02-blocages-et-decisions.md#b14)). Aucune recherche par préfixe, aucune
découverte : le dashboard sait exactement où chaque artefact atterrit, parce que c'est
lui qui l'a décidé.

Deux rétentions distinctes : `raw/` et `logs/` sont purgeables une fois la release
close ; `artifacts/` porte les preuves attachées à une livraison et doit survivre.

Le dashboard lit également `mse-pipeline` en lecture seule, pour les runs déclenchés
hors dashboard (CronJobs nocturnes) qui continuent d'y publier.

Politique de rétention proposée, à valider :

| Contenu | Rétention |
|---|---|
| Logs de runs sans release rattachée | 30 jours |
| Logs de runs d'une release | durée de vie de la release |
| Artefacts d'une release `released` | illimitée, ou selon obligation contractuelle |
| Vidéos E2E | 14 jours, sauf sur un run en échec rattaché à une release |

## 4.5 Calcul de la complétion d'une version

```
complétion = (Σ demandes en 'done') / (Σ demandes hors 'rejected')
```

Deux affichages complémentaires, car ils ne disent pas la même chose :

- **par nombre de demandes** — lisible, mais une grosse demande vaut une petite ;
- **pondérée par `done_ratio`** — plus fine, mais dépend de la rigueur de saisie dans
  Redmine.

L'interface montre la première et propose la seconde en survol. Les anomalies
(`tracker = 'Anomalie'`) sont comptées séparément et **n'entrent pas** dans le taux de
complétion : une version peut être fonctionnellement complète et porter des anomalies
ouvertes, ce sont deux signaux distincts qui doivent le rester.

## Amorçage des données

Deux jeux de données naissaient d'une variable d'environnement relue à chaque
fois : les administrateurs, testés à chaque connexion contre `BOOTSTRAP_ADMINS`,
et les dépôts pilotés, redéclarés à chaque démarrage de l'api.

Relire indéfiniment une configuration d'amorçage a deux défauts. La base cesse
d'être la source — retirer un dépôt de l'interface le voyait revenir au
redémarrage suivant, sans que rien à l'écran ne l'explique. Et la configuration
devient un **pouvoir permanent** : qui peut l'éditer peut se redonner un rôle
administrateur, indéfiniment, sans laisser d'autre trace qu'une connexion.

L'amorçage a donc lieu une fois, dans le Job de migration, juste après le schéma
qu'il alimente.

```sql
bootstrap_steps (step pk, applied_at, detail)
```

Une table plutôt qu'un drapeau unique : les étapes s'ajoutent avec le temps, et
une base déjà amorcée doit pouvoir en recevoir une nouvelle sans rejouer les
précédentes. Le marquage est fait **dans la même transaction** que l'étape — les
séparer laisserait une fenêtre où l'étape a eu lieu sans être marquée, et un
second démarrage la rejouerait. La réservation passe par `insert … on conflict do
nothing` : deux Jobs de migration lancés en parallèle, ce que Kubernetes fait
volontiers après un échec, ne doivent pas appliquer deux fois la même étape.

### Rôles déclarés avant leur titulaire

```sql
bootstrap_grants (identifier pk, role, claimed_at, claimed_by, created_at)
```

`user_roles` est indexée par `subject`, l'identifiant que le fournisseur met dans
le jeton — un nombre, chez Gitea. Il n'est connaissable qu'après la première
connexion : une ligne écrite à l'avance viserait un sujet inexistant, et
l'utilisateur réel se connecterait sans aucune permission. **Le projet l'a fait,
et l'a retiré.**

Cette table porte donc l'intention — « ce compte-là doit être `releaser` » — et la
connexion la transforme en rôle. `identifier` accepte le `sub` ou le nom de
compte ; le premier l'emporte en cas d'homonymie, étant immuable.

Une déclaration se réclame **une fois**. Consommée, elle ne repromeut plus
personne : c'est exactement la différence avec la configuration relue à chaque
connexion, et ce qui fait qu'un compte rétrogradé volontairement le reste. Elle
est conservée plutôt que supprimée — « ce rôle a été réclamé par ce sujet, ce
jour-là » est ce qu'on cherche quand on se demande d'où vient une permission.

Les déclarations non réclamées sont journalisées au démarrage de l'api : un
identifiant mal orthographié n'a aucun autre symptôme que « cet utilisateur n'a
aucune permission ».
