# 02 — Blocages et décisions

Chaque point est classé : **blocage** (empêche une fonctionnalité demandée tant qu'il
n'est pas levé), **arbitrage** (choix technique tranché ici, à valider), **question**
(nécessite une réponse du client ou de l'équipe MSE).

Un point dont le titre porte *tranché* a reçu sa réponse ; le raisonnement qui y a mené
est conservé, parce qu'il explique des contraintes de conception qui, elles, restent
actives. Les questions encore ouvertes sont regroupées
[en fin de document](#récapitulatif-des-questions-à-poser).

---

## B1 — Les résultats de tests ne sont pas adressables par run {#b1}

**Blocage.** La Task `minio-publish-results` est censée pouvoir isoler chaque
exécution dans un sous-dossier daté. Sa condition est inversée :

```sh
if ! [ -n "$(params.date-folder)" ]; then
  DATE=$(date +"%Y%m%d%H%M%S")
  DESTINATION_FOLDER="${DESTINATION_FOLDER}/${DATE}"
fi
```

`-n` teste une chaîne non vide. Que l'appelant passe `true` ou laisse le défaut
`false`, la valeur est non vide, la négation est fausse, et le dossier daté n'est
**jamais** créé. Le sous-dossier n'apparaîtrait que si le paramètre valait la chaîne
vide — l'inverse de l'intention.

Conséquence : les résultats de `php-static-tests` s'écrasent à chaque exécution. Le
pipeline `e2e-test` va plus loin, il passe `clear-destination: true` et supprime
explicitement les artefacts précédents.

**Effet sur le dashboard.** Impossible d'afficher les résultats d'une version passée,
de comparer deux runs, ou d'attacher un rapport à une release. La demande « collecter,
stocker et afficher les logs / résultats » et « le niveau de qualité de la version »
tombe.

**Levée.** Le bug disparaît avec la Task qui le porte : la décision
[B14](#b14) retire la logique de publication des pipelines et confie le layout de
stockage au dashboard, qui fournit des URLs pré-signées. Détail en
[10 — Prérequis mse-tekton](10-prerequis-mse-tekton.md).

Tant que ce n'est pas fait, le dashboard peut collecter les logs lui-même depuis les
pods (voir [06](06-orchestration-tekton.md)), ce qui couvre les logs mais pas les
artefacts (vidéos Cypress, rapports).

---

## B2 — Les tests n'émettent aucun format machine {#b2}

**Blocage.** PHPUnit tourne avec le reporter texte par défaut, PHPStan avec la table
ASCII, php-cs-fixer avec un diff, Cypress avec un rapport Mocha HTML. Le seul élément
structuré de toute la chaîne est une ligne de résumé extraite par regex.

**Effet.** « Le détail des tests avec un parsing des résultats afin de les consulter
dans une vue enrichie » suppose de connaître, test par test, le nom, la classe, la
durée, le message d'échec et la trace. Scraper du texte pour cela est possible mais
fragile : chaque montée de version de PHPUnit casse le parseur.

**Levée.** Ajouter les sorties machine aux Tasks existantes, sans retirer les sorties
texte :

| Outil | Option à ajouter | Format |
|---|---|---|
| PHPUnit | `--log-junit build/junit.xml` (+ `--coverage-clover` si couverture voulue) | JUnit XML |
| PHPStan | `--error-format=json` (en second passage ou via `--error-format=table` + fichier) | JSON |
| php-cs-fixer | `--format=json` | JSON |
| Cypress | reporter `mochawesome` avec `json: true` | JSON |
| Audit sécurité | *déjà normalisé* | JSONL + JSON |

Le format JUnit XML est le pivot naturel : PHPUnit et Cypress savent l'émettre, ce qui
donne un seul parseur côté dashboard pour trois familles de tests.

Ces fichiers sont publiés tels quels, sans retraitement, via les URLs pré-signées de
[B14](#b14). Le pipeline émet un format standard ; il ne le transforme pas.

---

## B3 — Il n'existe pas de pipeline de release {#b3}

**Blocage.** Le processus décrit — création d'une pré-release, d'une release finale,
d'un patch — n'a aucun équivalent Tekton. Ce qui existe : `build-image` produit des
images taguées par `mktools mkt-project-version` à partir de la branche et de la
révision, `retag-image` re-tague une image existante.

Ce qui n'existe nulle part : la création du tag Git, la clôture de la version Redmine,
la génération de la note de release, l'agrégation des résultats de tests en une
décision de release.

**Effet.** « Permettre de lancer une release de cette version (pre-release ou release
finale, patch…) » est une construction, pas un pilotage.

**Arbitrage proposé.** Répartir plutôt que tout mettre dans Tekton :

- **Le dashboard** crée le tag Git et la Release Gitea via l'API Gitea, calcule la
  note de release, écrit son propre enregistrement `release`. Ces opérations sont
  transactionnelles et doivent pouvoir échouer proprement — mauvais candidat pour un
  pipeline.
- **Tekton** construit et pousse les images à partir du tag (`build-image` avec
  `revision = refs/tags/<tag>`), puis exécute la batterie de tests.

Cela évite d'écrire un pipeline « release » monolithique qui devrait manipuler des
credentials Git en écriture et de la logique métier.

**Confirmé par le client.** La procédure est aujourd'hui partiellement manuelle :
`retag-image` re-tague les images et les publie sur le registre, **la release Gitea
est créée à la main**. L'arbitrage ci-dessus automatise donc exactement le segment qui
ne l'est pas, sans rien retirer de ce qui fonctionne. Le pipeline `retag-image` est
conservé tel quel et piloté par le dashboard.

**Confirmé également :** la maquette ne décrit que la partie consultation. Le pilotage
est entièrement à concevoir, derrière les permissions de
[07 §7.2](07-securite-et-permissions.md#72-autorisation).

---

## B4 — Corrélation entre une demande utilisateur et un PipelineRun {#b4}

**Blocage technique.** L'`EventListener` répond `202 Accepted` avec un `eventID`, mais
les `PipelineRun` sont créés en `generateName` : le nom réel n'est pas connu de
l'appelant. Tekton Triggers pose bien un label
`triggers.tekton.dev/triggers-eventid`, ce qui rend la corrélation possible, mais par
un aller-retour de recherche et avec une fenêtre de course.

**Arbitrage.** **Le dashboard crée les `PipelineRun` directement via l'API Kubernetes**
(clientset `tektoncd/pipeline`), et non via l'`EventListener`. Il maîtrise alors le nom
(`<pipeline>-<releaseId>-<seq>`), pose ses propres labels
(`arkhitekton/release-id`, `arkhitekton/job-id`, `arkhitekton/requested-by`,
`arkhitekton/queue-class`) et récupère l'objet créé de façon synchrone.

L'`EventListener` reste en place, inchangé, pour les webhooks Gitea et les CronJobs.
Les deux voies coexistent ; le dashboard reconnaît les runs qu'il n'a pas déclenchés à
l'absence de ses labels et les affiche en lecture seule.

**Conséquence.** Le dashboard doit reproduire, dans son code, le contenu des
`TriggerTemplate` : `workspaces` (secrets `git-credentials`, `regcred`,
`volumeClaimTemplate` avec la bonne `storageClassName`), `taskRunTemplate`
(`serviceAccountName: tekton-kubectl` pour l'intégration, `imagePullSecrets`), et les
valeurs par défaut des paramètres. Ces définitions doivent vivre dans une configuration
déclarative versionnée (voir [06](06-orchestration-tekton.md#catalogue)), pas en dur
dans le code Go, sinon toute évolution de `mse-tekton` casse silencieusement le
dashboard.

---

## B5 — Rétention des logs Tekton {#b5}

**Blocage.** Les logs d'un `TaskRun` sont les logs de son pod. Dès que le pod est
supprimé — pruner Tekton, éviction, nettoyage de namespace — ils disparaissent
définitivement. Tekton ne conserve rien.

**Effet.** « Collecter, stocker et afficher les logs » impose de les capter pendant
l'exécution.

**Arbitrage.** Un collecteur dans le service `worker` suit (`follow=true`) les logs de
chaque conteneur de chaque pod des `TaskRun` d'un `PipelineRun` piloté, les diffuse en
direct vers le frontend (SSE) et les écrit en continu sur MinIO. À la fin du run, il
scelle l'objet et enregistre sa référence en base. Détail en
[06](06-orchestration-tekton.md#logs).

---

## B6 — Redmine ne notifie pas {#b6} — *tranché*

**Blocage.** Redmine n'a pas de webhook natif. Sans plugin, la seule voie est
l'interrogation périodique de son API REST.

**Effet.** La demande « stocker les données provenant d'API pour ne pas avoir à
interroger les services en boucle » est en tension directe avec cette contrainte : on
ne peut pas *ne pas* boucler sur Redmine.

**Arbitrage.** Boucler, mais à faible fréquence et de façon incrémentale :

- Poll toutes les 2 à 5 minutes des versions du projet (`/projects/:id/versions.json`),
  volume faible et quasi statique.
- Poll des demandes par `updated_on >= <dernier passage>`
  (`/issues.json?project_id=…&updated_on=>=…&status_id=*&limit=100`), pagination
  complète, ce qui borne le trafic à ce qui a réellement changé.
- Rafraîchissement forcé à la demande depuis l'interface (bouton), et
  systématiquement avant une opération de release.
- `Last-Modified` / `If-Modified-Since` là où Redmine les honore.

Les données sont projetées en base ; l'interface ne lit jamais Redmine directement.

**Tranché.** Aucun plugin n'est déployable sur l'instance client. L'interrogation
périodique incrémentale est donc la seule voie, et la latence de quelques minutes est
une propriété assumée du système, pas une limitation temporaire. L'interface affiche
l'âge des données Redmine et propose un rafraîchissement manuel ; une resynchronisation
est déclenchée d'office avant toute opération de release, où la fraîcheur compte
réellement.

---

## B7 — Mécanisme de déploiement {#b7} — *tranché*

**La chaîne réelle, confirmée par le client :**

```
dashboard ──▶ retag-image (crane tag) ──▶ registre ──▶ FluxCD ──▶ environnement cible
```

- **`retag-image` est le mécanisme de release.** Il pose un tag supplémentaire sur
  un digest existant, une image par exécution : le dashboard en lance une par
  image promue. Nommé `promote-image` jusqu'ici, ce qui décrivait l'intention
  plutôt que l'acte — il ne promeut rien, c'est Flux qui déploie, et le dashboard
  s'en sert aussi hors promotion pour donner à une release finale son propre tag.
- **`portal-dev` est construite mais jamais promue.** La construction publie une
  troisième image, celle de développement : elle embarque xdebug, xhprof et le
  client PostgreSQL, et fait tourner le job de bootstrap des overlays `dev` et
  `pipeline` — seul à savoir charger le jeu de données de test. Aucun
  environnement du flux de promotion ne la déploie, donc rien ne la re-tague. Elle
  reste atteignable par son tag de version, ce qui permet de reproduire un défaut
  sur exactement la version livrée.
- **FluxCD prend le relais** et déploie sur les environnements cibles en fonction du tag
  de l'image. Le dashboard n'intervient pas dans cette phase.
- **`deploy-mse` est hors flux.** Il sert uniquement au pipeline de tests d'intégration,
  qui a besoin de piloter son propre environnement dédié `mse-pipeline`. Son paramètre
  `replace-image-mapping` y fait le travail que Flux fait ailleurs. Il ne concerne aucun
  environnement du flux de promotion.

### Trois conséquences

**1. Le dashboard n'écrit jamais dans les clusters d'environnement.** C'est Flux qui
déploie ; toute écriture directe produirait une dérive que Flux corrigerait au
rapprochement suivant. La RBAC de [07 §7.3](07-securite-et-permissions.md#73-rbac-kubernetes)
— lecture seule sur les namespaces applicatifs — était déjà la bonne, et le devient par
nécessité et non par prudence.

**2. `deployment.method` disparaît du modèle.** Il n'y a qu'une voie. Le champ
`deploy_method ∈ {promote, kustomize}` prévu en [04](04-modele-de-donnees.md) est retiré.

**3. Le déploiement devient asynchrone et observé, pas piloté.** Entre le re-tag et le
service rendu s'intercalent la détection par Flux, le rapprochement, le rollout, puis la
mise à jour de `PortalData` ([B8](#b8)). Le dashboard ne « déploie » pas : il **demande**
une promotion et **constate** son aboutissement. Le modèle de `deployments` porte donc un
état, détaillé en [04](04-modele-de-donnees.md).

### Comment Flux sélectionne l'image — *tranché*

**Par les métadonnées de build SemVer.** L'environnement cible est encodé dans le
suffixe de métadonnée du tag — noté `+<métadonnée>` en SemVer, publié avec un `_` dans
le registre pour la raison exposée plus bas.

| Sémantique SemVer | Tag OCI publié | Signification |
|---|---|---|
| `1.8.0-rc1` | `1.8.0-rc1` | image construite, non promue |
| `1.8.0-rc1+unstable` | `1.8.0-rc1_unstable` | promue sur unstable |
| `1.8.0-rc1+testing` | `1.8.0-rc1_testing` | promue sur qualif |
| `3.1.0+preprod` | `3.1.0_preprod` | release finale sur pré-production |
| `3.1.0+prod` | `3.1.0_prod` | promue en production |

Promouvoir = `retag-image` re-tague l'image existante avec le tag suffixé. C'est un
**re-tag du même digest**, pas une reconstruction : une release promue sur trois
environnements est une image, quatre tags.

**Attention — la métadonnée de qualif est `testing`.** Flux connaît cet environnement
sous son ancien nom. Le dashboard affiche `qualif` et écrit `testing`. Ce n'est pas un
alias de lecture pour compatibilité, c'est une correspondance **de sortie**, utilisée
pour construire un tag : s'en tromper promeut vers le mauvais environnement, ou vers
aucun. Elle est donc portée par un réglage dédié du déployeur, `promotionSuffix`, et non
par la liste générique d'alias — voir [B13](#b13).

**Conséquences.**

- **Pas de tag flottant.** Chaque promotion produit un tag immuable et distinct ; la
  table `environments` ne porte donc aucun champ de ce type.
- Le registre accumule les tags. C'est une bonne nouvelle pour [B8](#b8) : la liste des
  tags d'un digest dit à la fois quelle version c'est et où elle a été promue.
- La résolution digest → tags renvoie plusieurs entrées ; le dashboard retient le tag
  sans métadonnée comme identité de version, et les tags suffixés comme historique de
  promotion.

### Encodage du séparateur — *tranché : underscore*

`+` est exclu de la grammaire des tags OCI (`[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`) : un
`crane tag … 1.8.0-rc1+testing` serait refusé par le registre. D'où la colonne « tag OCI
publié » du tableau ci-dessus, où le séparateur retenu est **`_`**.

Le dashboard construit ses tags depuis un gabarit configurable
(`promotion_tag_template`, défaut `{version}_{suffix}`) plutôt qu'en dur : le séparateur
reste un paramètre si la convention évolue.

### Contrat avec l'`ImagePolicy` — deux pièges

`1.8.0-rc1_testing` n'est pas non plus du SemVer valide. La politique Flux doit donc
extraire la partie version avant d'appliquer son ordre :

```yaml
# ImagePolicy de l'environnement qualif
filterTags:
  pattern: '^(?P<semver>.+)_testing$'
  extract: '$semver'
policy:
  semver:
    range: '>=0.0.0-0'      # le suffixe -0 est indispensable, voir ci-dessous
```

**Piège 1 — les plages SemVer excluent les pré-releases par défaut.** `>=0.0.0` ne
correspond **pas** à `1.8.0-rc1` : une plage sans composante de pré-release n'en
sélectionne aucune. Sans le suffixe `-0`, l'`ImagePolicy` d'un environnement de test
ignorerait silencieusement toutes les RC promues vers lui.

**Piège 2 — c'est aussi ce qui protège la production.** Le comportement est souhaitable
sur `preprod` et `prod`, qui ne doivent recevoir que des versions stables : leur plage
s'écrit sans `-0`, ce qui rend une RC structurellement inéligible.

**Conséquence pour le dashboard.** Promouvoir une RC vers la production produirait un tag
valide, poussé sans erreur, que Flux ignorerait — un **échec silencieux**, le pire mode
de défaillance possible pour une opération de release. Le dashboard ne s'en remet donc
pas à Flux pour refuser : l'environnement porte un réglage `acceptsPrerelease`, et la
promotion est refusée en amont, avec un message explicite.

Ce champ est vérifié au démarrage contre la plage réellement configurée dans
l'`ImagePolicy` correspondante. Une divergence entre les deux est exactement ce qui
produirait l'échec silencieux, et bloque donc le démarrage.

### Plages par environnement — *tranché*

| Environnement | Pré-releases | Plage |
|---|:--:|---|
| `unstable` | ● | `>=0.0.0-0` |
| `qualif` | ● | `>=0.0.0-0` |
| `preprod` | ● | `>=0.0.0-0` |
| `prod` | | `>=0.0.0` |

**Les RC vont jusqu'en pré-production ; la production ne reçoit que des releases
finales.** C'est bien ce que décrivait la demande initiale — « création de pre releases
(release candidates) et déploiement sur les environnements de test (unstable, testing,
preprod) », puis « création de la release finale, déploiement sur l'environnement de
prod ». La frontière RC / release finale se situe donc entre `preprod` et `prod`, et non
avant `preprod`.

### État du mécanisme — *tranché*

**La promotion est déjà en service.** Les `ImagePolicy` existent : le dashboard les lit,
il n'y a rien à mettre en place. Le lot 5 n'est plus suspendu à un préalable côté Flux.

Le schéma de tag décrit plus haut est en revanche l'état **cible**, pas l'état courant —
voir la section suivante, qui en mesure l'écart.

### La transition SemVer n'est pas commencée, et elle n'est pas neutre {#b7-transition}

**Relevé sur `reg.cadoles.com/cnous-mse/portal` le 13/08/2026 : 415 tags, dont zéro au
format `<version>_<environnement>`.** L'environnement est aujourd'hui porté par le canal
mktools, à l'intérieur du tag :

| Canal | Tags | Exemple |
|---|--:|---|
| `k8sunstable` | 87 | `2026.7.15-k8sunstable.1128.83399b37f` |
| `k8spreprod` | 77 | |
| `k8stesting` | 76 | |
| `k8sprod` | 42 | |

Les `ImagePolicy` de production filtrent donc nécessairement sur ce canal, et non sur un
suffixe. **Un tag `3.1.0_unstable` poussé contre les politiques actuelles ne
correspondrait à rien** : le changement de schéma de tag et la réécriture des
`ImagePolicy` doivent être simultanés, sans quoi la promotion devient un no-op — le mode
de défaillance que [B7](#b7) cherche précisément à éliminer.

Deux effets ont été **vérifiés en exécutant le code de sélection de Flux**
(image-reflector-controller v1.2.4, `Masterminds/semver/v3` + `fluxcd/pkg/version`) :

**1. L'ancien schéma est une pré-release au sens SemVer.** `2026.7.15-k8sunstable.1128.…`
se parse comme la version `2026.7.15` assortie de la pré-release
`k8sunstable.1128.…`. Conséquence directe : la plage `>=0.0.0` de la production
**exclut tous les tags actuels**, exactement comme elle exclut les RC.

**2. Pendant la coexistence des deux schémas, l'ancien gagne toujours.** Sur une plage
`>=0.0.0-0` — `unstable`, `qualif`, `preprod` — la comparaison SemVer place `2026.7.15`
au-dessus de `3.1.0` : le numéro majeur est une année. Vérifié :

```
2026.5.5-k8sunstable.948.92c5160       accepté
2026.7.15-k8sunstable.1128.83399b37f   accepté
1.8.0-rc1                              accepté
3.1.0                                  accepté
→ Latest() retient 2026.7.15-k8sunstable.1128.83399b37f
```

Tant qu'un tag de l'ancien schéma reste éligible à une politique, **aucune version
SemVer ne sera jamais sélectionnée**, et rien ne le signalera.

Ce que cela impose au basculement : les `ImagePolicy` doivent cesser d'accepter l'ancien
schéma au moment même où elles commencent à accepter le nouveau. Le filtre par suffixe
`_<environnement>` le fait naturellement — un tag mktools ne porte pas ce suffixe et
sort du filtre — **à condition qu'aucun tag suffixé de l'ancien schéma n'existe**. Le
relevé ci-dessus confirme que c'est le cas aujourd'hui : la fenêtre est propre.

**Promouvoir ne touche pas `mse-kustom`** — l'opération se limite à pousser un tag sur le
registre. **Flux, en revanche, y réécrit le tag retenu** : l'image-automation-controller
commite dans `mse-kustom` la valeur sélectionnée par l'`ImagePolicy` de chaque
environnement.

La chaîne complète est donc :

```
dashboard ──▶ retag-image ──▶ registre
                                   │
                      Flux : ImagePolicy sélectionne le tag
                                   │
                      Flux : commit du tag dans mse-kustom   ◀── observable via Gitea
                                   │
                      Flux : Kustomization applique          ◀── observable via l'API k8s
                                   │
                      rollout, puis migrations → PortalData  ◀── observable via l'API k8s
```

Cette réécriture en Git est une source d'observation à part entière, et la seule qui soit
lisible **sans accès au cluster** — ce qui la rend décisive pour la production
([B8](#b8)). Elle fournit en outre, gratuitement, un historique de déploiement daté et
attribué pour tous les environnements : l'historique des commits de `mse-kustom` *est*
l'historique des déploiements.

### Ce que le dashboard observe

- **`Kustomization`** (`kustomize.toolkit.fluxcd.io`) — confirmé par le client :
  conditions `Ready` / `Reconciling` et `lastAppliedRevision`. C'est le signal
  d'aboutissement du déploiement.
- **`ImagePolicy` / `ImageRepository`** — quelle image la politique de l'environnement a
  retenue, ce qui permet de distinguer « Flux n'a pas encore vu le tag » de « Flux l'a vu
  et le rapprochement est en cours ».

Lecture seule dans les deux cas.

---

## B8 — Identifier la version réellement déployée {#b8} — *tranché, avec une zone d'ombre*

**Blocage fonctionnel.** « Pour chaque environnement, quelle version de l'application
est livrée » ne peut pas se contenter de ce que le dashboard croit avoir promu : une
intervention manuelle, un rollback, un rapprochement Flux en échec, et l'affichage ment.
Avec [B7](#b7), l'écart est structurel — le dashboard demande, Flux exécute, le délai
est réel.

### Deux questions distinctes, deux sources

La confusion à éviter : « quelle release est déployée » et « quelle image tourne » ne
sont pas la même question, et n'ont pas la même source. Le re-tag de la RC par la release
finale ([B17](#b17)) fait qu'un digest peut porter plusieurs identités.

| Question | Source | Pourquoi elle seule |
|---|---|---|
| **Quelle release est déployée ?** | le **tag promu**, lu dans le commit `mse-kustom`, à défaut au registre | c'est le tag que l'`ImagePolicy` a retenu ; le digest, partagé entre RC et release finale, ne suffit pas à trancher |
| **Quelle image tourne, et ses migrations ont-elles abouti ?** | le CRD **`PortalData`** | seule source issue de l'application en exécution, et seule à porter l'état des migrations |

### Le CRD `PortalData`

**Vérifié sur le cluster le 13/08/2026.** Le CRD est déployé et alimenté : une
instance `portal` existe dans `mse-dev`, en phase `Ready`.

Correction de ce qui était écrit ici : il n'est **pas** introduit par une PR sur
`mse-kustom`. Il appartient au dépôt **`mse`**
(`misc/k8s/kustomization/base/crds/portaldata-crd.yaml`), et c'est le job de bootstrap
de l'application qui en écrit le `status`.

Le contrat réel, relevé sur le CRD installé :

| Champ | Contenu |
|---|---|
| `status.phase` | `Bootstrapping`, `Ready` ou `Failed` |
| `status.dataVersion` | version de l'image ayant réalisé le dernier bootstrap réussi |
| `status.dataVersionKey` | forme normalisée triable de `dataVersion` (`YYYY.MM.DD.HHMM`) |
| `status.schemaVersion` | dernière migration Doctrine appliquée (informatif) |
| `status.bootstrapCompletedAt`, `status.fixturesLoadedAt` | horodatages |
| `status.conditions` | dont `DataAheadOfCode` lors d'une rétrogradation |

Son apport propre est l'état des migrations : **une image déployée dont les migrations
ont échoué n'est pas une version livrée**, et aucune autre source ne le dit.

Deux conséquences pour le dashboard :

- **`schemaVersion` n'est pas une version de release** : c'est un nom de classe
  Doctrine (`Application\Migrations\Version20260622083402`). L'identité de la version
  se lit dans `dataVersion`, jamais là ;
- **`dataVersionKey` sera vide après le passage à SemVer.** Sa construction est
  détaillée ci-dessous, et elle ne concerne pas que le dashboard.

### `dataVersionKey` ne survit pas au passage à SemVer {#b8-versionkey}

`App\Health\VersionKey::fromVersion` du dépôt `mse` n'accepte qu'un tag mktools :

```php
preg_match('/^(\d{4})\.(\d{1,2})\.(\d{1,2})-[^.]+\.(\d{1,4})\./', $version)
```

`1.8.0` et `1.8.0-rc1` n'y correspondent pas : la méthode renvoie `null`. Or ce `null`
est traité comme « version non exploitable », cas prévu pour les builds locaux
`latest`, et **les deux garde-fous qui en dépendent deviennent alors sans effet** :

| Emplacement | Ce qui disparaît |
|---|---|
| `HealthChecker::dataReady()` | l'attente que les données soient au moins à la version de l'image. Un pod démarre alors avant que le bootstrap ait migré |
| `BootstrapCommand` | le refus de rétrograder quand les données sont plus récentes que le code |

Aucun des deux n'échoue bruyamment : ils passent en mode dégradé silencieux. C'est un
**prérequis de la transition SemVer**, à traiter dans `mse` et non dans le dashboard,
mais il conditionne la fiabilité de la source que le lot 5 exploite.

Il rapporte la version de **l'image**, pas de la release. Une release finale réutilisant
l'image de sa RC ([B17](#b17)), `PortalData` annoncera `1.8.0-rc3` là où `1.8.0` est
déployée. Le rapprochement se fait donc sur `releases.image_version`, champ prévu pour
absorber exactement cet écart — jamais sur `releases.tag`, comparaison qui échouerait sur
toute release finale.

### Sources de recoupement

1. **Le cluster** — `Deployment` puis `Pod` pour l'`imageID`, le digest réellement tiré.
2. **Le registre** — résolution digest → tags via l'API OCI. Une promotion ajoutant un
   tag suffixé sur le digest déjà construit ([B7](#b7)), la liste des tags d'un digest
   retrace les environnements atteints.

   **Vérifié sur `reg.cadoles.com/cnous-mse/portal` le 14/08/2026**, avec un compte
   personnel en attendant le compte de service. Sur les vingt derniers tags des 416
   que porte le dépôt, deux digests en portent plusieurs :

   ```
   sha256:… → 2026.8.3-k8stesting.1249.520c9a020
              2026.8.3-k8sunstable.1249.520c9a020
              2026.8.3-migrationsymfony.1249
              2026.8.3-migrationsymfony.1249.520c9a020
   ```

   Une même image promue sur `unstable` **et** sur `testing`. Le mécanisme sur lequel
   repose cette source d'observation fonctionne donc **déjà**, avec le schéma de tag
   actuel : l'environnement y est porté par le canal mktools plutôt que par un suffixe,
   mais la relation digest → environnements atteints se lit de la même façon. Elle ne
   dépend pas de la transition SemVer.
3. **Flux** — `Kustomization` pour le rapprochement, `ImagePolicy` pour la sélection.

Le croisement donne les états affichables : `à jour`, `déploiement en cours`,
`migrations en cours`, `divergent` (le tag promu et `image_version` ne concordent pas
avec ce que `PortalData` annonce), `non résolu`. Sans recoupement, seul le premier serait
distinguable du dernier.

### Zone d'ombre : la production est dans un cluster indépendant

Le dashboard tourne dans le cluster Tekton. **Il n'a accès ni aux `Deployment`, ni au
`PortalData`, ni au statut Flux de la production.**

C'était initialement un trou majeur — l'un des deux objectifs énoncés est « pour chaque
environnement, quelle version est livrée », et la production est l'environnement qui
compte le plus. Il s'est nettement resserré depuis, la réécriture Git de Flux couvrant
l'identité de la version et son historique. Ne reste hors de portée que
l'**aboutissement** : rollout terminé, migrations réussies, absence de rollback manuel.

L'interface l'assume explicitement : un état « non observable » distinct de « inconnu »,
avec sa raison, plutôt qu'une carte figée qui ressemble à une panne.

**La réécriture Git de Flux couvre l'essentiel du besoin.** Flux commite dans
`mse-kustom` le tag retenu pour chaque environnement, production comprise
([B7](#b7)). Or `mse-kustom` est sur Gitea, déjà intégré, et se lit **sans aucun accès au
cluster de production**.

Ce que cela donne, et ce que cela ne donne pas :

| Information | Disponible pour la prod ? |
|---|---|
| Version retenue par Flux pour la production | **oui** — le commit dans `mse-kustom` |
| Date de la mise à jour et auteur du commit | **oui** |
| Historique complet des déploiements | **oui** — l'historique des commits *est* l'historique |
| Preuve que l'`ImagePolicy` a bien sélectionné le tag | **oui** — le commit n'existe que si la sélection a eu lieu |
| Rapprochement `Kustomization` abouti | non — statut dans le cluster de prod |
| Rollout terminé, migrations réussies (`PortalData`) | non — même raison |
| Rollback manuel dans le cluster | non |

C'est nettement plus qu'un palliatif : la production passe de « aucune donnée » à
« version X retenue par Flux le JJ/MM à HH:MM, aboutissement non observable ». Seule la
dernière ligne de la chaîne reste dans l'ombre.

Les autres pistes restent ouvertes pour la combler :

| Piste | Ce qu'elle ajoute | Coût |
|---|---|---|
| Kubeconfig prod en lecture seule, si le réseau le permet | rollout, `PortalData`, digests réels | accès inter-cluster à ouvrir |
| Agent léger en prod poussant `PortalData` vers l'API du dashboard | idem, en sens sortant uniquement | un déploiement de plus à maintenir |

**Un diagnostic offert par le croisement.** Sur les environnements observables, comparer
la date du commit `mse-kustom` à l'état de `PortalData` détecte un déploiement bloqué :
« Flux a commité `1.8.0-rc3` il y a 12 minutes, l'application rapporte toujours
`1.8.0-rc2` ». Aucune des deux sources ne le dit seule.

---

## B9 — Frontend Vue vs React {#b9} — *tranché*

**React confirmé par le client.** La maquette n'est pas qu'une illustration : elle sert
de **référence design**. Le portage doit donc en préserver la grammaire visuelle — même
palette, mêmes tokens, mêmes échelles de statut, mêmes formulations françaises des états
vides — et les écrans nouveaux ([08 §8.4](08-frontend.md#84-écrans-nouveaux)) sont conçus
dans cette grammaire plutôt qu'inventés. Tout écart de style relève d'une décision
explicite, pas d'un accident de portage.

Éléments d'arbitrage conservés ci-dessous pour mémoire.

La maquette est en Vue 3, la demande est React. Trois options :

| Option | Coût | Risque |
|---|---|---|
| Porter en React | ~1 à 2 semaines | faible — la maquette fait ~25 composants, sans état complexe |
| Conserver Vue | nul | contredit la demande explicite ; à ne retenir que si l'équipe est Vue |
| Réécrire le design | élevé | perte de l'acquis de conception |

**Retenu : portage React.** Le travail est mécanique. Les classes Tailwind se
transposent à l'identique, la logique métier (seuils de santé, verdicts de bench,
tables de libellés) tient en un fichier et se recopie tel quel. Ce qui change : Pinia →
TanStack Query + un store léger, vue-router → React Router, `computed` → `useMemo`.

À noter : la maquette gère l'état comme si tout était chargé d'un bloc
(`load()` unique). Le dashboard réel a des données qui arrivent en flux (logs, statut
de run). Le portage doit intégrer cette différence dès le départ, pas après coup.

---

## B10 — Secrets en clair dans `mse-tekton` {#b10} — *tranché*

Le dépôt `mse-tekton` versionne en clair, dans `base/kustomization.yaml` et
`base/resources/secrets/`, une clé privée SSH de déploiement, un mot de passe de
service MinIO, une URL de webhook Rocket.Chat, un jeton Gitea et des identifiants de
comptes de test Cypress.

**Tranché.** Le projet principal a migré ses secrets vers **Vault** ; `mse-tekton` n'a
pas suivi. Le dashboard est l'occasion de le faire.

1. **Le dashboard consomme ses secrets depuis Vault**, via `external-secrets.io` avec
   backend Vault — l'opérateur est déjà déployé dans le cluster, la `Role`
   `manage-mse-pipeline-namespace` de `mse-tekton` en atteste. Aucun secret versionné,
   ni en clair, ni en base64.
2. **`mse-tekton` migre à son tour.** Ce n'est pas dans le périmètre du dashboard, mais
   les deux projets partagent le même compte de service MinIO : tant que `mse-tekton`
   versionne ce mot de passe en clair, le stockage de preuve du dashboard reste exposé.
3. **Les identifiants déjà exposés sont à renouveler.** Ils sont dans l'historique Git
   et doivent être considérés comme compromis, indépendamment de la visibilité du dépôt.
   Le compte MinIO `svc-mse-pipeline` est le plus urgent : il a un accès en écriture au
   bucket qui servira de stockage de preuve.

La décision [B14](#b14) réduit fortement l'exposition future : le secret
`minio-credentials` sort de la quasi-totalité des Tasks au profit d'URLs pré-signées,
ce qui rend la migration vers Vault nettement moins lourde qu'elle ne l'aurait été.

Non bloquant pour le développement, bloquant pour la mise en production.

---

## B11 — Le score de qualité n'a pas de définition {#b11} — *tranché*

**Pondération ci-dessous acceptée par le client, ajustable ultérieurement.** Elle est
donc une donnée de configuration (`ConfigMap`), pas une constante de code, et l'interface
affiche systématiquement le détail par signal à côté du score agrégé — un score dont on
ne peut pas voir la décomposition n'est pas ajustable en connaissance de cause.


**Question.** La maquette affiche un score sur 100 et en dérive le statut global
(`>= 85` sain, `>= 70` à surveiller, sinon critique), mais le score est une donnée
d'entrée : rien ne dit comment il est calculé.

**Proposition à valider.** Un score pondéré, transparent et affiché avec son détail :

| Signal | Poids | Règle |
|---|---|---|
| Tests unitaires | 25 | 100 si tous passent, sinon `100 × (1 − échecs/total)` |
| Tests d'intégration | 20 | idem |
| Tests E2E | 20 | idem, un test *flaky* compte pour un demi-échec |
| Qualité du code | 10 | 100 si PHPStan et cs-fixer sont propres, dégradé par palier |
| Sécurité | 15 | 0 si un finding `CRITICAL` non supprimé, 50 si `HIGH`, 100 sinon |
| Performance | 10 | verdict du bench (`pass` 100, `warn` 60, `fail` 0) |

Un signal non exécuté (`na`) sort du calcul et son poids est redistribué, avec mention
explicite dans l'interface — un score de 100 sur trois signaux mesurés ne doit pas
ressembler à un score de 100 sur six.

**À trancher avec le client :** ces pondérations engagent la lecture qu'il fera de
l'outil.

---

## B12 — Dépôt E2E hébergé sur GitLab {#b12} — *tranché*

Le pipeline `e2e-test` clone `git@gitlab.nuonet.fr:cnous-toulouse/mse/mse-tests-e2e.git`.
Le dashboard doit donc parler à **trois** forges pour la partie révisions : Gitea pour
`mse` et `mse-kustom`, GitLab pour les tests E2E.

**Arbitrage.** Abstraire la forge derrière une interface Go (`ForgeClient`) avec deux
implémentations. En pratique le dashboard n'a besoin de GitLab qu'en lecture (lister
les branches et tags du dépôt de tests, pour choisir la révision à exécuter) — un
périmètre volontairement réduit, à ne pas élargir.

---

## B13 — Nommage des environnements {#b13} — *tranché*

**Liste canonique retenue :** `unstable` → `qualif` → `preprod` → `prod`.

- `testing` est un ancien nom de `qualif`, encore présent par endroits dans le code —
  **et toujours canonique pour Flux**, qui identifie cet environnement par la métadonnée
  de build `+testing` ([B7](#b7)).

  Le mot circule donc dans deux sens, et il faut les distinguer :

  | Sens | Champ | Usage |
  |---|---|---|
  | **Lecture** — traduire une valeur héritée rencontrée en entrée | `aliases text[]` | `testing` lu dans un paramètre de pipeline est reconnu comme `qualif` |
  | **Écriture** — produire le suffixe attendu par Flux | `deployer_settings ->> 'promotionSuffix'` | promouvoir vers `qualif` écrit `+testing` dans le tag |

  Deux champs et non un seul, parce que les erreurs qu'ils causent n'ont pas la même
  gravité : une lecture ratée affiche un libellé faux, une écriture ratée **promeut vers
  le mauvais environnement**. Le champ d'écriture est obligatoire et vérifié au
  démarrage contre les `ImagePolicy` présentes dans le cluster.

  `qualif` reste l'unique vocabulaire de l'interface.
- `dev` désigne l'environnement local de développement, non déployé : **hors périmètre**.
  Le paramètre `environment` du pipeline `e2e-test` a `dev` pour défaut — c'est une
  valeur par défaut à ne jamais utiliser depuis le dashboard, qui passe toujours
  l'environnement explicitement.

**Arbitrage conservé.** Les environnements restent une table de configuration
(`environments`) portant identifiant, libellé, alias, namespace Kubernetes, tag d'image
flottant, rang dans le flux de promotion et politique de sécurité applicable — le
fichier `base/files/policies/severity.yaml` de `mse-tekton` indexe déjà ses seuils par
environnement. Le vocabulaire d'affichage est une donnée, pas une décision de code.

**Reste ouvert.** `mse-pipeline`, namespace de déploiement éphémère du pipeline
d'intégration, est déployé mais réinitialisé à chaque run. Il n'a pas sa place dans le
flux de promotion. À confirmer : doit-il être visible comme environnement transitoire
(utile pour diagnostiquer un test d'intégration en échec) ou rester entièrement interne ?

---

## B14 — Où produire, publier et normaliser les résultats {#b14}

**Arbitrage structurant.** C'est la décision qui conditionne B1, B2 et tout le lot 4.

### Le constat

Le pipeline `security-audit` est le seul à produire un jeu de données exploitable, et il
a manifestement été conçu avec une interface en tête. Il fait cependant **trois** choses
distinctes, qui n'ont pas à vivre au même endroit :

1. exécuter les scanners et constater leur issue ;
2. écrire les artefacts sur MinIO ;
3. normaliser — dédupliquer, calculer les empreintes, appliquer les exceptions,
   comparer au run précédent, rendre un verdict de politique.

La troisième pose deux problèmes concrets, visibles dans le code :

- **Le calcul cross-run se fait sans transaction.** `SecurityNormalize` appelle
  `loadPreviousRun(ctx, credentials, minioPath, runID)` puis `ApplyDiff` pour établir
  `status: new|existing|resolved`, `first_seen` et le `delta`. Deux audits concurrents
  — CronJob nocturne et lancement manuel depuis le dashboard — lisent le même
  `latest.json`, calculent chacun leur diff et écrivent tous les deux : le dernier
  écrase l'autre et la chaîne `first_seen` casse. Pire, `ApplyDiff` traite
  `previous = nil` comme neutre et documenté tel quel ; un incident MinIO transitoire
  fait donc ressortir **l'intégralité des findings en `new`**, sans erreur.
- **Normaliser à l'écriture fige le jeu de données.** MinIO est immuable : toute
  amélioration du parsing ne s'applique qu'aux runs futurs, et le dashboard doit gérer
  N versions de schéma à vie. Cas déjà présent : `ApplyExceptions` lit
  `security-exceptions.yaml` pendant le run, donc **une exception ajoutée aujourd'hui
  ne change pas le rapport d'hier** — alors que c'est exactement le genre d'élément qui
  bouge, et rétroactivement.

S'y ajoute le fait que le pipeline ne connaît pas la release à laquelle son run se
rattache : l'attribution est de toute façon une opération du dashboard.

### La décision

**Le pipeline constate, le dashboard interprète.**

| Responsabilité | Porteur | Motif |
|---|---|---|
| Exécuter l'outil | pipeline | — |
| Produire la sortie native (SARIF, JUnit XML, JSON) | pipeline | format standard, stable |
| Produire le descripteur `meta` (outil, version, exit code, statut) | **pipeline** | inobservable depuis l'artefact : seul le processus qui a lancé l'outil sait distinguer « scanner en erreur » de « scanner à zéro finding » |
| Écrire l'artefact de façon durable | pipeline | doit survivre à l'indisponibilité du dashboard (CronJob nocturne) |
| Décider **où** l'artefact atterrit | **dashboard** | c'est l'origine de B1 et de l'incohérence entre familles |
| Parser, dédupliquer, empreinter | dashboard | rejouable |
| Appliquer les exceptions | dashboard | doit être rétroactif |
| Diff cross-run, `first_seen` | dashboard | exige une transaction, pas un bucket |
| Verdict de politique, score | dashboard | interprétation |
| Rattacher à une release | dashboard | seul à le savoir |

### Le mécanisme : URL pré-signée

Le dashboard mint des URLs `PUT` pré-signées au moment où il crée le `PipelineRun`, et
les passe en paramètre. La Task publie par un simple `curl -T`.

```yaml
params:
  - name: upload-urls
    value: |
      {"junit":"https://nuo-s3.nuonet.fr/…?X-Amz-Signature=…",
       "meta": "https://nuo-s3.nuonet.fr/…?X-Amz-Signature=…"}
```

| Propriété | Effet |
|---|---|
| Credentials | plus aucun credential MinIO permanent dans les pipelines — le secret `minio-credentials` sort de la quasi-totalité des Tasks |
| Layout | décidé par le dashboard, qui connaît la clé exacte de chaque objet : plus de recherche par préfixe, plus de `date-folder`, B1 sans objet |
| Durabilité | écriture directe et durable, indépendante de la disponibilité du dashboard |
| Portée | URL limitée à une clé unique, expiration alignée sur le timeout du run |
| Binaires | vidéos Cypress et captures passent sans traitement particulier |
| Outillage | `curl` remplace l'image `mc` |

### Le canal stdout, pour le petit et l'immédiat

Ce qui est de l'ordre du kilo-octet et que l'on veut **en direct** transite par stdout,
capté par le collecteur de logs que le dashboard opère de toute façon
([B5](#b5)) :

```
##MSE-META##    {"tool":"phpunit","version":"11.4.1","exit_code":1,"status":"ok"}
##MSE-SUMMARY## FAILURES! Tests: 1301, Failures: 3.
```

Cela permet d'afficher « 3 échecs sur 1 301 » avant la fin du run, ce que l'attente de
l'artefact final ne permet pas.

### Options écartées, et pourquoi

**Tout par stdout, sans écriture objet.** Séduisant — un seul mécanisme de collecte,
zéro credential — mais trois obstacles :

- les logs conteneur passent par la rotation du kubelet (défauts `containerLogMaxSize:
  10Mi`, `containerLogMaxFiles: 5`) : un SARIF Trivy de plusieurs mégaoctets se fait
  pousser hors de la fenêtre par la verbosité du même conteneur. Et ces valeurs sont de
  la configuration kubelet, ni sous notre contrôle ni homogène d'un cluster à l'autre ;
- les artefacts binaires (vidéos, captures, HTML de couverture) ne passent pas, donc un
  second mécanisme subsiste et le bénéfice disparaît ;
- le résultat n'existerait plus que dans le log du pod jusqu'à sa lecture : worker en
  redémarrage ou pruner Tekton plus rapide, et le résultat est perdu définitivement.
  Perdre des lignes de log et perdre les résultats de tests de la release à livrer ne
  sont pas le même incident.

*(Les `results` Tekton ne sont pas une alternative : plafonnés à ~4 Ko, c'est un canal
de métadonnées, pas de payload.)*

**Le dashboard lit les volumes des pipelines.** Impossible en l'état, pour des raisons
qui ne relèvent pas du droit d'accès :

- tous les workspaces sont en `volumeClaimTemplate` + `ReadWriteOnce` +
  `storageClassName: local-path`, soit un `hostPath` sur un nœud précis : la donnée
  n'existe pas sur le nœud du dashboard ;
- Kubernetes fige les volumes d'un pod à sa création — on ne monte pas dynamiquement le
  PVC d'un run dans un pod déjà démarré, même en RWX ;
- `volumeClaimTemplate` pose un `ownerReference` vers le `PipelineRun` : le pruner
  supprime le run, le GC ramasse le PVC.

La variante praticable — un volume RWX pré-provisionné, monté en écriture par les
pipelines et en lecture seule par le worker — supprimerait effectivement toute logique
de publication des pipelines. Elle est écartée pour son coût : aucune classe RWX
n'existe aujourd'hui, elle introduit un domaine de panne partagé (un run qui remplit le
volume bloque tous les autres), contraint l'ordonnancement du worker, et reste un
staging — la copie vers MinIO subsiste, simplement déplacée. Le delta avec le
pré-signé se résume à une ligne de `curl` dans le pipeline. À reconsidérer uniquement si
une classe RWX est déployée pour d'autres besoins.

### Portage du code existant

`pkg/security` de `tekton-mse-manager` est presque pur : parsers, `fingerprint`,
`dedup`, `severity`, `sarif`, `composer`, `exceptions` n'ont d'I/O que la lecture de
fichiers, et sont déjà testés. Le package est **extrait en module Go partagé**, consommé
par le dashboard et — pendant la transition — par `tekton-mse-manager`. C'est un
déplacement, pas une réécriture.

`sec-normalize` reste en place le temps de la bascule. Le dashboard, lui, consomme
`raw/` et `meta/`, jamais `findings.jsonl`.

**Révision — 19/08/2026 : les notifications Rocket.Chat sont retirées.** Ce document
retenait d'abord de les conserver, « utiles indépendamment du dashboard ». Le client a
tranché l'inverse une fois le dashboard capable d'afficher les résultats : deux canaux
pour la même information en font un que personne ne lit.

Retirées de `security-audit` **et** de `php-integration-test`, avec la Task
`rocketchat-notify` devenue sans appelant et le secret `rocketchat-webhook` — qui était
versionné en clair, et relevait donc du problème décrit plus haut.

**Conséquence assumée, à connaître :** ces deux pipelines tournent aussi par CronJob,
indépendamment du dashboard. L'audit nocturne et les tests d'intégration nocturnes sont
désormais **silencieux** — leur résultat ne se constate qu'en ouvrant le dashboard. La
notification d'échec disparaît avec le reste, or elle couvrait le cas d'un audit qui
n'a jamais tourné. Si cette surveillance compte, elle est à reconstruire côté dashboard,
et rien ne la porte aujourd'hui.

### Ce que cela change pour les autres pipelines

La vraie portée de cette décision n'est pas de défaire l'audit de sécurité, mais
d'éviter d'écrire quatre `sec-normalize` de plus. Un parseur JUnit dans le dashboard
couvre d'un coup les tests unitaires, d'intégration et E2E.

### Faisabilité vérifiée sur l'instance cible

`nuo-s3.nuonet.fr` accepte les URLs `PUT` pré-signées SigV4. Testé le 13/08/2026 avec une
signature en query string, celle que produira `minio-go` :

| Test | Résultat |
|---|---|
| `PUT` pré-signé depuis un poste | HTTP 200, 0,15 s |
| `PUT` pré-signé depuis un pod du cluster (`alpine/curl`, l'image de la Task) | HTTP 200, 0,16 s |
| Objets écrits et relus | contenu intact |

C'était la seule hypothèse non vérifiée de cette décision. Elle est levée.

**Trois observations qui engagent l'implémentation :**

- **La région est `NUO-Region`**, et non `us-east-1`. À porter en configuration : une
  région erronée invalide la signature, avec un message d'erreur qui n'y renvoie pas.
- **`UNSIGNED-PAYLOAD` est accepté.** Décisif : le dashboard signe l'URL *avant* que
  l'artefact existe, il ne peut donc pas en connaître le SHA256. Sans cette tolérance,
  tout le mécanisme tombait.
- **Le versioning est actif sur le bucket** (une suppression pose un delete marker). La
  politique de rétention doit en tenir compte, sinon les objets « supprimés » continuent
  d'occuper de l'espace.

**Réserve restante.** La durée de vie des URLs doit couvrir le timeout le plus long
(90 min pour `security-audit`) sans excéder ce qui est raisonnable.

**Point de sécurité confirmé au passage.** Le compte `svc-mse-pipeline` utilisé pour le
test lit et écrit l'intégralité du bucket. Le compte qui signe les URLs doit être
restreint en écriture au préfixe `raw/`, comme prévu en
[07 §7.4](07-securite-et-permissions.md#74-gestion-des-secrets) — ce n'est pas le cas
aujourd'hui.

---

## B15 — `local-path` en production {#b15} — *tranché*

**Volontaire.** Aucune classe de stockage RWX distribuée n'est disponible dans le
cluster ; `local-path` est le choix assumé.

Deux conséquences, toutes deux favorables aux décisions déjà prises :

1. **La variante « volume RWX partagé » de [B14](#b14) est définitivement close.** Elle
   supposait une classe qui n'existe pas et ne peut pas être déployée. La publication
   par URL pré-signée n'est donc pas seulement le meilleur choix, c'est le seul.
2. **Le dashboard ne lira jamais les volumes des pipelines.** Ce n'était déjà pas
   souhaitable ; c'est désormais impossible, ce qui ferme le sujet.

À noter au passage, sans lien avec le dashboard : le remplacement de
`STORAGE_CLASS_NAME` n'est câblé que dans `overlays/local`, la valeur `local-path` étant
codée en base et donc héritée telle quelle en production. Le résultat est le bon, mais
par un chemin qui donne l'illusion d'être paramétrable.

---

## B16 — Tests de charge : cluster partagé avec Tekton {#b16}

**Contrainte, confirmée par le client.** Le cluster qui héberge Tekton est **le même**
que celui de l'environnement de test de charge. Aucun pipeline n'existe, et la piste
retenue côté équipe est un **runner k6 hors cluster** — réflexion en cours.

### Le problème n'est qu'à moitié résolu par un runner externe

Sortir le générateur du cluster supprime sa consommation de ressources. Mais la
**cible** reste dans le cluster Tekton : un build buildx, un audit Trivy ou une campagne
E2E qui tourne pendant la mesure perturbe le résultat autant que le générateur l'aurait
fait. C'est un problème de voisinage, pas de génération.

Conséquence directe sur la maquette : `BenchPanel` et `Sparkline` comparent chaque
mesure à une baseline et affichent une tendance sur six points. Si les conditions de
mesure varient d'un point à l'autre, la courbe ne veut rien dire.

### Ce que le dashboard apporte

Le modèle de file le permet déjà, sans infrastructure supplémentaire :

- **File à une place.** Le type `load-tests` porte `maxConcurrent: 1` : deux mesures ne
  se chevauchent jamais. Voir [06 §6.3](06-orchestration-tekton.md#63-file-dexécution).
- **Étiquetage des conditions de mesure.** Chaque `test_results` de famille `load` porte
  `exclusive: true|false` et le nombre de jobs concurrents constatés au démarrage. La
  comparaison à la baseline exige les deux : file à une place **et** aucune exécution
  concurrente. Les autres mesures sont affichées, mais marquées non comparables, avec le
  motif.

Cela ne couvre pas les autres locataires du cluster. Cela couvre ce qui est sous notre
contrôle, et rend honnête la comparaison qui ne l'était pas.

**Ce que la version précédente prévoyait, et qui a été retiré.** Une classe
`exclusive-cluster` faisait barrière : à la prise du job, l'ordonnanceur cessait de
démarrer de nouveaux runs, attendait la fin de ceux en cours, puis lançait seul. Le
modèle de file a été ramené à un plafond par type, et aucun plafond ne reproduit une
barrière.

La perte est réelle mais bornée : la comparabilité ne reposait déjà pas sur la barrière
mais sur le **constat** relevé à la réservation, et ce constat exige que rien d'autre ne
tournât. Une mesure prise en voisinage n'est donc plus empêchée de se produire — elle est
écartée de la baseline, et le dit. Si le débit de mesures comparables se révélait
insuffisant, la barrière serait à réintroduire comme un attribut du type, non comme un
troisième régime de file.

### Intégration du runner externe

**Piloter le runner depuis une Task Tekton, pas depuis le dashboard.** La Task appelle
le runner, suit l'exécution, récupère la sortie. Elle est fine — un driver.

L'alternative — le dashboard pilote directement le runner — impose un second modèle
d'exécution : autre chemin de collecte de logs, autre mécanisme d'annulation, autre
chemin de résultats, autre représentation en file. Pour un seul type de job, c'est un
doublement de la machinerie sans contrepartie.

Le mécanisme pré-signé de [B14](#b14) prend ici tout son sens : **le runner hors cluster
publie ses résultats directement sur MinIO avec une URL fournie par le dashboard**.
Aucun credential MinIO à distribuer sur une machine externe au cluster — l'argument
valait pour les Tasks, il vaut davantage pour un hôte hors périmètre.

### Questions ouvertes

- Où vit le runner (VM dédiée, machine d'intégration, offre managée) et comment est-il
  joignable depuis une Task ? *(API HTTP, SSH, agent — la réponse détermine la Task)*
- L'authentification du dashboard vers le runner.
- Est-il acceptable de bloquer l'ensemble de la file pendant une campagne ? C'est un
  arbitrage débit / justesse de mesure, et il appartient au client.

---

## B17 — Invariant de version unique {#b17}

**Contexte, communiqué par le client.** Le projet migre vers un versionnement **SemVer**,
aligné sur les versions créées dans Redmine. Les pipelines de build décrivent encore
l'ancien schéma de tag (`mktools mkt-project-version` dans
`base/resources/task/build/generate-image-tag.yaml`).

Cette transition, combinée à `PortalData` ([B8](#b8)), fait apparaître une contrainte que
tout le reste du dashboard suppose sans la nommer.

### L'invariant

**Une version est une chaîne, présente à l'identique en quatre endroits :**

| Endroit | Porteur | Écrit par |
|---|---|---|
| Nom de la version Redmine | `1.8.0` | l'équipe projet |
| Tag Git | `1.8.0`, `1.8.0-rc1` | le dashboard, via l'API Gitea |
| Tag d'image | `1.8.0`, `1.8.0-rc1` | `build-image`, puis `retag-image` |
| `version.txt` dans l'image → `PortalData` | `1.8.0`, `1.8.0-rc1` | *baké au build* |

Relation précise : `tag de release = nom de version Redmine [+ suffixe de pré-release]`.
Une pré-release `1.8.0-rc1` se rattache à la version Redmine `1.8.0` — c'est un suffixe
SemVer valide, et c'est ce qui permet de relier une RC à son périmètre fonctionnel.

### Pourquoi c'est un point dur

Si ces quatre valeurs divergent, chaque fonctionnalité du dashboard se dégrade d'une
manière différente et difficile à diagnostiquer :

- `PortalData` rapporte une version absente de la table `releases` → environnement
  affiché « divergent » à tort ;
- la complétion Redmine ne se rattache à aucune release → l'écran version reste vide ;
- la note de release compare des intervalles de tags Git qui ne correspondent pas aux
  images promues.

L'invariant n'est donc pas une commodité : c'est ce qui rend la jointure possible.

### Conséquences pratiques

1. **`generate-image-tag` doit produire le tag SemVer**, et non plus dériver un tag de
   la branche. Le dashboard connaît la version au moment où il crée le tag Git ; il la
   passe en paramètre plutôt que de la faire recalculer.
2. **`build-image` doit passer cette valeur en `--build-arg VERSION`.** Le Dockerfile de
   `mse` déclare déjà `ARG VERSION="latest"` et fait `echo $VERSION > /app/version.txt` :
   le mécanisme existe, il n'était simplement jamais alimenté par les pipelines. Écrire
   `version.txt` dans le contexte de build serait sans effet, le `RUN echo` l'écrasant.
   Sans le build-arg, `PortalData` rapporte `latest`, qui n'identifie aucune release.
   Voir [P10](10-prerequis-mse-tekton.md).
3. **Le dashboard doit tolérer la période de transition.** Les images déjà publiées
   portent l'ancien schéma. La table `releases` accepte donc un `tag` non SemVer, et le
   tri par version retombe sur un tri par date quand le parsing SemVer échoue — plutôt
   que de masquer les releases anciennes.
4. **La validation à la création d'une release** vérifie que le tag proposé correspond à
   une version Redmine existante, et refuse un tag déjà pris dans Gitea ou dans le
   registre.

### La métadonnée de promotion ne fait pas partie de la version

`version.txt` porte **`1.8.0-rc1`**, jamais `1.8.0-rc1_testing`. Ce n'est pas un choix
mais une conséquence du mécanisme de [B7](#b7) : promouvoir est un **re-tag du même
digest**, donc de la même image, donc du même `version.txt`. Le fichier est figé au
build ; la métadonnée est ajoutée après, sur le tag.

C'est cohérent : `+testing` désigne une destination d'acheminement, pas une propriété de
l'application. La même image promue sur `unstable` puis sur `qualif` rapporte la même
version aux deux endroits, ce qui est exact.

Cela résout au passage la question restée ouverte : une pré-release est identifiée
`1.8.0-rc1` et non `1.8.0`, donc deux RC successives restent distinguables sur un
environnement.

**Conséquence pour le dashboard.** L'identité d'une release est le tag **sans**
métadonnée. Le rattachement d'un `PortalData` à une release se fait sur cette valeur,
les tags suffixés ne servant qu'à retracer les promotions. Une comparaison naïve entre
`PortalData.version` et le tag promu échouerait systématiquement.

### Le passage RC → release finale — *tranché : re-tag*

Les pré-releases vont jusqu'en pré-production, la production ne reçoit que des releases
finales ([B7](#b7)). Il existe donc un moment où `1.8.0-rc3`, validée en pré-production,
doit devenir `1.8.0`. Deux façons de le faire, toutes deux avec un coût réel :

| | Mécanisme | Ce que `PortalData` rapporte en prod | Coût |
|---|---|---|---|
| **Re-tag** | `1.8.0` pointe sur le digest de `1.8.0-rc3` | **`1.8.0-rc3`** — le `version.txt` est figé au build | l'invariant casse : la prod annonce une version qui n'est pas la release |
| **Rebuild** | `1.8.0` est reconstruite depuis le même commit | `1.8.0` — correct | l'artefact déployé en prod n'est pas celui qui a été validé en pré-production |

Aucune des deux n'est gratuite. Le re-tag préserve la chaîne de validation — bit pour
bit, c'est l'image testée — mais fait mentir `PortalData`. Le rebuild préserve
l'invariant mais met en production un artefact que personne n'a testé, même si le code
source est identique : dépendances résolues à nouveau, image de base éventuellement mise
à jour.

Une troisième voie existe et doit être écartée explicitement : baker `1.8.0` dans le
`version.txt` dès la RC. Cela rendrait le re-tag cohérent, mais toutes les RC d'une même
version deviendraient indiscernables dans `PortalData` — on ne saurait plus quelle RC
tourne sur `unstable` ou `qualif`, ce qui vide de son sens l'affichage de ces deux
environnements.

**Tranché : re-tag.** La release finale réutilise l'image de la RC validée.

Le bénéfice est celui qui compte le plus : **ce qui part en production est bit pour bit
ce qui a été validé en pré-production**. Aucune dépendance re-résolue, aucune image de
base rafraîchie entre la validation et la livraison.

Le prix est que `version.txt` reste figé sur `1.8.0-rc3`. Trois conséquences à traiter,
et une seule est délicate.

**1. La divergence est enregistrée, pas subie.** La table `releases` porte un champ
`image_version` — la version réellement bakée dans l'image — distinct du `tag` qui
identifie la release. Pour une release construite, les deux sont égaux ; pour une release
issue d'un re-tag, `image_version` vaut le tag de la RC source, et `source_release_id`
pointe vers elle.

Le contrôle de cohérence devient alors uniforme, sans cas particulier :

```
PortalData.version  ==  release.image_version
```

et non `== release.tag`, qui échouerait systématiquement sur toute release finale.

**2. L'identité de la release vient du tag promu, pas de `PortalData`.** Le digest étant
partagé entre `1.8.0-rc3` et `1.8.0`, il ne suffit plus à trancher. C'est le tag appliqué
par Flux — lisible dans le commit `mse-kustom` — qui dit quelle release est déployée ;
`PortalData` confirme quelle image tourne et si ses migrations ont abouti. Les deux rôles
restent distincts et complémentaires, mais leur ordre s'inverse par rapport à ce qui était
prévu.

L'interface affiche `1.8.0`, avec l'image source en second niveau : « `1.8.0` — image
`1.8.0-rc3` ». Présenté ainsi, ce n'est pas un écart mais une traçabilité.

**3. Le point délicat : la release finale n'a aucun run de tests.** Elle n'est pas
construite, donc rien ne s'exécute sur elle. Une lecture naïve du modèle afficherait une
section qualité vide sur exactement la release qui part en production.

Les résultats de la RC source sont **hérités** par la release finale — ce qui est
rigoureusement exact, puisqu'il s'agit du même artefact. L'interface l'indique
(« résultats hérités de `1.8.0-rc3` ») plutôt que de les présenter comme siens, et le
score de qualité est celui de la RC.

Corollaire à implémenter comme garde-fou : **créer une release finale depuis une RC dont
les tests ne sont pas au vert doit être signalé**. Le re-tag transporte la validation ; il
transporte aussi son absence.

---

## Récapitulatif des questions à poser

### Tranchées

| Sujet | Réponse |
|---|---|
| Framework frontend *(B9)* | **React**. La maquette est portée et sert de **référence design** |
| Environnements *(B13)* | `unstable` → `qualif` → `preprod` → `prod`. `testing` = ancien nom de `qualif`, traité en alias. `dev` = local, hors périmètre. `mse-pipeline` **non affiché** |
| Déploiement *(B7)* | `retag-image` re-tague et publie ; **FluxCD** déploie ensuite. Sélection par métadonnée SemVer, séparateur `_`, `qualif` désigné `testing`. **Mécanisme déjà en service, indépendant de `mse-kustom`.** `deploy-mse` est hors flux, réservé au pipeline d'intégration |
| Frontière RC / release *(B7)* | les pré-releases vont jusqu'en **pré-production** ; seule la production exige une version stable |
| Passage RC → release finale *(B17)* | **re-tag** de l'image validée. Ce qui part en production est bit pour bit ce qui a été validé en pré-production ; en contrepartie `PortalData` rapporte la version de la RC, absorbée par `releases.image_version` |
| Version déployée *(B8)* | CRD **`PortalData`** (PR en validation sur `mse-kustom`), alimenté par le job de migration depuis `version.txt` baké au build. **La production est dans un cluster indépendant : aucune donnée en attendant** |
| Versionnement *(B17)* | transition vers **SemVer**, aligné sur les versions Redmine. Les pipelines de build décrivent encore l'ancien schéma |
| Webhook Redmine *(B6)* | **impossible** — interrogation périodique incrémentale, définitivement |
| Secrets *(B10)* | **Vault**. Le projet principal a migré, `mse-tekton` doit suivre |
| Score de qualité *(B11)* | pondération proposée acceptée, ajustable — donc en configuration |
| Stockage *(B15)* | `local-path` volontaire, **aucune classe RWX disponible** — ferme la variante volume partagé de B14 |
| Dépôt E2E *(B12)* | GitLab, lecture seule |
| Fournisseur OIDC | **Gitea**. Les permissions sont gérées **manuellement dans le dashboard**, sans claim de groupes |
| Liaison commit ↔ demande | `#1234` dans les messages de commit et les titres de PR |
| Outil de test de charge *(B16)* | **k6**, runner hors cluster — hébergement en réflexion |
| Actions manuelles | **simple champ texte éditable** sur la release, renseigné au fil de la production du livrable |
| État de la procédure de release *(B3)* | `retag-image` re-tague et publie ; la release Gitea est **manuelle** aujourd'hui |
| Périmètre de la maquette | consultation seule ; le pilotage est à concevoir, derrière permissions |
| URLs pré-signées *(B14)* | **SigV4 vérifié sur `nuo-s3.nuonet.fr`**, depuis un poste et depuis un pod. Région `NUO-Region`, `UNSIGNED-PAYLOAD` accepté, versioning actif |

### Ouvertes

1. Compte de service en lecture sur le registre `reg.cadoles.com` ? *(B8)*
2. Runner k6 : où vit-il, comment est-il joignable depuis une Task, comment
   s'authentifie-t-on ? Bloquer toute la file pendant une campagne est-il acceptable ?
   *(B16 — reste ouvert tant que l'hébergement n'est pas décidé)*
3. Production hors cluster : la couverture par les commits `mse-kustom` — version, date,
   historique, mais pas l'aboutissement — est-elle jugée suffisante, ou faut-il instruire
   un accès en lecture au cluster de production ? *(B8 — plus une priorisation qu'un
   blocage)*
4. Gitea comme fournisseur OIDC : version de l'instance et portées disponibles, pour
   confirmer le flux code + PKCE. *(voir [07](07-securite-et-permissions.md))*
5. Comment distinguer un `#1234` Redmine d'un `#45` Gitea dans un message de commit ou
   un titre de PR ? Les deux forges utilisent la même notation, et le client n'a pas de
   réponse pour l'instant. **Portée limitée** : le client confirme que *le statut de la
   demande dans Redmine reste la référence*, donc l'ambiguïté n'affecte que le
   recoupement technique, pas le contenu fonctionnel de la note de release. *(voir
   [05 §5.3](05-api-et-integrations.md#53-gitea))*
