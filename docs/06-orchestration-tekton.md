# 06 — Orchestration Tekton

## 6.1 Comment le dashboard déclenche un pipeline

Le dashboard crée les `PipelineRun` **directement via l'API Kubernetes**, pas via
l'`EventListener` (motivation en [B4](02-blocages-et-decisions.md#b4)).

Chaque `PipelineRun` créé porte :

```yaml
metadata:
  name: <pipeline>-<jobId>          # nom maîtrisé, pas de generateName
  labels:
    mse.dashboard/job-id: "4271"
    mse.dashboard/release-id: "88"
    mse.dashboard/queue-class: "e2e"
    mse.dashboard/kind: "e2e-test"
  annotations:
    mse.dashboard/requested-by: "prenom.nom@exemple.fr"
```

Ces labels sont ce qui permet, après un redémarrage du `worker`, de retrouver l'état
réel du monde sans faire confiance à la seule base de données. Un `PipelineRun` sans
label `mse.dashboard/job-id` est un run déclenché ailleurs (webhook Gitea, CronJob) :
le dashboard l'affiche en lecture seule et ne prétend pas le piloter.

## 6.2 Le catalogue de pipelines {#catalogue}

Créer un `PipelineRun` à la main suppose de reproduire ce que faisaient les
`TriggerTemplate` : workspaces, secrets, `volumeClaimTemplate`, `serviceAccountName`,
`imagePullSecrets`, valeurs par défaut. Si ces informations sont codées en dur en Go,
toute évolution de `mse-tekton` casse le dashboard sans que rien ne le signale.

Elles vivent donc dans un **catalogue déclaratif**, monté en `ConfigMap` et validé au
démarrage contre les `Pipeline` réellement présents dans le cluster (nom du pipeline,
existence, liste des paramètres attendus). Un écart bloque le démarrage avec un message
explicite plutôt que de produire des runs invalides à la première utilisation.

Le catalogue déclare aussi les **artefacts attendus** de chaque pipeline. C'est cette
déclaration qui permet au dashboard de calculer les clés de stockage et de minter les
URLs pré-signées avant de créer le run (voir §6.6).

```yaml
pipelines:
  - kind: static-tests
    pipelineRef: php-static-tests
    queueClass: parallel
    label: "Tests statiques"
    params:
      repo-url:     { source: config, key: forge.mse.sshUrl }
      revision:     { source: input, required: true }
      commit:       { source: input, required: true }
    uploads:                       # → paramètre upload-urls du PipelineRun
      junit-unit:  { family: unit,   format: junit-xml }
      phpstan:     { family: static, format: phpstan-json }
      cs-fixer:    { family: static, format: cs-fixer-json }
    workspaces:
      - name: git-credentials
        secret: { secretName: git-credentials }
      - name: shared-data
        volumeClaimTemplate:
          accessModes: [ReadWriteOnce]
          storageClassName: "{{ .storageClass }}"
          storage: 1Gi
    podTemplate:
      imagePullSecrets: [regcred]

  - kind: integration-tests
    pipelineRef: php-integration-test
    queueClass: exclusive-mse-pipeline     # déploie dans le namespace mse-pipeline
    serviceAccountName: tekton-kubectl
    timeout: 45m
    # ...

  - kind: e2e-tests
    pipelineRef: e2e-test
    queueClass: exclusive-e2e
    timeout: 60m
    # ...

  - kind: load-tests
    pipelineRef: load-test                 # à créer, cf. lot 8
    queueClass: exclusive-cluster    # barrière : le cluster héberge aussi la cible (B16)
    # ...

  - kind: security-audit
    pipelineRef: security-audit
    queueClass: parallel
    timeout: 90m

  - kind: build
    pipelineRef: build-image
    queueClass: parallel

  - kind: promote
    pipelineRef: promote-image
    queueClass: exclusive-registry
```

## 6.3 File d'exécution

Tekton n'a pas de file d'attente : soumettre dix `PipelineRun` les fait tous démarrer.
Or les tests E2E et de charge ne supportent pas la concurrence — ils s'exécutent contre
un environnement partagé —, et le pipeline d'intégration déploie dans le namespace
`mse-pipeline`, qu'il faudrait sinon partager entre deux exécutions.

### Classes d'exclusion

| Classe | Sémantique |
|---|---|
| `parallel` | aucune restriction, limité seulement par le plafond global |
| `exclusive-<nom>` | un seul job de cette classe en cours à la fois |
| `exclusive-cluster` | **barrière** : exclut tous les jobs, quelle que soit leur classe |

L'exclusion ordinaire est **par classe**, pas globale : un audit de sécurité peut tourner
pendant des tests E2E, deux campagnes E2E non. La classe est une donnée du catalogue, ce
qui permet de la corriger sans toucher au code si une contention imprévue apparaît.

### La barrière `exclusive-cluster`

Cas d'usage : les tests de charge. Le cluster Tekton est le même que celui de
l'environnement de test de charge ([B16](02-blocages-et-decisions.md#b16)), donc tout
job concurrent — build buildx, audit Trivy, campagne E2E — fausse la mesure.

Sémantique, en trois temps :

1. **Drainage** — le job est sélectionné, l'ordonnanceur cesse de démarrer de nouveaux
   runs, tous classes confondues.
2. **Attente** — il attend la fin des runs en cours. Le job reste `queued`, avec un
   motif distinct (`draining`) pour que l'interface l'explique.
3. **Exécution** — seul dans la file, jusqu'à sa complétion.

Une file qui bloque sans dire pourquoi produit systématiquement des relances inutiles :
l'interface affiche « cluster réservé — N job(s) en attente » et la liste de ce qui est
retenu.

Le job enregistre par ailleurs, à son démarrage, s'il s'exécutait bien en exclusivité.
Cette information (`exclusive: true|false`, plus le nombre de jobs concurrents constatés)
est portée par le `test_results` de famille `load` : sans elle, la comparaison à la
baseline et les tendances du `BenchPanel` compareraient des mesures prises dans des
conditions différentes.

Le drainage a un coût de débit réel. Il est donc **activable par classe dans le
catalogue**, et l'arbitrage débit / justesse de mesure appartient au client.

Un plafond global (`maxConcurrentRuns`, par défaut 4) protège le cluster : les
pipelines Tekton demandent des PVC et des pods de build, et rien n'empêche
mécaniquement d'en lancer vingt.

### Ordonnanceur

Boucle unique dans le `worker`, protégée par un `Lease` Kubernetes. À chaque tick
(déclenché par `LISTEN/NOTIFY` PostgreSQL, avec un tick de sécurité toutes les 10 s) :

```sql
-- Sélection des jobs éligibles : file FIFO par priorité, en écartant
-- les classes déjà occupées.
with busy as (
  select distinct queue_class from jobs where state = 'running'
),
capacity as (
  select count(*) as running from jobs where state = 'running'
)
select j.*
from jobs j, capacity c
where j.state = 'queued'
  and (j.queue_class = 'parallel' or j.queue_class not in (select queue_class from busy))
  and c.running < $1
order by j.priority, j.requested_at
for update of j skip locked
limit 1;
```

`FOR UPDATE ... SKIP LOCKED` garantit qu'aucun job n'est pris deux fois même si
l'élection de leader échouait — ceinture et bretelles, parce qu'un double lancement de
déploiement en production n'est pas rattrapable.

La transition `queued → running` et la création du `PipelineRun` ne peuvent pas être
atomiques (base et cluster sont deux systèmes). L'ordre retenu : marquer `running` avec
le nom du `PipelineRun` prévu, valider la transaction, **puis** créer dans le cluster.
Si la création échoue, le job repasse `queued` avec `attempt + 1`. L'inverse (créer puis
marquer) produirait des runs orphelins invisibles du dashboard.

### Annulation

Annuler un job `queued` est un simple changement d'état. Annuler un job `running` pose
`spec.status: Cancelled` sur le `PipelineRun` ; Tekton arrête les pods et l'informer
constate la fin. Le dashboard n'attend pas la confirmation pour rendre la main à
l'utilisateur, mais n'affiche `cancelled` qu'une fois le cluster d'accord.

## 6.4 Suivi de l'état

Un informer sur les `PipelineRun` et `TaskRun` du namespace `mse-tekton-pipelines`
alimente le cache local et projette chaque changement en base :

- `PipelineRun` → table `runs` (statut, raison, dates, `results`)
- `TaskRun` → table `task_runs` (statut, dates, nom du pod)

Chaque projection émet un événement sur le canal SSE correspondant.

**Resynchronisation au démarrage.** Le `worker` liste tous les `PipelineRun` portant le
label `mse.dashboard/job-id`, les compare à la base et réconcilie : un job `running` en
base dont le `PipelineRun` a disparu passe `failed` avec un motif explicite ; un
`PipelineRun` terminé pendant l'indisponibilité voit son état rattrapé. Sans cette
étape, un redémarrage laisse des jobs bloqués en `running` qui gèlent leur classe
d'exclusion — c'est le mode de panne le plus probable de tout le dispositif.

## 6.5 Collecte des logs {#logs}

Les logs Tekton sont les logs des pods et disparaissent avec eux
([B5](02-blocages-et-decisions.md#b5)). Ils doivent être captés pendant l'exécution.

**Mécanisme.** Quand l'informer voit un `TaskRun` passer en cours d'exécution avec un
pod affecté, le `worker` démarre une goroutine par conteneur :

```
GET /api/v1/namespaces/{ns}/pods/{pod}/log?container={c}&follow=true&timestamps=true
```

Chaque ligne part vers deux destinations :

1. **Le direct** — publiée sur un canal en mémoire, consommée par les abonnés SSE.
   Aucune persistance : un client qui se connecte en cours de route reçoit d'abord le
   tampon déjà écrit, puis le flux.
2. **Le stockage** — accumulée dans un tampon, écrite sur MinIO par tranches
   (toutes les 5 s ou 1 Mo, au premier atteint) via un upload multipart. À la fin du
   conteneur, l'upload est finalisé, `task_runs.log_object` renseigné et `log_sealed`
   passé à `true`.

**Reprise.** Si le `worker` redémarre en cours de run, la goroutine repart avec
`sinceTime` positionné au dernier horodatage écrit — d'où `timestamps=true`. Un
recouvrement de quelques lignes est possible ; il est dédoublonné à la lecture par
comparaison d'horodatage. Perdre des lignes serait pire que d'en dupliquer.

**Volume.** Un pipeline d'intégration produit couramment plusieurs dizaines de mégaoctets.
La lecture d'un log scellé se fait par plage d'octets (`Range`), l'interface ne charge
que la fin par défaut et remonte à la demande.

### Trois écarts constatés à la mise en œuvre — *14/08/2026*

**Pas d'envoi multipart.** Une partie d'envoi multipart S3 doit peser au moins 5 Mio,
ce qui rend inatteignable la granularité de flush décrite ci-dessus : un incident
perdrait jusqu'à 5 Mio de journal. Chaque flush écrit donc un **objet complet**,
durable immédiatement, et le scellement les concatène en un objet unique avant de les
purger. La clé d'une tranche porte le rang de l'étape puis celui de la tranche, tous
deux zéro-paddés : l'ordre lexicographique des clés est l'ordre chronologique du
journal, et l'assemblage n'a rien à savoir du pipeline.

**Une goroutine par étape, et non un flux unique par conteneur.** Une étape non
démarrée fait échouer la lecture de son conteneur ; les suivre en série demanderait
d'attendre et de réessayer sur chacune. Chaque étape écrit dans son propre espace de
tranches, et l'ordre est rétabli au scellement — il est connu d'avance.

**Le direct ne passe pas par un canal en mémoire.** Le collecteur tourne dans le
`worker`, le flux SSE est servi par l'`api` : ce sont deux processus, et un canal en
mémoire ne les relie pas. Trois voies étaient possibles — faire transiter chaque ligne
par PostgreSQL `NOTIFY`, dont la charge utile est bornée à 8 000 octets ; servir le
flux depuis le `worker`, qui n'a ni serveur HTTP ni service ; ou **suivre le
stockage**, retenu.

L'`api` relit périodiquement ce que le collecteur a écrit et pousse la différence. La
latence est donc bornée par l'intervalle de flush du collecteur, non par le sondage.
Trois avantages compensent ce délai : le procédé fonctionne avec plusieurs répliques
d'`api`, il est repris **à l'octet près** par `Last-Event-ID`, et c'est le même chemin
de lecture que l'historique — il n'y a pas deux implémentations à garder d'accord.

**Un TaskRun devient terminal avant que son flux de logs soit épuisé côté client.**
Sceller à cet instant tronque la fin du journal, c'est-à-dire le message d'échec qu'on
vient chercher. Le scellement laisse donc les lectures en cours atteindre la fin du
flux, avec un délai de grâce de 30 secondes.

## 6.6 Publication et récupération des résultats

Découpage retenu et options écartées en [B14](02-blocages-et-decisions.md#b14). En
résumé : **le pipeline constate, le dashboard interprète.**

### Émission d'URLs pré-signées

Au moment de créer le `PipelineRun`, le `worker` :

1. lit les `uploads` déclarés dans le catalogue pour ce `kind` ;
2. calcule la clé de destination de chaque artefact — c'est lui qui décide du layout ;
3. mint une URL `PUT` pré-signée SigV4 par artefact, d'expiration alignée sur le
   timeout du pipeline ;
4. les passe en paramètre `upload-urls` (JSON compact).

```
raw/<pipelinerun-name>/junit-unit.xml
raw/<pipelinerun-name>/phpstan.json
raw/<pipelinerun-name>/meta/<outil>.json
raw/<pipelinerun-name>/e2e/videos/<nom>.mp4
```

La Task publie par `curl -T`. Elle n'a besoin d'aucun credential MinIO, ne choisit
aucun chemin, et ne transforme rien.

Ces URLs sont des **capacités portant un droit d'écriture** : elles sont limitées à une
clé unique, expirent avec le run, et ne sont jamais journalisées — le middleware de
journalisation masque le paramètre `upload-urls`, au même titre qu'un secret.

### Signal en direct par stdout

Le collecteur de logs (§6.5) reconnaît deux marqueurs de début de ligne et les extrait
du flux avant diffusion :

```
##MSE-META##    {"tool":"phpunit","version":"11.4.1","exit_code":1,"status":"ok"}
##MSE-SUMMARY## FAILURES! Tests: 1301, Failures: 3.
```

Ils alimentent immédiatement `test_results` en état partiel, ce qui permet d'afficher
un résultat avant la fin du run. Ils sont de l'ordre du kilo-octet : aucun risque lié à
la rotation des logs, contrairement à un payload complet
([B14](02-blocages-et-decisions.md#b14)).

Le descripteur `meta` reste produit par le pipeline parce que lui seul peut distinguer
« scanner en erreur » de « scanner à zéro finding » — distinction inobservable depuis
l'artefact, et qui est le meilleur apport du design actuel de `security-audit`.

### Traitement à la fin du run

L'informer signale la complétion. Le `worker` :

1. lit les artefacts aux clés qu'il a lui-même attribuées — pas de recherche par
   préfixe, pas de découverte ;
2. les parse (JUnit XML, JSON PHPStan, JSON cs-fixer, mochawesome, SARIF, composer
   audit) ;
3. déduplique, empreinte, applique les exceptions en vigueur **au moment du calcul** ;
4. compare au run précédent dans une transaction PostgreSQL — `status`, `first_seen`,
   `delta` ;
5. écrit `test_results` et `test_cases`, rattache à la release ;
6. copie dans le bucket `mse-dashboard` ce qui doit survivre à la purge des artefacts
   bruts ;
7. recalcule le score de qualité et le statut agrégé.

Les étapes 2 à 4 s'appuient sur le module Go extrait de
`tekton-mse-manager/pkg/security` (parsers, `fingerprint`, `dedup`, `severity`,
`sarif`, `composer`, `exceptions`), déplacé sans réécriture.

### Retraitement

Le brut étant conservé, tout l'historique est rejouable : une commande
`mse-dashboard reprocess --from <date>` reparse et réécrit les projections. C'est ce
qui permet de corriger un parseur, d'affiner un mapping de sévérité ou d'appliquer
rétroactivement une nouvelle exception — impossible tant que la normalisation était
figée à l'écriture.

### Cas de l'audit de sécurité

Pendant la transition, `sec-normalize` continue de tourner. Le dashboard, lui, consomme
`raw/` et `meta/`, **jamais** `findings.jsonl` — sinon il hériterait d'un diff calculé
sans transaction et d'exceptions figées à l'écriture.

*Les notifications Rocket.Chat que cette Task portait ont été retirées le 19/08/2026
([B14](02-blocages-et-decisions.md#b14)). Les exécutions nocturnes de `security-audit`
et de `php-integration-test` sont donc silencieuses : leur résultat se constate dans le
dashboard, et nulle part ailleurs.*

Deux propriétés du format d'origine sont conservées telles quelles : un scanner en
erreur reste visible et ne compte pas zéro finding ; un finding couvert par une
exception reste listé mais sort des agrégats. Le verdict `policy` par environnement
s'affiche sans jamais bloquer — la décision reste humaine, c'est le principe posé par le
pipeline lui-même.

## 6.7 Ce que le dashboard ne fait pas

- Il ne modifie pas les `Pipeline` et `Task` : ils appartiennent à `mse-tekton`.
- Il ne supprime pas les `PipelineRun` qu'il n'a pas créés.
- Il ne remplace pas l'`EventListener` : les push Gitea et les CronJobs continuent de
  passer par lui. Les runs qui en résultent sont observés, pas pilotés.
- Il n'exécute aucune commande dans les pods applicatifs. Le pipeline d'intégration
  le fait déjà, via `tekton-mse-manager` et le compte `tekton-kubectl` ; élargir ce
  droit au dashboard reviendrait à lui donner un accès d'exécution sur les
  environnements, sans besoin fonctionnel correspondant.
- Il ne lit pas les volumes des pipelines et ne crée pas de pod pour les lire. Les
  workspaces sont en `ReadWriteOnce` sur `local-path`, donc physiquement inaccessibles
  depuis un autre nœud, et leur PVC est ramassé avec le `PipelineRun` ; la lecture
  passerait par un pod éphémère et le droit `pods: create` dans
  `mse-tekton-pipelines`, volontairement exclu de la RBAC
  ([07 §7.3](07-securite-et-permissions.md#73-rbac-kubernetes)).
