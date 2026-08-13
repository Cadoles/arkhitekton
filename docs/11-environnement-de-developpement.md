# 11 — Environnement de développement

## 11.1 Topologie

Tout tourne dans un cluster **kind** unique.

| Composant | Emplacement | Correspondance en production |
|---|---|---|
| Tekton | namespace `mse-tekton-pipelines` | identique |
| Application MSE | namespace **`mse-dev`** | `unstable`, `qualif`, `preprod` — et `prod`, cluster séparé |
| Dashboard | namespace `mse-dashboard` — procédure en [§11.9](#119-déployer-le-dashboard-sur-kind) | identique |
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

### En place depuis le 13/08/2026

```bash
make dev-prereqs
```

Installe quatre jeux de CRD Flux, versions figées et empreintes vérifiées, puis applique
les objets de développement de `hack/dev-prereqs/fixtures/` :

| CRD | Version | Apporte |
|---|---|---|
| `image-reflector-controller` | v1.2.4 | `ImageRepository`, `ImagePolicy` |
| `image-automation-controller` | v1.2.4 | `ImageUpdateAutomation` |
| `kustomize-controller` | v1.9.4 | `Kustomization` |
| `source-controller` | v1.9.4 | `GitRepository`, et les autres sources |

**Le groupe des images est servi en `v1`, non en `v1beta2`.** Flux a fait graduer ces API.
À confirmer contre la version de Flux réellement déployée en production : le client Go du
lot 5 doit viser la même, et une `ImagePolicy` lue dans la mauvaise version ne remonte
pas les mêmes champs.

Le CRD `PortalData` **n'est pas copié dans ce dépôt**. Il appartient à `mse`
(`misc/k8s/kustomization/base/crds/portaldata-crd.yaml`) ; une copie ici divergerait en
silence, et le dashboard lirait un schéma que l'application n'écrit plus. La cible vérifie
sa présence et indique comment l'installer si elle manque.

Les objets déclarés reproduisent [B7](02-blocages-et-decisions.md#b7) : une
`ImageRepository`, quatre `ImagePolicy` — dont celle de qualif nommée **`portal-testing`**,
le suffixe Flux — une `GitRepository` sur `mse-kustom`, quatre `Kustomization` et une
`ImageUpdateAutomation`. Tous acceptés par les schémas réels.

La RBAC correspondante est dans les manifests : `mse-dashboard-flux` en lecture seule sur
`flux-system`, `mse-dashboard-observer` sur le namespace applicatif, `PortalData` compris.

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

**Validé autrement, le 13/08/2026.** Plutôt que d'installer Flux, le chemin de sélection
a été rejoué à l'identique — même version du contrôleur (v1.2.4), mêmes bibliothèques
(`Masterminds/semver/v3`, `fluxcd/pkg/version`), même enchaînement `filterTags` puis
plage :

| Tag | `unstable` (`>=0.0.0-0`) | `prod` (`>=0.0.0`) |
|---|:--:|:--:|
| `1.8.0-rc1_<env>` | retenu | **ignoré** |
| `3.1.0_<env>` | retenu | retenu |
| `2026.5.5-k8sunstable.948.…_prod` | — | **ignoré** |

Les deux pièges de [B7](02-blocages-et-decisions.md#b7) sont confirmés, et un troisième
apparaît : l'ancien schéma de tag est une pré-release au sens SemVer, et prime sur toute
version SemVer sur les environnements qui acceptent les pré-releases. Détail et
conséquences en [B7 §transition](02-blocages-et-decisions.md#b7-transition).

Installer Flux pour de vrai reste utile pour ce que cette méthode ne couvre pas : le
comportement du contrôleur autour de la sélection — cadence de scan du registre,
authentification au registre, réécriture Git effective.

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
| ~~Sémantique des plages SemVer~~ | *vérifiée le 13/08/2026 en rejouant le code de Flux* | — |
| `filterTags` réellement configurés en production | non lisibles d'ici | à relever sur le cluster de production, cf. [B7 §transition](02-blocages-et-decisions.md#b7-transition) |
| Version d'API Flux en production | CRD locales servies en `v1` | à confirmer avant le client Go du lot 5 |
| Comportement du contrôleur autour de la sélection | contrôleurs absents | installation Flux ponctuelle, §11.3 |
| Séparation du cluster de production | un seul cluster | pas avant un accès prod |
| Volume et débit des logs | runs de développement courts | mesure sur les runs réels dès le lot 2 |
| Tests de charge | pas de runner externe | [B16](02-blocages-et-decisions.md#b16) |
| Contention de la file sous charge réelle | peu de runs concurrents | recette |

## 11.9 Déployer le dashboard sur kind

### Mise en route

```bash
cp deploy/overlays/local/secrets/dashboard.env.example \
   deploy/overlays/local/secrets/dashboard.env   # non versionné, à compléter
make deploy
```

Puis, une fois l'Ingress créé et son adresse attribuée par MetalLB :

```bash
echo "$(kubectl get ingress -n mse-dashboard mse-dashboard \
        -o jsonpath='{.status.loadBalancer.ingress[0].ip}') dashboard.mse.local" | sudo tee -a /etc/hosts
```

`dashboard.mse.local` n'est pas couvert par les entrées `*.mse.local` déjà
présentes : elles sont énumérées une à une, pas génériques.

`make deploy` construit l'image, l'injecte dans les nœuds kind (`kind load`, sans
aller-retour par le registre), applique l'overlay et attend la fin des migrations.

L'overlay local apporte son propre PostgreSQL et son propre MinIO, dans le
namespace `mse-dashboard`. L'instance MinIO de `mse-dev` n'est **pas** réutilisée :
le bucket `mse-dashboard` doit rester distinct de `mse-pipeline` pour que rétention
et droits soient indépendants, et partager l'instance rendrait un `mc rb`
malencontreux capable d'emporter les deux.

### Ce que le déploiement vérifie réellement

| Point | Ce que le local prouve |
|---|---|
| RBAC | `kubectl auth can-i --as=system:serviceaccount:mse-dashboard:mse-dashboard` confirme les droits accordés **et** les refus voulus (`secrets`, `pods/exec`, écriture sur `pipelines`) |
| Découverte OIDC | joignabilité de l'issuer depuis un pod, pas la validité du client |
| Sonde `/readyz` | distingue les trois dépendances : `postgres`, `schema`, `minio` |
| Amorçage des rôles | `granted: 1` au premier pod, `0` au second — l'opération est idempotente et n'écrase rien |

### Quatre pièges rencontrés en montant ces manifests

**Un générateur sans `namespace` casse silencieusement les références.** Un
`configMapGenerator` qui n'indique pas son namespace produit un objet dans
`default`, et surtout kustomize cesse de reconnaître les `configMapRef` faits
depuis les Deployments : ils gardent le nom **sans** suffixe de hachage. Le
`kustomize build` reste vert et le déploiement échoue en
`CreateContainerConfigError`.

**Aucun transformateur `namespace:` n'est déclaré**, ni dans la base ni dans les
overlays. La `Role` `mse-dashboard-tekton` vit dans `mse-tekton-pipelines` ; un
transformateur global la déplacerait vers `mse-dashboard`, où elle n'accorderait
plus rien — sans erreur, et avec pour seul symptôme un `forbidden` à la première
création de `PipelineRun`. Chaque ressource porte donc son namespace.

**`spec.template` d'un Job est immuable.** Réappliquer l'overlay après un
changement d'image ou de configuration échoue sur `field is immutable`.
`make deploy` supprime donc les deux Jobs — migration et création de bucket —
avant d'appliquer.

**Le schéma doit entrer dans la sonde de préparation.** PostgreSQL répond au ping
bien avant que le Job de migration ait tourné : sans le contrôle `schema`, un pod
`api` se déclarerait prêt puis répondrait 500 sur chaque table absente. Observé au
premier déploiement, où le Job a échoué deux fois — PostgreSQL n'acceptait pas
encore de connexion — avant de réussir au troisième essai.

### Ce qui n'est pas encore couvert

| Sujet | État |
|---|---|
| Overlay `preprod` / `prod` | absent. Il portera l'`ExternalSecret` prévu en [07 §7.4](07-securite-et-permissions.md#74-gestion-des-secrets) ; l'écrire sans connaître les chemins Vault produirait un manifeste non vérifiable |
| `NetworkPolicy` | absente. À écrire avec la liste complète des flux sortants (PostgreSQL, MinIO, API Kubernetes, Redmine, Gitea, registre) : une politique partielle coupe un flux sans que la cause soit lisible |
| Frontend | l'`api` sert l'API seule ; la SPA sera embarquée via `embed.FS` |

## 11.10 Intégration continue

Un seul workflow Gitea Actions, `.gitea/workflows/ci.yaml`, sur les branches
d'intégration, les demandes de fusion et les tags `v*`.

| Job | Vérifie | Bloquant |
|---|---|---|
| `go` | formatage, `go vet`, tests contre un PostgreSQL et un MinIO réels | ● |
| `vuln` | `govulncheck` | ● |
| `manifests` | rendu kustomize + kube-linter | ● |
| `image` | l'image se construit (hors tag, sans publication) | ● |
| `publish` | publie l'image — **uniquement sur tag, après les trois premiers** | — |

### Pourquoi un seul fichier

Deux workflows séparés ne peuvent pas s'attendre l'un l'autre. Avec un fichier
`build.yaml` distinct, un tag poserait une image sur le registre sans qu'aucun
test ait tourné — le défaut de chaîne de livraison que ce dashboard existe
précisément pour rendre visible. Le `needs: [go, vuln, manifests]` du job
`publish` n'est possible qu'à l'intérieur d'un même workflow.

### Quatre points qui ne vont pas de soi

**`go fmt` ne peut pas servir de vérification.** Il réécrit les fichiers, donc
réussit toujours : en intégration continue il corrige le code dans un arbre de
travail jeté à la fin du job, et le dépôt reste mal formaté. `make lint` appelle
donc `fmt-check`, qui constate sans corriger. C'est ce qui rend vraie la promesse
de `make check` — reproduire localement ce que la CI vérifie.

**L'image MinIO ordinaire ne fonctionne pas en `services`.** Son entrypoint
attend `server /data` sur la ligne de commande, que la syntaxe `services` ne
permet pas de fournir : le conteneur démarrerait sur un message d'usage et le
service ne serait jamais joignable. La variante `edge-cicd` porte ces arguments
dans son `CMD`. Elle est épinglée par empreinte — un tag mouvant en CI produit
des échecs qu'aucune bissection ne retrouve.

**Le bucket de test est créé par le harnais.** Auparavant le `Makefile` s'en
chargeait avec `mc` ; chaque nouvel environnement d'exécution aurait dû le refaire
à sa façon. `ensureBucket` le fait dans le code de test, où il est le même partout.

**kube-linter analyse la sortie de kustomize, pas les fichiers du dépôt.**
L'action `stackrox/kube-linter-action` employée par `mse-tekton` prend un
répertoire ; l'appliquer ici laisserait passer précisément ce qui se joue à la
transformation — un patch qui ne s'applique pas, une référence de ConfigMap non
résolue. Le job installe donc kustomize et kube-linter, empreintes vérifiées, et
appelle la même cible `make manifests` que le poste de développement.

### Où l'image est publiée

`reg.cadoles.com/vcarroy/mse-dashboard`, et non `cnous-mse`. Une image dans le
projet partagé est un signal de disponibilité pour le reste de l'équipe ;
l'émettre avant que le dashboard fasse quoi que ce soit serait trompeur. La
bascule est une ligne — `IMAGE_NAMESPACE` — à la première version destinée à être
déployée.

### Secrets attendus

`REGISTRY_USERNAME` et `REGISTRY_TOKEN`, mêmes noms que dans `mse-tekton`, requis
par le seul job `publish`.
