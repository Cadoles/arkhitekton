# 11 — Environnement de développement

## 11.1 Topologie

Tout tourne dans un cluster **kind** unique.

| Composant | Emplacement | Correspondance en production |
|---|---|---|
| Tekton | namespace `mse-tekton-pipelines` | identique |
| Application MSE | namespace **`mse-dev`** | `unstable`, `qualif`, `preprod` — et `prod`, cluster séparé |
| Dashboard | namespace `mse-dashboard` | identique |
| PostgreSQL, MinIO | dans le cluster | CloudNativePG, `nuo-s3.nuonet.fr` |
| Registre | **`reg.cadoles.com/vcarroy`** | `reg.cadoles.com/cnous-mse` |
| Flux | **simulé** — voir §11.3 | image-automation + kustomize controllers |

Le déploiement de Tekton en local est déjà outillé par `mse-tekton` : `make tekton`
puis `make pipelines`, avec `overlays/local` qui pose `TEST_NAMESPACE=mse-dev` et
`STORAGE_CLASS_NAME=standard` (kind fournit `standard`, et non `local-path` comme la
production — sans incidence, le dashboard ne lit pas les volumes).

## 11.2 Ce qui est réel, ce qui est simulé

| Élément | En local | Remarque |
|---|---|---|
| Pipelines Tekton | **réels** | mêmes `Pipeline` et `Task`, via `overlays/local` |
| Création de `PipelineRun`, file, annulation | **réels** | le cœur du lot 3 est testable sans rien simuler |
| Collecte de logs depuis les pods | **réelle** | |
| MinIO et URLs pré-signées | **réels** | mais ne valident pas la cible de production — voir §11.5 |
| Push et re-tag d'images | **réels**, vers `reg.cadoles.com/vcarroy` | |
| CRD `PortalData` | **réel** (CRD installé), alimenté par le simulateur | |
| Flux | **simulé** | §11.3 |
| Redmine, Gitea | instances réelles ou bouchons | §11.6 |
| Runner k6 | absent | hors périmètre local |

## 11.3 Simulation de Flux

C'est le point délicat : le dashboard n'observe pas le déploiement, il **déduit** son
avancement d'objets produits par Flux ([B7](02-blocages-et-decisions.md#b7)). Sans Flux,
la moitié du lot 5 n'a rien à regarder.

### Installer les CRD, simuler les contrôleurs

**Les CRD Flux sont installées pour de vrai** (`image.toolkit.fluxcd.io`,
`kustomize.toolkit.fluxcd.io`), ainsi que celle de `PortalData`. Seuls les contrôleurs
sont remplacés.

Ce choix n'est pas cosmétique. Le dashboard valide au démarrage que chaque
`promotion_suffix` correspond au `filterTags` d'une `ImagePolicy`, et que
`accepts_prerelease` correspond à sa plage SemVer — c'est précisément le garde-fou contre
les promotions silencieusement sans effet
([B7](02-blocages-et-decisions.md#b7)). Si l'on court-circuitait cette vérification en
développement, on désactiverait le contrôle exactement là où on l'exerce le plus souvent.
Avec des CRD réelles et des `ImagePolicy` déclarées, le code de validation, les informers
et le câblage des watches tournent sans aucune branche conditionnelle propre au
développement.

### Ce que le simulateur doit reproduire

Un composant de développement (`hack/fluxsim`, jamais déployé ailleurs) qui joue la
chaîne :

```
1. tag poussé sur reg.cadoles.com/vcarroy
2. → ImagePolicy.status.latestImage mis à jour
3. → commit du tag dans le dépôt mse-kustom local
4. → Kustomization : Reconciling, puis Ready + lastAppliedRevision
5. → Deployment patché avec la nouvelle image
6. → pods remplacés
7. → PortalData : version, puis migrations en cours, puis terminées
```

Les étapes 2 et 3 sont ce qui alimente `environment_observations.git_*`, l'étape 4 le
`flux_state`, l'étape 7 le `portal_version` — les trois sources croisées du modèle.

### Piloter, pas seulement rejouer

Le simulateur expose chaque étape comme une commande distincte, en plus d'un mode
automatique. C'est ce qui rend la machine à états de `deployments` testable de façon
déterministe :

| Scénario à provoquer | Ce qu'il vérifie |
|---|---|
| S'arrêter après l'étape 3 | l'état `reconciling` s'affiche, et le diagnostic « commit il y a N minutes, application toujours sur l'ancienne version » se déclenche |
| Échouer à l'étape 4 | passage en `failed` avec un motif lisible |
| Étape 7 en échec de migration | l'environnement n'est **pas** annoncé `live` — une image déployée dont les migrations échouent n'est pas une version livrée |
| Ne rien faire du tout | promotion restée `tagged` : reproduit le comportement d'un environnement non observable, donc de la production |
| Modifier l'image hors dashboard | détection de divergence |

Le dernier scénario mérite d'être automatisé : c'est le cas qui fait mentir un dashboard
de release, et il ne se produit jamais spontanément en développement.

### Coût et limite

Le simulateur reproduit la *forme* des objets Flux, pas son comportement de sélection.
Un `filterTags` mal écrit ou une plage SemVer mal formée ne sera pas détecté par lui.

**Il faut donc, au moins une fois, installer Flux pour de vrai** sur kind et valider les
motifs sur le registre de test — en particulier que `>=0.0.0-0` sélectionne bien les RC
et que `>=0.0.0` les exclut. C'est une validation ponctuelle, pas un environnement à
maintenir.

## 11.4 Registre de test

`reg.cadoles.com/vcarroy` remplace `reg.cadoles.com/cnous-mse` dans la configuration
locale (`reg-project` des pipelines, `repository` des images).

Deux points d'attention :

- **Les tests de promotion accumulent des tags.** Chaque scénario produit
  `X.Y.Z`, `X.Y.Z_unstable`, `X.Y.Z_testing`… Prévoir une purge, ou préfixer les
  versions de test (`0.0.0-dev.<n>`) pour les distinguer sans ambiguïté d'un vrai
  historique.
- **La résolution digest → tags s'exerce réellement** ici, ce qui valide le code de
  [B8](02-blocages-et-decisions.md#b8) sans dépendre de la production.

## 11.5 MinIO et URLs pré-signées — ce que le local ne prouve pas

MinIO déployé dans kind gère nativement SigV4 : les URLs pré-signées y fonctionneront.
**Cela ne dit rien de `nuo-s3.nuonet.fr`**, qui est une autre instance — un succès local
ne doit jamais être lu comme une validation de la cible.

**Vérifié séparément sur l'instance réelle, le 13/08/2026 :** `PUT` pré-signé accepté
depuis un poste et depuis un pod du cluster, HTTP 200 dans les deux cas. Détails et
conséquences en [B14](02-blocages-et-decisions.md#b14).

### Le piège de la procédure de test

La tentation est d'utiliser `mc share upload`. **Ce n'est pas le bon test** :

```bash
mc share upload --expire 1h nuo/mse-pipeline/probe.txt   # ← POST form policy
```

Cette commande produit un **formulaire POST** signé (upload façon navigateur), pas une
URL `PUT` pré-signée. Elle valide un mécanisme que le dashboard n'utilisera jamais, et
peut donc réussir là où le vrai chemin échouerait.

Le mécanisme à tester est la **signature SigV4 en query string sur `PUT`**, celle que
produit `minio-go` :

```
PUT https://<host>/<bucket>/<clé>
    ?X-Amz-Algorithm=AWS4-HMAC-SHA256
    &X-Amz-Credential=<clé>/<date>/<région>/s3/aws4_request
    &X-Amz-Date=…&X-Amz-Expires=3600&X-Amz-SignedHeaders=host
    &X-Amz-Signature=…
```

consommée par `curl --upload-file <url>`. Trois points à ne pas manquer en la
reproduisant : la région est **`NUO-Region`**, le payload est signé
**`UNSIGNED-PAYLOAD`** (indispensable — l'URL est signée avant que l'artefact existe), et
le test doit être rejoué **depuis un pod**, la joignabilité depuis un poste ne préjugeant
pas de celle du cluster.

## 11.6 Redmine, Gitea, OIDC

| Service | Option retenue | Alternative |
|---|---|---|
| Redmine | instance de recette, en lecture seule | bouchon HTTP rejouant des réponses enregistrées |
| Gitea (dépôts) | `forge.cadoles.com`, compte de service, dépôts de test | Gitea local en conteneur |
| Gitea (OIDC) | application OAuth2 dédiée avec redirection `http://localhost:…` | Gitea local |

Le bouchon Redmine mérite d'exister quel qu'il soit : la projection des statuts vers les
buckets (`todo`/`doing`/`done`/`rejected`) est spécifique au workflow client et doit être
testable sur des cas construits — statut inconnu, demande sans version, tracker
inattendu — qu'une instance réelle ne présentera pas à la demande.

L'amorçage des rôles ([07 §7.2](07-securite-et-permissions.md#72-autorisation)) se fait
par configuration : en local, le compte de développement est déclaré `admin` au
démarrage.

## 11.7 Table `environments` en local

Le fait que les environnements soient une table de configuration
([B13](02-blocages-et-decisions.md#b13)) rend le local trivial à décrire — un seul
environnement, pointant sur `mse-dev` :

| id | libellé | `promotion_suffix` | namespace | RC | observable |
|---|---|---|---|:--:|:--:|
| `dev` | Développement local | `dev` | `mse-dev` | ● | ● |

Une seconde entrée non observable est utile pour exercer le cas de la production sans
second cluster : mêmes réglages, `observable = false`. C'est le seul moyen de vérifier
que l'interface distingue bien « non observable » de « bloqué ».

## 11.8 Ce qui n'est pas testable en local

À garder en tête pour ne pas conclure trop vite d'un environnement de développement
vert :

| Sujet | Pourquoi | Où le valider |
|---|---|---|
| ~~SigV4 sur `nuo-s3.nuonet.fr`~~ | *vérifié le 13/08/2026* | — |
| Motifs `filterTags` et plages SemVer réels | Flux simulé | installation Flux ponctuelle, §11.3 |
| Séparation du cluster de production | un seul cluster | pas avant un accès prod |
| Volume et débit des logs | runs de développement courts | mesure sur les runs réels dès le lot 2 |
| Tests de charge | pas de runner externe | [B16](02-blocages-et-decisions.md#b16) |
| Contention de la file sous charge réelle | peu de runs concurrents | recette |
