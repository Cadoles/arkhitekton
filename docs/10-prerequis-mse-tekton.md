# 10 — Prérequis côté `mse-tekton`

Ces modifications sont à réaliser dans le dépôt `mse-tekton`. Elles ne dépendent pas du
dashboard et peuvent être livrées indépendamment ; elles bénéficient d'ailleurs aussi
aux CronJobs existants. Le lot 4 du dashboard (résultats de tests) en dépend
entièrement.

## P1 — Remplacer `minio-publish-results` par une publication pré-signée

**Fichier :** `base/resources/task/common/minio-publish-results.yaml`

Cette Task porte deux défauts distincts.

**Le bug.** La condition de `date-folder` est inversée et le sous-dossier daté n'est
jamais créé, quelle que soit la valeur du paramètre :

```sh
# `-n` est vrai pour "true" comme pour "false" ; la négation est donc toujours
# fausse, et le dossier n'apparaîtrait que si le paramètre était vide
if ! [ -n "$(params.date-folder)" ]; then
```

**La cause.** Le pipeline décide du layout de stockage. C'est de là que viennent
l'écrasement des résultats, l'incohérence entre familles de tests, et la nécessité pour
le dashboard de deviner où chercher.

**Correction retenue** — décision [B14](02-blocages-et-decisions.md#b14) : le layout
appartient au dashboard, qui fournit des URLs `PUT` pré-signées en paramètre du
`PipelineRun`. La Task de publication se réduit à :

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: publish-artifact
spec:
  workspaces:
    - name: source
  params:
    - name: path          # chemin dans le workspace
    - name: upload-url    # URL pré-signée fournie par le dashboard
  steps:
    - name: upload
      image: reg.cadoles.com/dh/alpine/curl:8.17.0
      script: |
        #!/usr/bin/env sh
        set -eu
        SRC="$(workspaces.source.path)/$(params.path)"
        [ -f "$SRC" ] || { echo "no artifact at $SRC, skipping"; exit 0; }
        curl --fail --silent --show-error --upload-file "$SRC" "$(params.upload-url)"
```

Bénéfices : le secret `minio-credentials` sort de la quasi-totalité des Tasks, l'image
`mc` disparaît, le bug de `date-folder` devient sans objet, et le dashboard connaît la
clé exacte de chaque artefact sans recherche par préfixe.

Pour les artefacts multiples (vidéos et captures Cypress), le dashboard fournit une URL
par fichier attendu, ou l'archive est publiée en un seul objet `tar.gz` — au choix,
selon le volume constaté.

## P2 — Émettre `meta` et le résumé sur stdout

Deux informations doivent remonter **en direct**, avant la fin du run, et sont trop
petites pour justifier un artefact : le descripteur d'exécution et la ligne de résumé.

Chaque Task de test les écrit sur stdout avec un marqueur en début de ligne, que le
collecteur du dashboard extrait du flux :

```sh
printf '##MSE-META## %s\n' \
  "{\"tool\":\"phpunit\",\"version\":\"${PHPUNIT_VERSION}\",\"exit_code\":${RC},\"status\":\"ok\"}"
printf '##MSE-SUMMARY## %s\n' "${SUMMARY_LINE}"
```

Le descripteur `meta` reste produit par le pipeline, et non déduit par le dashboard,
parce que lui seul peut distinguer **« l'outil a échoué »** de **« l'outil n'a rien
trouvé »**. C'est la meilleure propriété du design actuel de `security-audit`
(`ScannerMeta.Status`), et elle doit être préservée telle quelle.

Ce canal ne transporte que du kilo-octet : les payloads complets passent par les URLs
pré-signées de P1, jamais par stdout — les logs conteneur subissent la rotation du
kubelet et ne sont pas un transport fiable pour plusieurs mégaoctets.

## P3 — Produire des formats machine

Aucun test n'émet aujourd'hui de format exploitable. Les options ci-dessous s'ajoutent
aux sorties texte existantes, qui restent utiles à la lecture directe.

**`base/resources/task/checks/unit-test.yaml`**

```sh
php -d memory_limit=1024M vendor/bin/phpunit \
    --testsuite unit \
    --log-junit junit-unit.xml \
    | tee phpunit-logs
```

**`base/resources/task/checks/phpstan.yaml`**

PHPStan ne produit qu'un format à la fois. Écrire le JSON dans un fichier et garder la
table à l'écran :

```sh
vendor/bin/phpstan analyse --configuration phpstan-pipeline.neon \
    --no-progress --memory-limit 512M \
    --error-format=json > phpstan.json || true
vendor/bin/phpstan analyse --configuration phpstan-pipeline.neon \
    --no-progress --memory-limit 512M | tee ./phpstan-logs
```

Le second passage bénéficie du cache et coûte peu. Si le surcoût est jugé excessif,
conserver uniquement le JSON et laisser le dashboard produire l'affichage lisible — ce
qui est même préférable.

**`base/resources/task/checks/cs-fixer.yaml`**

```sh
vendor/bin/php-cs-fixer --allow-unsupported-php-version=yes --dry-run \
    --using-cache=no --format=json fix > cs-fixer.json || true
```

**`base/resources/task/checks/cypress.yaml`**

Ajouter le reporter `mochawesome` avec sortie JSON. Le pipeline `e2e-test` publie déjà
`/cypress/reports/mocha/*.html` ; il suffit d'y ajouter les `.json`. Le HTML seul n'est
pas parsable de façon fiable.

**Task `integration-test`** — cas particulier : PHPUnit ne tourne pas dans la Task mais
**dans le pod applicatif**, via `kubectl exec` piloté par `tekton-mse-manager`. Le
rapport JUnit est donc écrit dans le pod, où rien ne le récupère.

Deux options ajoutées à la commande :

| Option | Rôle |
|---|---|
| `--junit-pod-path` | chemin du rapport dans le pod (défaut `/tmp/junit-integration.xml`) |
| `--junit-file` | chemin local de rapatriement ; vide désactive |

Le rapport est lu par `cat` sur le flux d'exec, plutôt que copié par une archive : le
contenu est du XML, et cela évite d'exiger `tar` dans l'image applicative.

Trois propriétés à préserver, chacune couverte par un test :

- **le rapatriement a lieu même quand les tests échouent** — c'est précisément le run
  dont on veut le détail ;
- **un rapport absent ne fait pas échouer le run** : PHPUnit tué avant de l'écrire est
  une information, pas une panne d'infrastructure. L'absence du fichier en aval dit déjà
  ce qu'il faut savoir ;
- **sur nouvelle tentative** (retry sur exit 137), le rapport de l'essai suivant écrase
  le précédent : seul le dernier essai fait foi.

Ces fichiers sont publiés tels quels via les URLs pré-signées de P1. Le pipeline émet un
format standard ; il ne le transforme pas et ne le normalise pas.

## P4 — Ne plus effacer les artefacts E2E

**Fichier :** `base/resources/pipeline/e2e-test.yaml`

Les trois tâches `finally` passent `clear-destination: true`, ce qui supprime les
vidéos, captures et rapports du run précédent — une seconde cause de perte, distincte du
bug de P1.

Le paramètre disparaît avec la Task qui le porte : la publication pré-signée écrit à une
clé unique par run, fournie par le dashboard. Aucun run ne peut plus écraser un autre.

Le nettoyage reste souhaitable, mais par rétention — un CronJob de purge, sur le modèle
de `cleanup-minio-integration-logs` et `cleanup-minio-security-audit` qui existent déjà.

## P5 — Exposer un Trigger pour `deploy-mse`

Le pipeline `deploy-mse` existe mais n'apparaît dans aucun `Trigger` ni dans
l'`EventListener` `mse-listener`. Il n'est donc déclenchable qu'à la main.

Le dashboard créant ses `PipelineRun` directement, un Trigger ne lui est pas
indispensable. Il reste utile pour l'homogénéité et pour un déclenchement hors
dashboard. À arbitrer avec l'équipe `mse-tekton` — sans impact sur le planning du
dashboard.

**Point tranché depuis :** le déploiement d'un environnement passe par `promote-image`
(re-tag avec métadonnée SemVer), FluxCD prenant le relais. `deploy-mse` reste réservé au
pipeline d'intégration et à son namespace `mse-pipeline`. Le dashboard n'écrit jamais
dans les clusters d'environnement ([B7](02-blocages-et-decisions.md#b7)).

`promote-image` fonctionne tel quel : le séparateur retenu est l'**underscore**
(`1.8.0-rc1_testing`), caractère valide dans un tag OCI — contrairement au `+` de la
notation SemVer. Aucun ajustement de la Task n'est nécessaire, le dashboard lui passant
le tag déjà construit en `new-tag`.

## P6 — Pipeline de tests de charge

À créer entièrement (lot 8). Contraintes de conception, alignées sur P1 à P3 :

- Publier via URL pré-signée fournie par le dashboard, sans credential MinIO.
- Émettre un format machine (k6 produit du JSON en sortie ; Gatling produit un
  `simulation.log` et un JSON de synthèse).
- Émettre `##MSE-META##` et `##MSE-SUMMARY##` sur stdout, ce qui donne un débit
  observable en cours d'exécution — utile sur un test de charge long.
- Ne pas bloquer sur un dépassement de seuil : produire la donnée, laisser la décision
  au dashboard et à l'humain — c'est le principe déjà posé explicitement par le pipeline
  `security-audit`.

## P7 — Rendre `pkg/security` consommable, et verrouiller le contrat

**Répertoire :** `images/tekton-mse-manager/pkg/security` *(était `internal/security`)*

### Le contrat n'est pas le code Go

Constat fait à l'implémentation : les descripteurs `meta` sont écrits **en shell** par
chaque Task de scan (`cat > "${META}" <<EOF`), pas par du Go. Et depuis
[B14](02-blocages-et-decisions.md#b14), le pipeline ne produit plus de findings — il
produit du SARIF brut et ce descripteur.

L'interface entre le pipeline et ses consommateurs est donc **du JSON**, pas un paquet
Go. Partager le code n'est pas un prérequis de l'architecture : c'est une commodité, dont
le seul bénéficiaire durable est le dashboard, la normalisation n'ayant plus qu'un seul
lieu d'exécution.

Le besoin réel se scinde en deux, et seul le second était urgent.

### 1. Rendre le paquet importable — fait

Deux obstacles techniques bloquaient toute réutilisation, indépendamment du choix
d'hébergement :

| Obstacle | Correction |
|---|---|
| `internal/` interdit l'import depuis un autre module — c'est la sémantique du répertoire, pas une convention | déplacé en `pkg/security` |
| `module tekton-mse-manager` — nom nu, non résoluble par `go get` | `module forge.cadoles.com/CNOUS/mse-tekton/images/tekton-mse-manager` |

Le dashboard peut désormais l'importer comme une dépendance ordinaire, sans nouveau
dépôt, sans directive `replace`, sans vendoring et sans toucher au contexte de build
Docker de `tekton-mse-manager`.

**Réserve à lever avant le premier `go get`.** Go résout les versions d'un module en
sous-répertoire par des tags préfixés du chemin : `images/tekton-mse-manager/vX.Y.Z`. Le
workflow de publication émet aujourd'hui `tekton-mse-manager/vX.Y.Z`, sans le segment
`images/`. Tant que les deux ne coïncident pas, `go get` ne verra aucune version publiée.
À arbitrer avec l'équipe : aligner le préfixe, ou publier un tag parallèle.

### 2. Verrouiller le contrat JSON — fait

Rien ne vérifiait que le `meta` écrit en shell se relisait dans `ScannerMeta`. La panne
correspondante est silencieuse : un champ renommé fait passer le scanner en statut vide
plutôt qu'en erreur, et **un scanner qui n'a pas tourné devient indiscernable d'un
scanner à zéro finding** — précisément la distinction que tout le dispositif existe pour
préserver.

`pkg/security/meta_contract_test.go` lit les Tasks du dépôt, extrait le JSON produit,
substitue les expansions shell et vérifie qu'il se relit sans perte
(`DisallowUnknownFields`), que les champs porteurs sont renseignés, et que `format` et
`category` sont effectivement pris en charge par les parseurs.

### Ce qui reste ouvert

`sec-normalize` continue de tourner à l'identique : elle porte la notification
Rocket.Chat, utile indépendamment du dashboard. Le dashboard consomme `raw/` et `meta/`,
jamais `findings.jsonl`.

Le dashboard réutilisera les **parseurs** (`sarif`, `composer`, `fingerprint`, `dedup`,
`severity`, `exceptions`) mais pas la **chaîne de traitement** : le diff cross-run passe
en SQL transactionnel et les exceptions s'appliquent à la lecture
([B14](02-blocages-et-decisions.md#b14)). La surface réellement partagée est donc
partielle.

Import direct ou recopie dans le monorepo se décidera à ce moment-là, sur pièces. Les
deux options sont désormais ouvertes ; aucune n'est refermée par ce qui précède.

## P8 — `local-path` en production — *sans suite*

Confirmé volontaire : aucune classe de stockage RWX distribuée n'est disponible dans le
cluster. Aucune action.

Reste une remarque de forme, sans urgence : le remplacement de `STORAGE_CLASS_NAME`
n'est câblé que dans `overlays/local`, la valeur étant codée en base et héritée telle
quelle en production. Le résultat est correct, par un chemin qui laisse croire à un
paramétrage. Voir [B15](02-blocages-et-decisions.md#b15).

## P10 — Tag SemVer et `version.txt` baké au build

**Fichiers :** `base/resources/task/build/generate-image-tag.yaml`,
`base/resources/pipeline/build-image.yaml`

Deux évolutions liées, qui conditionnent l'invariant de version
([B17](02-blocages-et-decisions.md#b17)) et donc l'exploitabilité de `PortalData`.

**1. Le tag d'image devient le tag SemVer.** `generate-image-tag` dérive aujourd'hui le
tag de la branche via `mktools mkt-project-version`. Le dashboard connaît la version au
moment où il crée le tag Git : il la passe en paramètre plutôt que de la faire
recalculer. La Task conserve son comportement actuel quand le paramètre est absent, ce
qui permet aux déclenchements hors dashboard (push Gitea, CronJob) de continuer à
fonctionner pendant la transition.

**2. La version est passée en `--build-arg VERSION`.** Le mécanisme existe déjà côté
`mse` : le Dockerfile déclare `ARG VERSION="latest"` et fait
`echo $VERSION > /app/version.txt` dans chaque stage applicatif. C'est ce fichier que lit
`App\Health\AppVersion` pour renseigner `PortalData`.

Écrire `version.txt` dans le contexte de build serait donc **sans effet** : le `RUN echo`
du Dockerfile l'écrase. Seul le build-arg agit.

```yaml
# buildx-build-image : nouveau paramètre
- name: version
  default: ""
# → --build-arg VERSION=<version>, ajouté seulement si le paramètre est non vide
```

Laissé vide, le Dockerfile retombe sur son défaut `latest` — valeur qui n'identifie
aucune release, et que `VersionKey::fromVersion()` traite déjà comme non exploitable.

Ces deux points valent pour `build-image` **et** pour `php-integration-test`, qui
construit ses propres images et les déploie dans `mse-pipeline`.

**`version.txt` ne porte jamais la métadonnée de promotion.** Une pré-release donne
`1.8.0-rc1`, jamais `1.8.0-rc1_testing` : promouvoir est un re-tag du même digest, donc
de la même image, donc du même fichier. La métadonnée est ajoutée après le build, sur le
tag ([B17](02-blocages-et-decisions.md#b17)).

Cela vaut aussi pour le paramètre passé à `generate-image-tag` : le dashboard transmet la
version, pas le tag de promotion.

## P9 — Gestion des secrets

Voir [B10](02-blocages-et-decisions.md#b10) et
[07 §7.4](07-securite-et-permissions.md#74-gestion-des-secrets). Hors périmètre du
dashboard, mais à traiter avant sa mise en production, les deux projets partageant le
même compte de service MinIO.

P1 réduit fortement l'exposition : le secret `minio-credentials` sort de la quasi-totalité
des Tasks, ce qui rend la rotation nettement moins coûteuse.

---

## Récapitulatif

| Réf. | Modification | Bloque | Effort |
|---|---|---|---|
| P1 | Publication pré-signée, `minio-publish-results` retirée | lot 4 | faible |
| P2 | Marqueurs `##MSE-META##` / `##MSE-SUMMARY##` sur stdout | lot 4 | faible |
| P3 | Sorties JUnit / JSON | lot 4 | moyen |
| P4 | Retirer `clear-destination` sur E2E | lot 4 | trivial |
| P5 | Trigger `deploy-mse` | — | faible |
| P6 | Pipeline de tests de charge | lot 8 | élevé |
| P7 | `pkg/security` importable + contrat `meta` verrouillé | lot 4 | faible |
| P8 | ~~`local-path` en production~~ — *volontaire, sans suite* | — | — |
| P9 | Secrets migrés vers Vault | mise en production | moyen |
| P10 | Tag SemVer paramétrable + `version.txt` baké | **lot 5** | faible |

Deux ensembles distincts :

- **P1 à P4 et P7** débloquent le **lot 4** (résultats de tests), qui porte à lui seul le
  second objectif du projet.
- **P10** débloque le **lot 5** (releases et déploiements) : sans `version.txt` baké,
  `PortalData` rapporte une version fausse et toute la vue « quelle version sur quel
  environnement » s'effondre.

**Préalable à P1 : levé.** `nuo-s3.nuonet.fr` accepte les `PUT` pré-signés SigV4,
vérifié depuis un poste et depuis un pod du cluster avec l'image `alpine/curl` de la Task
([B14](02-blocages-et-decisions.md#b14)). P1 n'a plus de dépendance externe.
