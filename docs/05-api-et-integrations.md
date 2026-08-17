# 05 — API et intégrations

## 5.1 API interne

Contrat décrit dans `api/openapi.yaml`, source unique du client TypeScript et des types
Go de transport. Préfixe `/api/v1`. Toutes les réponses en JSON, erreurs au format
RFC 9457 (`application/problem+json`).

### Lecture

| Méthode | Chemin | Rôle |
|---|---|---|
| `GET` | `/environments` | table des environnements + déploiement courant + statut de santé |
| `GET` | `/environments/{env}` | vue détaillée : release déployée, signaux, historique |
| `GET` | `/environments/{env}/history` | historique des déploiements |
| `GET` | `/versions` | versions Redmine, complétion, nombre d'anomalies ouvertes |
| `GET` | `/versions/{id}` | détail : demandes groupées par bucket et par tracker |
| `GET` | `/versions/{id}/issues` | demandes, filtrables par tracker et statut |
| `GET` | `/releases` | filtres `version_id`, `state`, `kind` |
| `GET` | `/releases/{tag}` | agrégat complet (le « modèle release » de la maquette) |
| `GET` | `/releases/{tag}/notes` | note de release, générée ou éditée |
| `GET` | `/releases/{tag}/images` | images et digests |
| `GET` | `/releases/{tag}/tests` | résultats par famille |
| `GET` | `/releases/{tag}/tests/{family}` | détail parsé, cas de test paginés |
| `GET` | `/releases/{tag}/security` | findings, filtrables par sévérité et catégorie |
| `GET` | `/releases/{tag}/actions` | actions manuelles (texte libre) |
| `GET` | `/deployments?environment=` | promotions et leur état de progression |
| `GET` | `/runs` | filtres `release_id`, `status`, `pipeline` |
| `GET` | `/runs/{name}` | détail : tasks, durées, results Tekton |
| `GET` | `/runs/{name}/logs?task=&step=` | log complet (depuis MinIO si scellé) |
| `GET` | `/jobs` | file d'exécution, incluant les jobs en attente |
| `GET` | `/me` | identité, groupes, permissions effectives |

### Écriture

| Méthode | Chemin | Permission |
|---|---|---|
| `POST` | `/releases` | `release:create` |
| `POST` | `/releases/{tag}/promote` | `release:promote` |
| `PATCH` | `/releases/{tag}/notes` | `release:edit` |
| `PUT` | `/releases/{tag}/actions` | `release:edit` |
| `POST` | `/jobs` | `pipeline:run` |
| `DELETE` | `/jobs/{id}` | `pipeline:cancel` |
| `POST` | `/deployments` | `deploy:{env}` |
| `POST` | `/sync/{source}` | `admin:sync` — force une resynchronisation |
| `POST` | `/releases/{tag}/artifacts/{kind}` | `release:export` — génère un export |

### Temps réel

| Chemin | Contenu |
|---|---|
| `GET /stream/runs/{name}` | SSE : changements d'état des tasks, progression |
| `GET /stream/runs/{name}/logs?task=` | SSE : lignes de log en direct |
| `GET /stream/queue` | SSE : évolution de la file |

SSE plutôt que WebSocket : le flux est unidirectionnel, la reconnexion et la reprise
(`Last-Event-ID`) sont natives, et cela traverse les proxys sans configuration
particulière. Les événements portent un identifiant monotone pour permettre au client
de reprendre après coupure sans trou.

### Idempotence

Toutes les routes d'écriture qui déclenchent une action dans le cluster acceptent un
en-tête `Idempotency-Key`. Le double-clic sur « Lancer les tests E2E » est le mode de
défaillance le plus probable de l'interface ; il ne doit pas produire deux exécutions.

## 5.2 Redmine

**Authentification.** Clé d'API d'un compte de service, en-tête `X-Redmine-API-Key`.
Le compte doit être en **lecture seule** sur le projet : le dashboard ne modifie rien
dans Redmine. Si une écriture devenait nécessaire (clôture automatique d'une version à
la livraison), ce serait une décision explicite, pas un effet de bord.

**L'instance visée est celle de production, et l'interdiction d'écrire est appliquée par
le code.** Le transport de `internal/redmine` refuse toute méthode autre que `GET` et
`HEAD` avant tout contact réseau, et rend `ErrWriteForbidden`. Le choix du transport
plutôt que d'une consigne est délibéré : une revue de code peut manquer un appel, un
`RoundTrip` ne peut pas être contourné sans réécrire la construction du client. Un test
le vérifie sur `POST`, `PUT`, `PATCH`, `DELETE` et deux méthodes exotiques, en comptant
ce qu'un serveur reçoit — zéro.

La clé est ajoutée par ce même transport, ce qui garantit qu'aucun appelant ne peut la
placer dans une chaîne de requête, où elle finirait dans les journaux d'accès du serveur
et dans l'`url.Error` de toute requête en échec. `https` est exigé hors localhost : la
clé voyage à chaque appel.

Cette garde protège d'une erreur de programmation, **pas d'un compte trop puissant**.
D'où `CheckAccess`, qui rend le drapeau `admin` du compte et journalise un avertissement
s'il est vrai : le pouvoir d'écrire existe dès que la clé le porte, même si le dashboard
ne s'en sert pas, et le prochain outil branché sur le même fichier d'environnement n'aura
pas forcément la même garde.

**Vérifier une clé sans rien écrire :**

```bash
mse-dashboard check-redmine
```

Rôle de diagnostic qui lit `/users/current.json` puis s'arrête. Il n'exige ni base, ni
stockage, ni accès au cluster — vérifier une clé doit rester possible depuis un poste qui
n'a rien d'autre.

**Endpoints utilisés.**

| Besoin | Appel |
|---|---|
| Versions | `GET /projects/{project}/versions.json` |
| Demandes d'une version | `GET /issues.json?fixed_version_id={id}&status_id=*&limit=100&offset=…` |
| Synchro incrémentale | `GET /issues.json?project_id={p}&updated_on=%3E%3D{iso}&status_id=*&sort=updated_on` |
| Anomalies ouvertes | `GET /issues.json?fixed_version_id={id}&tracker_id={anomalie}&status_id=open` |
| Référentiels | `GET /trackers.json`, `GET /issue_statuses.json` — pour alimenter le mapping |

**Points d'attention.**

- Pas de webhook natif (voir [B6](02-blocages-et-decisions.md#b6)) : synchronisation
  périodique et incrémentale.
- La pagination est en `offset`/`limit` avec `total_count` ; sur une version chargée,
  cela peut faire plusieurs pages. Le poll incrémental par `updated_on` les évite en
  régime établi.
- `fixed_version_id` peut être nul : une demande sans version ne remonte pas.
- Les identifiants de trackers et de statuts sont propres à l'instance. Ils sont
  chargés au démarrage et rafraîchis quotidiennement, jamais codés en dur.

## 5.3 Gitea

**Authentification.** Jeton d'accès personnel d'un compte de service. Portées
nécessaires : `read:user`, `read:repository`, et `write:repository` uniquement pour la
création de tags et de releases.

| Besoin | Appel |
|---|---|
| Branches, tags | `GET /repos/{owner}/{repo}/branches`, `/tags` |
| Comparaison de deux références | `GET /repos/{owner}/{repo}/compare/{base}...{head}` |
| Commits | `GET /repos/{owner}/{repo}/commits?sha=&since=` |
| Pull requests fusionnées | `GET /repos/{owner}/{repo}/pulls?state=closed&sort=recentupdate` |
| Créer un tag | `POST /repos/{owner}/{repo}/tags` |
| Créer une release | `POST /repos/{owner}/{repo}/releases` |
| Statut de commit | `POST /repos/{owner}/{repo}/statuses/{sha}` |

**Génération de la note de release.** Deux sources, de statut inégal :

1. **Référence** — les demandes Redmine rattachées à la version, groupées par tracker
   (`Evolution` → Nouveautés, `Anomalie` → Corrections), filtrées sur leur statut. C'est
   le contenu que lit le client, et le seul qui fasse foi.
2. **Recoupement** — les pull requests fusionnées entre le tag précédent et le nouveau
   (`compare/{prev}...{new}`), qui disent ce qui est réellement dans le code.

La note présente la première et signale les écarts constatés avec la seconde : une PR
fusionnée sans demande Redmine associée, une demande marquée terminée sans commit
correspondant. Ces écarts sont précisément ce qu'une revue de release doit examiner ;
les masquer viderait l'exercice de son intérêt. Ils sont présentés comme des
avertissements à instruire, pas comme du contenu.

**Hiérarchie des sources — tranchée par le client : le statut de la demande dans Redmine
reste la référence.**

Cela fixe les rôles et limite la portée de l'ambiguïté décrite ci-dessous :

| Source | Rôle |
|---|---|
| Demandes Redmine rattachées à la version | **référence** — c'est elle qui définit le contenu fonctionnel de la note |
| PR Gitea fusionnées entre deux tags | **recoupement** — sert à détecter les écarts, jamais à établir le périmètre |

**Liaison commit ↔ demande.** Les messages de commit et les titres de PR portent
l'identifiant Redmine sous la forme `#1234`.

Réserve à traiter dès l'implémentation : **Gitea utilise la même notation pour ses
propres issues et PR**. Un titre « Merge PR #45 » produit un faux rattachement à la
demande Redmine 45, qui existe très probablement. Le client n'a pas de réponse à ce jour.

Conséquence, atténuée par la hiérarchie ci-dessus : un faux positif dégrade le
recoupement — il fait apparaître un écart inexistant — mais ne peut pas introduire une
entrée fausse dans le contenu fonctionnel, qui vient de Redmine.

Trois parades, par ordre de fiabilité :

1. **Vérifier l'existence côté Redmine** avant de retenir la référence, et n'accepter
   que les demandes rattachées à la version en cours de release. Coûte un appel, élimine
   la quasi-totalité des faux positifs.
2. **Filtrer par plage d'identifiants** si les numéros Redmine et Gitea ne se recouvrent
   pas — à vérifier sur les données réelles, et susceptible de cesser d'être vrai.
3. **Signaler plutôt que deviner** : une référence ambiguë apparaît marquée comme telle,
   à confirmer à la relecture.

La première est retenue, la troisième en complément. Deviner silencieusement est le seul
comportement exclu.

**Changements cassants.** La maquette prévoit une section « Changements cassants ». Ils
ne sont pas déductibles automatiquement : ils viennent soit d'un label Gitea
(`breaking-change`), soit d'un champ personnalisé Redmine, soit d'une saisie manuelle
dans le dashboard. Les trois voies sont supportées, la saisie manuelle servant de
filet.

## 5.4 GitLab

Périmètre volontairement minimal : lister les branches et tags de
`cnous-toulouse/mse/mse-tests-e2e` pour choisir la révision des tests E2E.
Authentification par jeton de projet en lecture. Interface `ForgeClient` commune avec
Gitea (voir [B12](02-blocages-et-decisions.md#b12)).

## 5.5 Registre d'images

`reg.cadoles.com` (Harbor). Deux usages :

1. **Résolution digest → tags**, indispensable pour identifier la version déployée
   quand les environnements utilisent des tags flottants (voir
   [B8](02-blocages-et-decisions.md#b8)).
2. **Inventaire des images d'une release** : taille, date de push, digest, tags.

Via l'API OCI distribution standard (`GET /v2/{repo}/manifests/{ref}` avec l'en-tête
`Accept` adapté, `GET /v2/{repo}/tags/list`) plutôt que l'API propriétaire Harbor, pour
ne pas se lier à l'implémentation. L'API Harbor n'est utilisée que si la recherche
inverse par digest s'avère impraticable en OCI pur — auquel cas
`GET /api/v2.0/projects/{p}/repositories/{r}/artifacts?q=digest%3D{d}` répond
directement.

Compte de service en **lecture seule** : le dashboard ne pousse ni ne re-tague
lui-même. Le re-tag passe par le pipeline `promote-image`, qui a déjà les droits
nécessaires via le secret `regcred`.

## 5.6 Kubernetes

Deux périmètres distincts, avec deux niveaux de droits :

| Périmètre | Namespace | Droits |
|---|---|---|
| Pilotage Tekton | `mse-tekton-pipelines` | création/lecture/suppression de `PipelineRun`, lecture de `Pipeline`, `Task`, `TaskRun`, `Pod`, `Pod/log` |
| Observation des environnements | namespaces applicatifs | lecture seule de `Deployment`, `StatefulSet`, `Pod`, **`PortalData`**, et des ressources Flux |

Détail des `Role` en [07](07-securite-et-permissions.md).

### `PortalData` — preuve d'exécution et état des migrations

CRD introduit par une PR en cours sur `mse-kustom`
([B8](02-blocages-et-decisions.md#b8)). Alimenté par le job de migration de
l'application à partir du `version.txt` baké dans l'image. Il porte la version livrée et
le statut des migrations.

C'est la seule source qui dise si les **migrations** ont abouti — une image déployée dont
les migrations ont échoué n'est pas une version livrée. Lue par informer, comme le reste.

**Elle ne porte pas l'identité de la release, mais celle de l'image.** Une release finale
réutilise l'image de la RC validée par re-tag
([B17](02-blocages-et-decisions.md#b17)) : `PortalData` rapportera donc `1.8.0-rc3` là où
`1.8.0` est déployée. Le rapprochement se fait sur `releases.image_version`, jamais sur
`releases.tag` — cette dernière comparaison échouerait sur toute release finale.

L'identité de la release déployée vient du **tag promu**, lu dans le commit `mse-kustom`
ou à défaut au registre. `PortalData` confirme ensuite que l'image attendue tourne
réellement.

### FluxCD — état d'avancement d'une promotion

Flux déploie les environnements à partir du tag d'image
([B7](02-blocages-et-decisions.md#b7)). Le dashboard ne le pilote pas ; il lit son
statut pour distinguer « promotion demandée » de « déploiement abouti », et alimenter les
états de `deployments`.

**Sélection de l'image : par métadonnée de build SemVer.** L'environnement cible est
encodé en suffixe du tag, avec l'**underscore** comme séparateur — `+` étant exclu de la
grammaire des tags OCI :

```
1.8.0-rc1_testing     pré-release sur qualif   (Flux désigne qualif par « testing »)
3.1.0_preprod         release finale sur pré-production
```

Promouvoir consiste à re-taguer le digest existant. L'`ImagePolicy` de l'environnement
extrait la partie SemVer par `filterTags` avant d'appliquer son ordre, et sa plage
détermine si l'environnement accepte les pré-releases — `>=0.0.0-0` les inclut,
`>=0.0.0` les exclut. Voir [B7](02-blocages-et-decisions.md#b7) : cette distinction est
ce qui empêche une RC d'atteindre la production, et ce que le dashboard doit refléter
pour ne pas produire de promotion silencieusement sans effet.

Ressources observées :

| Ressource | Ce qu'elle apporte |
|---|---|
| `Kustomization` (`kustomize.toolkit.fluxcd.io`) | conditions `Ready` / `Reconciling`, `lastAppliedRevision` — signal d'aboutissement du déploiement |
| `ImagePolicy`, `ImageRepository` (`image.toolkit.fluxcd.io`) | quelle image la politique de l'environnement a retenue — distingue « Flux n'a pas encore vu le tag » de « rapprochement en cours » |

Les `ImagePolicy` constituent la **spécification exacte de ce que le dashboard doit
produire**. Elles sont lues au démarrage et confrontées à la table `environments` sur
deux points :

- le `promotion_suffix` correspond bien au `filterTags` de l'environnement ;
- `accepts_prerelease` correspond bien à la plage SemVer configurée.

Une divergence sur l'un ou l'autre bloque le démarrage. C'est délibéré : les deux
erreurs produisent le même symptôme — un tag poussé sans erreur que Flux ignore — et un
échec silencieux sur une opération de release est bien plus coûteux qu'un refus de
démarrer.

Lecture en `get`/`list`/`watch` seul : **le dashboard n'écrit jamais dans les ressources
Flux**, sous peine de créer une dérive que Flux corrigerait au rapprochement suivant.

### `mse-kustom` — la réécriture Git de Flux

L'image-automation-controller commite dans `mse-kustom` le tag retenu par l'`ImagePolicy`
de chaque environnement. Cette source a deux propriétés que les autres n'ont pas :

- **elle est lisible sans accès au cluster**, via Gitea déjà intégré ;
- **elle est datée et attribuée** : l'historique des commits de `mse-kustom` *est*
  l'historique des déploiements, sans qu'il faille le reconstituer.

Le dashboard suit les commits touchant les overlays d'environnement et en extrait le tag
appliqué. Croisé avec `PortalData`, cela détecte un déploiement bloqué — « Flux a commité
`1.8.0-rc3` il y a 12 minutes, l'application rapporte toujours `1.8.0-rc2` » — ce
qu'aucune des deux sources ne dit seule.

### Production : partiellement observable

La production tourne dans un cluster indépendant, non accessible depuis le cluster
Tekton. Ni `Deployment`, ni `PortalData`, ni le statut Flux n'y sont lisibles.

La réécriture Git ci-dessus la couvre cependant en grande partie : version retenue, date,
historique. L'environnement est marqué `observable = false`, ce qui restreint son
affichage aux états jusqu'à `tagged`/`reconciling` — l'interface indique explicitement
que l'aboutissement n'est pas constatable, plutôt que de laisser croire à un déploiement
figé. Pistes pour combler le reste en
[B8](02-blocages-et-decisions.md#b8).

Les lectures passent par des **informers** (cache local alimenté par `watch`), pas par
des `list` répétés : c'est le mécanisme prévu par l'API Kubernetes pour ce cas et il
évite de mettre l'API server sous pression. Le cache est également ce qui permet de
répondre instantanément aux requêtes de l'interface.

## 5.7 Politique générale face aux services externes

Chaque intégration respecte le même contrat :

- **Timeout explicite** sur chaque appel, jamais de valeur par défaut du client HTTP.
- **Disjoncteur** : après N échecs consécutifs, la source passe en dégradé, le
  dashboard sert ses données en cache et l'interface affiche leur âge.
- **Réessai avec temporisation exponentielle** sur les erreurs transitoires (5xx, 429),
  jamais sur les 4xx.
- **Aucun appel externe dans le chemin d'une requête utilisateur**, sauf action
  explicite de rafraîchissement. Les lectures d'interface servent la projection en
  base ; c'est ce qui rend l'interface rapide et résistante à l'indisponibilité de
  Redmine ou de Gitea.
