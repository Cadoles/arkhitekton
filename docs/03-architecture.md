# 03 — Architecture

## 3.1 Vue d'ensemble

```
                    ┌──────────────────────────────────────────┐
   Navigateur ──────▶  Ingress  /  → SPA React (statique)      │
                    │            /api → service api            │
                    └──────────────────────────────────────────┘
                                    │
             ┌──────────────────────┴───────────────────────┐
             ▼                                              ▼
   ┌───────────────────┐                        ┌────────────────────────┐
   │  mse-dashboard    │   NOTIFY / LISTEN      │   mse-dashboard        │
   │  api  (N replicas)│◀──────────────────────▶│   worker (1, leader)   │
   │  - REST + SSE     │      via PostgreSQL    │   - scheduler de file  │
   │  - session OIDC   │                        │   - informer Tekton    │
   │  - autorisation   │                        │   - collecteur de logs │
   └───────────────────┘                        │   - synchros externes  │
             │                                  └────────────────────────┘
             │                                              │
     ┌───────┴──────────┬───────────────┬───────────────────┴────┐
     ▼                  ▼               ▼                        ▼
┌──────────┐      ┌──────────┐   ┌─────────────┐        ┌────────────────┐
│PostgreSQL│      │  MinIO   │   │ API Kube    │        │ APIs externes  │
│ état,    │      │ logs,    │   │ Tekton CRDs │        │ Redmine, Gitea,│
│ file,    │      │ artefacts│   │ Deployments │        │ GitLab, Harbor │
│ cache    │      │ rapports │   │ des envs    │        └────────────────┘
└──────────┘      └──────────┘   └─────────────┘
```

## 3.2 Pourquoi deux rôles et un seul binaire

Le service `api` doit pouvoir être répliqué : c'est lui qui encaisse le trafic
utilisateur et les flux SSE. Le service `worker` doit être **unique** : un ordonnanceur
de file dupliqué lance deux fois le même pipeline, et deux collecteurs de logs écrivent
sur le même objet MinIO.

Les séparer en deux dépôts ou deux modules Go ferait payer une duplication du modèle de
domaine pour un bénéfice nul. Un binaire, deux sous-commandes :

```
mse-dashboard api      # serveur HTTP
mse-dashboard worker   # boucles de fond, avec élection de leader
mse-dashboard migrate  # migrations de schéma, exécutées en Job d'init
```

Le `worker` tourne en `replicas: 1` mais acquiert malgré tout un `coordination.k8s.io/Lease`
avant de démarrer ses boucles : pendant un rolling update, deux pods coexistent
brièvement, et c'est exactement le moment où un double lancement se produirait.

## 3.3 Structure du monorepo

```
mse-dashboard/
├─ apps/
│  └─ web/                       # SPA React + Vite + TypeScript
│     ├─ src/
│     │  ├─ features/            # découpage par domaine, pas par type de fichier
│     │  │  ├─ environments/
│     │  │  ├─ versions/
│     │  │  ├─ releases/
│     │  │  ├─ runs/
│     │  │  └─ reports/
│     │  ├─ components/          # primitives partagées (Badge, Gauge, Sparkline…)
│     │  ├─ lib/                 # client API généré, statuts, formats
│     │  └─ routes/
│     └─ vite.config.ts
├─ cmd/
│  └─ mse-dashboard/main.go
├─ internal/
│  ├─ config/                    # chargement conf + validation au démarrage
│  ├─ domain/                    # entités et règles métier, sans dépendance I/O
│  │  ├─ version/                # complétion d'une version, agrégats Redmine
│  │  ├─ release/                # cycle de vie, transitions autorisées
│  │  ├─ quality/                # calcul du score, verdicts
│  │  └─ queue/                  # sémantique de file et classes d'exclusion
│  ├─ store/                     # PostgreSQL (pgx + sqlc), migrations
│  ├─ blob/                      # MinIO
│  ├─ tekton/                    # clientset, catalogue de pipelines, informer
│  ├─ forge/                     # interface + implémentations Gitea / GitLab
│  ├─ redmine/
│  ├─ registry/                  # Harbor / OCI distribution
│  ├─ parsers/                   # JUnit, PHPStan, cs-fixer, mochawesome
│  │                             #   + module partagé extrait de tekton-mse-manager
│  │                             #     (SARIF, composer-audit, dedup, fingerprint,
│  │                             #      severity, exceptions) — cf. B14 / P7
│  ├─ report/                    # génération PDF (bon de livraison, rapports)
│  ├─ auth/                      # OIDC, sessions, autorisation
│  ├─ http/                      # routes, handlers, middlewares, SSE
│  └─ worker/                    # scheduler, collecteur de logs, synchros
├─ deploy/
│  ├─ base/                      # manifests kustomize
│  └─ overlays/{local,preprod,prod}
├─ hack/
│  └─ fluxsim/                   # simulateur Flux, développement uniquement (doc 11)
├─ docs/
├─ api/openapi.yaml              # contrat, source du client TS et des types Go
├─ Makefile
└─ go.mod
```

Une seule règle de dépendance : `internal/domain` ne référence ni base de données, ni
HTTP, ni Kubernetes. Tout le reste peut en dépendre. Cela garde testable la partie qui
concentre les décisions — calcul de complétion, score, transitions de release,
ordonnancement.

## 3.4 Choix techniques

### Backend

| Sujet | Choix | Motif |
|---|---|---|
| Routage HTTP | `chi` | stdlib-compatible, middlewares simples, pas de framework |
| PostgreSQL | `pgx/v5` + `sqlc` | requêtes SQL écrites à la main, types Go générés, pas d'ORM |
| Migrations | `goose` | fichiers SQL numérotés, exécutés par `mse-dashboard migrate` |
| Kubernetes | `client-go` + clientset `tektoncd/pipeline` | typé, informers natifs |
| OIDC | `coreos/go-oidc` + `golang.org/x/oauth2` | vérification JWKS, code flow + PKCE |
| MinIO | `minio-go/v7` | client officiel S3 |
| Journalisation | `log/slog` | stdlib, sortie JSON structurée |
| Tests | stdlib + `testcontainers-go` | PostgreSQL et MinIO réels en test d'intégration |
| Tekton en test | `envtest` (CRD Tekton) | pas de cluster complet requis |

Pas d'ORM : le modèle est majoritairement lu par jointures d'agrégats (une release avec
ses runs, ses résultats, ses demandes Redmine), un cas où un ORM produit des requêtes
que l'on finit par réécrire.

### Frontend

| Sujet | Choix |
|---|---|
| Socle | React 19 + TypeScript + Vite |
| Données serveur | TanStack Query (cache, invalidation, refetch) |
| Routage | React Router |
| Style | Tailwind CSS, palette et tokens repris de la maquette (`healthy` / `warn` / `critical`) |
| Formulaires | React Hook Form + Zod |
| Client API | généré depuis `api/openapi.yaml` (`openapi-typescript` + fetch typé) |
| Temps réel | `EventSource` sur les endpoints SSE |

Pas de gestionnaire d'état global : la quasi-totalité de l'état est de l'état serveur,
donc du ressort de TanStack Query. Le peu d'état client (environnement courant, mode
synthèse/détaillée, thème) tient dans l'URL et `localStorage`, comme dans la maquette.

## 3.5 Déploiement

Namespace dédié `mse-dashboard`, séparé de `mse-tekton-pipelines`.

| Ressource | Détail |
|---|---|
| `Deployment/api` | 2 réplicas, sonde `/healthz` et `/readyz`, `securityContext` non-root, FS racine en lecture seule |
| `Deployment/worker` | 1 réplica, `strategy: Recreate` pour éviter le chevauchement |
| `Job/migrate` | hook pré-déploiement, idempotent |
| `Service` + `Ingress` | TLS, la SPA servie par le conteneur `api` (fichiers statiques embarqués via `embed.FS`) |
| `ServiceAccount` + `Role`/`RoleBinding` | voir [07](07-securite-et-permissions.md) |
| `ExternalSecret` | identifiants Redmine, Gitea, GitLab, registre, MinIO, OIDC |
| `ConfigMap` | catalogue de pipelines, table des environnements, pondérations du score |

PostgreSQL : instance CloudNativePG (`postgresql.cnpg.io` est déjà utilisé dans le
cluster, la `Role` de `mse-tekton` en atteste), avec sauvegardes planifiées. MinIO :
bucket dédié `mse-dashboard`, distinct de `mse-pipeline` pour que les politiques de
rétention et les droits soient indépendants.

## 3.6 Séparation des responsabilités avec `mse-tekton`

Une frontière nette évite que les deux projets se marchent dessus :

| Responsabilité | Porteur |
|---|---|
| Définition des Tasks et Pipelines | `mse-tekton` |
| Exécution technique (build, tests, déploiement) | Tekton |
| Sortie native des outils + descripteur `meta` | Tasks Tekton |
| Écriture de l'artefact brut | Tasks Tekton, via URL pré-signée |
| Layout de stockage (clé de chaque objet) | dashboard |
| Décision de *quoi* lancer et *quand* | dashboard |
| File d'attente et exclusion mutuelle | dashboard |
| Parsing, normalisation, diff cross-run, score | dashboard |
| Cycle de vie d'une release | dashboard |
| Déclenchement sur push Gitea et planifications | `mse-tekton` (EventListener, CronJob) — inchangé |

Résumé de la frontière ([B14](02-blocages-et-decisions.md#b14)) : **le pipeline
constate, le dashboard interprète.**

Le dashboard ne modifie jamais les ressources de `mse-tekton` : il crée des
`PipelineRun` qui référencent des `Pipeline` existants. Si `mse-tekton` évolue, seul le
catalogue déclaratif du dashboard doit suivre.
