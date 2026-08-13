# 01 — Analyse de l'existant

## 1.1 Le dépôt `mse-tekton`

Déploiement kustomize (`base/` + `overlays/local|prod`), namespace cible
`mse-tekton-pipelines`, namespace applicatif de test `mse-pipeline`.

### Pipelines disponibles

| Pipeline | Rôle | Déclencheur actuel | Paramètres notables |
|---|---|---|---|
| `php-static-tests` | cs-fixer, PHPStan, tests unitaires | webhook Gitea `push` **ou** en-tête `Tekton-Event: test-static` | `repo-url`, `revision`, `commit`, `minio-*` |
| `php-integration-test` | build + déploiement éphémère dans `mse-pipeline` + PHPUnit `--testsuite integration` | `Tekton-Event: test-integration`, CronJob nocturne | `mse-revision`, `mse-kustom-revision`, `tag-name`, `integration-timeout` |
| `e2e-test` | Cypress sur un environnement déployé | `Tekton-Event: test-e2e`, CronJob `0 3 * * *` | `repo-url` (**GitLab**, pas Gitea), `revision`, `environment` |
| `security-audit` | Trivy (image/fs/config), Semgrep, Gitleaks, composer audit, puis normalisation | `Tekton-Event: security-audit`, CronJob | `app-revision`, `mse-kustom-revision`, `tekton-revision` |
| `build-image` | buildx des images `portal` et `portal-consumer`, push registre | `Tekton-Event: build` | `revision`, `tag-name`, `reg-project` |
| `promote-image` | re-tag des deux images via `crane tag` | `Tekton-Event: promote` | `old-tag`, `new-tag` |
| `deploy-mse` | clone `mse-kustom` puis `tekton-mse-manager deploy` | **aucun** — pipeline sans Trigger | `namespace`, `replace-image-mapping`, `overlay-location`, `reset-data` |

Tous les Triggers sont agrégés par l'`EventListener` `mse-listener`, exposé en interne
sur `el-mse-listener:8080/trigger/mse`, filtrés par interceptor CEL sur l'en-tête
`Tekton-Event`.

### La brique `tekton-mse-manager`

Binaire Go (`images/tekton-mse-manager`) déjà utilisé par plusieurs Tasks. Commandes :
`deploy` (kustomize + remplacement d'images + stratégie d'apply + allowlist de
suppression de ressources immuables), `integration-test` (rollout, exec dans le pod,
retry sur OOM/exit 137, extraction du résumé PHPUnit, upload MinIO), `reset`,
`minio-cleanup`, `security-normalize`.

C'est un actif important : la logique de déploiement et d'exécution dans le cluster
existe déjà et n'a pas à être réécrite dans le dashboard. Le dashboard l'appelle
indirectement en déclenchant les pipelines qui l'utilisent.

### Le format de rapport de sécurité

Le pipeline le plus récent, et le seul manifestement conçu avec une interface en tête.
Il fait trois choses distinctes — exécuter les scanners, publier sur MinIO, normaliser —
dont la troisième est déplacée côté dashboard
([B14](02-blocages-et-decisions.md#b14)). Ce qui suit décrit l'existant ; ce qui en est
conservé est indiqué en fin de section.

`pkg/security` définit un contrat explicite, documenté dans le code comme tel
(« Ils forment le contrat avec toute interface qui lira ces runs ») :

```
<bucket>/security-audit/runs/<runID>/findings.jsonl
<bucket>/security-audit/runs/<runID>/summary.json
<bucket>/security-audit/runs/<runID>/manifest.json
<bucket>/security-audit/latest.json
```

avec `runID` au format `2006-01-02T15-04-05Z`, trié lexicographiquement comme
chronologiquement — ce qui permet de retrouver le run précédent par simple tri de
préfixes MinIO.

Les structures sont stables et directement consommables :

- `Finding` — `fingerprint`, `run_id`, `target{kind,name,revision}`, `tool`,
  `category` (`sca|sast|secret|iac|image`), `rule_id`, `severity`
  (`CRITICAL|HIGH|MEDIUM|LOW|INFO`), `cvss`, `cwe`, `location`, `package`, `fixable`,
  `status` (`new|existing|resolved`), `first_seen`, `suppression`.
- `Summary` — `total`, `by_severity`, `by_category`, `by_target`, `delta`,
  `suppressed`, `policy` (verdict `PASS`/`FAIL` par environnement), `scanner_status`,
  `failed_scanners`.
- `Manifest` — `run_id`, `pipeline_run_name`, `previous_run_id`, `scanners[]`.

**Ce qui est conservé.** Le descripteur `ScannerMeta` reste produit par le pipeline :
lui seul peut distinguer « scanner en erreur » de « scanner à zéro finding », et cette
distinction est inobservable depuis l'artefact brut. Restent également le principe
« un finding sous exception est listé mais hors agrégat » et celui du non-blocage — la
décision de release est humaine, et c'est précisément le rôle du dashboard de la porter.

**Ce qui est déplacé.** La normalisation elle-même — dedup, empreintes, application des
exceptions, comparaison au run précédent, verdict de politique. `SecurityNormalize`
appelle `loadPreviousRun` puis `ApplyDiff` : le calcul cross-run se fait donc sans
transaction, sur un `latest.json` que deux runs concurrents peuvent écraser, et les
exceptions sont figées à l'écriture. Le code lui-même est bon et testé ; il est extrait
en module partagé et exécuté côté dashboard ([B14](02-blocages-et-decisions.md#b14),
[P7](10-prerequis-mse-tekton.md)).

### Comportement des autres familles de tests

| Tâche | Sortie produite | Destination MinIO |
|---|---|---|
| `unit-test` | `phpunit-logs` — texte brut du reporter par défaut | `/static-tests/phpunit` |
| `phpstan` | `phpstan-logs` — table ASCII | `/static-tests/phpstan` |
| `cs-fixer` | `cs-fixer-logs` — diff textuel | `/static-tests/csfixer` |
| `cypress` | vidéos, captures, rapports Mocha **HTML** | `/e2e/videos`, `/e2e/screenshots`, `/e2e/reports` |
| `integration-test` | `phpunit.log` + résumé une ligne en result Tekton | `/integration-tests/phpunit` |

Aucun format machine. Le seul élément structuré est le `summary` PHPUnit d'intégration,
extrait par regex par `tekton-mse-manager` et publié en `results.summary`, consommé
ensuite par la notification Rocket.Chat.

## 1.2 La maquette

Application Vue 3 + Vite + Pinia + vue-router + Tailwind, servie sous `/dashboard/`,
alimentée par un jeu de données statique. Deux routes : `/env/:env` et `/history/:env`,
avec `env ∈ {unstable, qualif, preprod, prod}` et un basculement global
« Synthèse » / « Vue détaillée ».

### Modèle de données implicite

```
data = {
  releases: [ Release ],
  environments: { <envId>: Environment }
}

Release {
  version, name,
  meta { commit, branch, duration, runId, runUrl },
  score: 0..100, status: healthy|warn|critical,
  pipelines: [ { key, name, status: pass|warn|fail|skip|na, duration, value?, history?, lowerBetter? } ],
  perf:      [ { label, value, unit, baseline, history, lowerBetter, vus, total?, totalBaseline? } ],
  manualBenchs: [ ... ],
  security:  { status, summary, advisories: [ { pkg, sev, id, fixed, url } ] },
  notes:     { breaking[], features[], fixes[] }  // { scope, text, pr, prUrl, ticket, ticketUrl }
  issues:    [ { title, severity, workaround, eta, ticket, ticketUrl } ],
  checks:    [ { role, detail, status } ]
}

Environment { id, label, version, deployedAt, lastUpdate, validation[], history[] }
```

La vue d'un environnement est une **jointure** : l'environnement porte un numéro de
version, la release correspondante porte tout le contenu. `signoff` est la concaténation
de `release.checks` (automatique, « CI / Pipelines ») et `environment.validation`
(humain, « Client »).

### Composants

`EnvHeader`, `PromotionFlow` (fil unstable → qualif → preprod → prod), `HealthGauge`,
`Sparkline`, `SignalGrid` (Qualité du code / Tests / e2e / Bench tests / Sécurité),
`SignalDetail`, `PipelineList` + `PipelineStepDetail`, `SecurityPanel`, `BenchPanel`,
`ReleaseNotes` (Changements cassants / Nouveautés / Corrections), `KnownIssues`,
`SignoffPanel`, `ReleaseHistory`, `ToasterPanel`, `CollapsibleSection`, `ThemeToggle`.

### Sémantique des statuts, à reprendre telle quelle

- Santé depuis le score : `>= 85` → `healthy`, `>= 70` → `warn`, sinon `critical`.
- Verdict de bench : ratio mesure/baseline (inversé si `lowerBetter`), `>= 1` → `pass`,
  `>= 0.9` → `warn`, sinon `fail` ; le verdict sur le volume total peut dégrader le
  verdict sur le débit, jamais l'améliorer.
- Cinq échelles de libellés distinctes (santé, pipeline, bench, sign-off, sévérité),
  chacune avec son vocabulaire français.

## 1.3 Écarts entre la demande, la maquette et l'existant

| Sujet | Demande | Maquette | `mse-tekton` | Statut |
|---|---|---|---|---|
| Environnements | unstable, testing, preprod, prod | unstable, **qualif**, preprod, prod | `mse-pipeline` (éphémère), param `environment` défaut `dev` | **tranché** — `qualif` retenu, `testing` = alias, `dev` hors périmètre |
| Frontend | React | **Vue 3** | — | **tranché** — React, maquette portée |
| Tests de charge | demandés | affichés (`BenchPanel`, `perf`) | **inexistants** | outil = k6, runner hors cluster à l'étude *(B16)* |
| Release / pré-release | pilotée par le dashboard | version affichée | `promote-image` seul ; **release Gitea manuelle** | à construire *(B3)* |
| Périmètre de la maquette | pilotage + consultation | **consultation seule** | — | pilotage entièrement à concevoir |
| Anomalies Redmine | demandées | `issues[]` en dur | — | à construire |
| Actions manuelles | demandées | absentes de la maquette | — | **tranché** — champ texte libre éditable |
| Note de release | demandée | `notes{}` en dur | — | à construire |
| Bon de livraison | demandé | absent | — | à construire |
| Score de qualité | implicite | `score` en dur | — | **règle de calcul à valider** *(B11)* |

Ces écarts ne sont pas des erreurs de la maquette : elle décrit la cible de la partie
consultation, et le client confirme que le pilotage reste entièrement à concevoir. Ils
délimitent le périmètre de construction réel.
