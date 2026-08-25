# 13 — Contrat des pipelines

Ce document décrit ce qu'un pipeline doit honorer pour que le dashboard sache
l'exploiter. Il s'adresse à qui écrit un pipeline, sans lire le code du dashboard.

Il est **normatif**. Les pipelines livrés — noyau et implémentations de référence — en
sont des réalisations, pas la définition : un pipeline conforme écrit ailleurs vaut le
leur.

Le respecter donne, sans code supplémentaire côté dashboard : le suivi d'exécution, les
journaux, l'ingestion des résultats, le score de qualité, la comparaison à l'exécution
précédente, et l'enchaînement automatique après une construction.

---

## 13.1 Les quatre canaux

Un pipeline parle au dashboard par quatre canaux, et rien d'autre.

| Canal | Sens | Volume | Quand |
|---|---|---|---|
| **Paramètres** | dashboard → pipeline | petit | à la création |
| **Marqueurs de flux** | pipeline → dashboard | kilo-octets | pendant l'exécution |
| **Artefacts** | pipeline → dashboard | méga-octets | pendant l'exécution |
| **Statut Tekton** | pipeline → dashboard | — | à la fin |

Aucun autre chemin n'existe. Un pipeline n'appelle pas l'API du dashboard, ne connaît pas
sa base, et n'a pas besoin d'identifiants pour publier ([B14]).

---

## 13.2 Paramètres

Le dashboard fournit les paramètres que le catalogue déclare. Trois sources, qui ne se
mélangent pas :

| Source | Origine | Exemple |
|---|---|---|
| `input` | l'utilisateur, au lancement | `revision`, `image-tag` |
| `config` | la configuration du dashboard | `repo-url`, `reg-project` |
| `literal` | le catalogue lui-même | une valeur fixe |

**Un paramètre de source `config` ou `literal` ne peut pas être fourni par une demande.**
C'est ce qui empêche de détourner un pipeline vers un autre dépôt que celui prévu. Un
paramètre que le catalogue **ne déclare pas** est en revanche transmis tel quel, ce qui
rend possible une exécution personnalisée.

Un paramètre déclaré `required` et non fourni fait refuser la mise en file, avec son nom —
plutôt que d'échouer en cours d'exécution sur un message qui ne le désigne pas.

---

## 13.3 Marqueurs de flux

Deux informations doivent remonter **avant la fin du run** : le descripteur d'exécution
d'un outil, et sa ligne de résumé. Elles sont trop petites pour justifier un artefact, et
trop utiles pour attendre — devant une campagne de quarante minutes, on veut lire
« PHPStan : 12 violations » dès que PHPStan a fini.

Elles sont écrites **sur la sortie standard**, une par ligne, préfixées.

### Le jeton du marqueur est un réglage

La forme générale est `##<JETON->META##` et `##<JETON->SUMMARY##`, où le jeton est celui
du déploiement. Sans jeton déclaré, la forme neutre s'applique — `##META##` et
`##SUMMARY##` —, et le déploiement MSE déclare `MSE`, ce qui rend les formes historiques
`##MSE-META##` et `##MSE-SUMMARY##` employées dans les exemples ci-dessous.

Le nom d'un projet dans un **protocole** est un couplage d'une autre nature que dans une
interface : un pipeline générique qui émet `##MSE-META##` reste marqué MSE dans son code
source, quelle que soit la propreté de ce qui le lit. D'où le jeton (doc 12 §12.9,
chantier 5).

Les deux marqueurs d'un même protocole portent nécessairement le **même** jeton : un
`##ACME-META##` accompagné d'un `##SUMMARY##` serait accepté par personne, et rien dans
les données ne dirait qu'ils appartiennent au même flux.

### Descripteur — `##MSE-META##`

```
##MSE-META## {"tool":"phpstan","version":"1.10.3","exit_code":1,"status":"ok"}
```

| Champ | Obligatoire | Sens |
|---|---|---|
| `tool` | oui | identifiant de l'outil, stable d'un run à l'autre |
| `version` | non | version de l'outil, pour expliquer une variation de résultat |
| `exit_code` | oui | code de sortie **de l'outil** |
| `status` | oui | `ok` si l'outil a conclu, quelle qu'en soit l'issue |
| `error` | non | message quand `status` ≠ `ok` |

**`exit_code` et `status` ne disent pas la même chose, et c'est le cœur du canal.**
PHPStan sort en `1` dès qu'il trouve une violation : c'est un **succès de l'outil** et un
échec du code. Le pipeline est seul à pouvoir faire la différence entre « l'outil a
trouvé des problèmes » et « l'outil n'a pas pu tourner » — les deux produisent zéro
résultat exploitable, et les confondre ferait passer un scanner planté pour un code sain.

Un descripteur **absent** vaut « n'a pas conclu ». Le signal correspondant devient *non
mesuré* et non *conforme* : se tromper dans ce sens affiche « non mesuré » sur un outil
qui a tourné, ce qui est corrigible ; dans l'autre, on afficherait « conforme » sur un
scanner qui a planté.

### Le `tool` du descripteur nomme le même outil que son artefact

Le descripteur d'un outil qui **échoue** est la seule trace qu'il laisse : il ne publie
alors aucun artefact. Le dashboard en crée un résultat `na`, et le nom qu'il y inscrit est
celui du champ `tool`.

Ce nom doit donc être **exactement** celui sous lequel l'artefact du même outil est
enregistré quand il réussit. Sinon le même outil apparaît sous deux noms selon son issue —
un signal « conforme » sous l'un, un « non mesuré » sous l'autre — et l'écran d'une release
laisse croire à deux outils dont l'un ne tourne jamais.

C'était le cas : la Task `cs-fixer` émettait `php-cs-fixer` alors que son rapport est rangé
sous `cs-fixer`.

### Ce qui n'émet pas encore

`security-audit` n'émet aucun descripteur. C'est le pipeline où l'absence coûte le plus :
un scanner de vulnérabilités mort rend zéro constat, exactement comme un dépôt sain.

### Résumé — `##MSE-SUMMARY##`

```
##MSE-SUMMARY## PHPStan : 12 violations sur 340 fichiers
```

Texte libre, une ligne, destiné à l'œil. Il apparaît dans l'interface pendant que le run
continue.

### Ce que le canal ne transporte pas

Les journaux du kubelet subissent une rotation : ce canal n'est pas fiable au-delà du
kilo-octet. Tout payload volumineux passe par un artefact.

---

## 13.4 Artefacts

### Déclaration

Le catalogue déclare, pour chaque artefact attendu :

```yaml
uploads:
  junit-unit:
    param: results-upload-url    # le paramètre qui recevra l'URL
    artifact: junit.xml          # le nom de l'objet, extension comprise
    family: unit                 # la famille de résultats
    format: junit-xml
```

Le dashboard calcule la clé de stockage **avant** de créer le run et fournit une **URL
pré-signée** dans le paramètre nommé. Le pipeline y écrit par un `PUT`, sans identifiant,
sans connaître le bucket ni le préfixe ([B14]).

**Un artefact non déclaré ne reçoit pas d'URL**, et sa publication est ignorée. Un
pipeline qui produit plus que ce que le catalogue annonce ne doit pas échouer pour
autant.

Une URL vide est un cas normal : un run déclenché hors dashboard n'a personne pour lui en
délivrer une. Le pipeline doit alors **continuer sans publier**, jamais échouer.

### Familles reconnues

| Famille | Contenu |
|---|---|
| `unit` | tests unitaires |
| `integration` | tests d'intégration |
| `e2e` | tests de bout en bout |
| `static` | analyse statique — violations, pas cas de test |
| `security` | constats d'audit |
| `load` | mesures de charge |

### Formats reconnus

| Format | Familles | Produit par |
|---|---|---|
| JUnit XML | `unit`, `integration` | PHPUnit, et tout outil qui l'émet |
| Mochawesome JSON | `e2e` | Cypress |
| PHPStan JSON | `static` | PHPStan |
| php-cs-fixer JSON | `static` | php-cs-fixer |
| `summary.json` + `findings.jsonl` | `security` | normalisation d'audit |
| Résumé k6 JSON | `load` | k6 `--summary-export` |

Ces formats sont des **formats d'outils**, pas des conventions du dashboard. Un projet
qui produit du JUnit avec un autre langage est exploitable sans rien ajouter.

### Convention d'arborescence

Dans une archive, le **répertoire** nomme la famille :

```
test-results/unit/junit.xml
test-results/integration/junit.xml
test-results/phpstan/report.json
test-results/cs-fixer/report.json
```

Le répertoire est consulté **avant** le nom du fichier. C'est délibéré : toutes les Tasks
nomment leur rapport `junit.xml`, et se fier au nom faisait tomber le rapport
d'intégration dans la règle des tests unitaires — la famille `integration` n'était jamais
mesurée, et son échec rapporté comme un échec unitaire.

Un fichier non reconnu est ignoré en silence : une archive contient aussi des journaux et
des captures, et échouer dessus perdrait les rapports qui l'accompagnent.

---

## 13.5 Construction d'image

### La version est inscrite dans l'image

Le pipeline reçoit la version et la passe en argument de construction ; le Dockerfile
l'écrit dans un fichier que l'application lit pour se déclarer :

```dockerfile
ARG VERSION=latest
ENV VERSION=${VERSION}
RUN echo "${VERSION}" > /app/version.txt
```

**Écrire `version.txt` dans le contexte de build est sans effet** : le `RUN echo`
l'écrase. C'est le `--build-arg` qui fait foi.

Sans cette inscription, le dashboard ne peut pas répondre à « quelle version tourne
réellement » autrement qu'en supposant que le tag déployé est celui qui tourne. La
différence entre savoir et supposer est ce que cette ligne achète ([P10]).

### Une exécution, une image

Le pipeline de construction produit **une** image. Un appelant qui en veut trois lance
trois exécutions.

Trois raisons, constatées : ajouter une image ne demande alors pas de modifier le
pipeline ; l'échec de l'une ne fait pas échouer les autres, et l'on sait laquelle ; et
elles peuvent tourner en parallèle, ce qu'un workspace partagé interdisait.

Le nom de l'image et la cible du Dockerfile sont des **paramètres**. Le tag est
déterministe — imposé par l'appelant quand il le fournit — donc les images d'une même
release le partagent sans avoir à s'enchaîner.

### Promouvoir ne reconstruit jamais

Poser un tag supplémentaire sur une image déjà publiée se fait par **re-tag du même
digest**. Reconstruire produirait un artefact différent — horodatages, ordre des couches —
et livrerait autre chose que ce qui a été validé ([B17]).

---

## 13.6 Déploiement d'un environnement de test

Un pipeline qui déploie une kustomization reçoit la liste des images à substituer. Une
entrée qui ne correspond à rien dans ce qui est déployé est **ignorée**, jamais une
erreur : cela permet de passer la liste complète, qu'on déploie l'application réelle ou
une kustomization de test qui n'en utilise qu'une partie.

---

## 13.7 Ce qui n'est pas dans le contrat

Volontairement absent, et qui relève du catalogue ou du projet :

- **le nom des paramètres** — `revision`, `app-revision`, `mse-revision` désignent la même
  chose sous trois noms ; le catalogue fait la correspondance ;
- **le nom des images** et les cibles du Dockerfile — décrits par le projet ;
- **l'enchaînement** — quels pipelines suivent une construction réussie ;
- **la pondération du score** — configuration, jamais dérivée du code ([B11]).

---

## 13.8 Éprouver un pipeline

Un pipeline conforme se vérifie sans le dashboard :

1. **Marqueurs** — `grep '##MSE-' ` sur les journaux rend au moins un descripteur par
   outil, chacun du JSON valide portant `tool`, `exit_code` et `status`.
2. **Artefacts** — chaque `upload` déclaré donne un objet à la clé annoncée. Un `PUT` sur
   l'URL pré-signée retourne `200`.
3. **URL absente** — le pipeline lancé sans URL d'upload **aboutit** au lieu d'échouer.
4. **Version** — `docker run --rm <image> cat /app/version.txt` rend le tag demandé, et
   non `latest`.
5. **Familles** — l'arborescence publiée place chaque rapport sous le répertoire de sa
   famille.

Le point 3 est celui qu'on oublie, et il se manifeste par des CronJobs qui échouent tous
les soirs sans que personne ne comprenne pourquoi.

---

## 13.9 Écarts connus

Ce que ce document décrit et que le code ne fait pas. Écrit ici plutôt que tu, pour qu'un
pipeline conforme ne soit pas soupçonné quand l'effet attendu n'apparaît pas.

Le canal de marqueurs y figurait en entier — émis, lisible, sans le moindre appelant. Il
est **raccordé** depuis : à la conclusion d'un run, les journaux de ses tasks sont scannés,
et tout outil dont le descripteur ne dit pas `ok` produit un résultat `na` porteur de sa
cause. Le score sort alors ce signal du calcul et redistribue son poids, au lieu de compter
un zéro qui serait faux ([B11]). Ce qui reste ci-dessous est plus étroit, et l'un des deux
points est un refus assumé plutôt qu'un manque.

### Les lignes de marqueur restent visibles dans le journal

`MarkerSet.StripMarkers` existe et n'a **aucun appelant**, délibérément.

Le journal affiché n'est pas recomposé : il est servi par plages depuis l'objet scellé, et
la numérotation absolue compte les sauts de ligne de cet objet-là. Retirer des lignes à
l'affichage décalerait chaque numéro après le premier marqueur, et un lien `#L275` partagé
ne désignerait plus la même ligne selon qu'il a été produit avant ou après le filtrage.

Le prix de les laisser est faible — quelques lignes de protocole dans un flux qui en
compte des milliers. Le prix de les retirer est un système d'ancrage qui ment. Le filtrage
resterait possible côté visionneuse, comme un affichage optionnel qui ne touche ni aux
décalages ni aux numéros ; ce n'est pas fait, et ce n'est pas prévu.

### Le résumé n'apparaît pas pendant l'exécution

`##SUMMARY##` est lu — `Markers.Summaries` le rend — mais rien ne l'affiche en cours de
run. Le descripteur, lui, est exploité à la **conclusion** du run : c'est ce qui distingue
un outil planté d'un outil qui n'a rien trouvé.

Ce qui manque est donc l'immédiateté, pas l'information : devant une campagne de quarante
minutes, on aimerait lire « PHPStan : 12 violations » dès que PHPStan a fini plutôt qu'à la
fin de tout. Cela demande de scanner un journal **en cours** et de pousser le résultat par
le flux d'événements — un travail de diffusion, distinct de celui d'ingestion qui est fait.

[B11]: 02-blocages-et-decisions.md#b11
[B14]: 02-blocages-et-decisions.md#b14
[B17]: 02-blocages-et-decisions.md#b17
[P10]: 10-prerequis-mse-tekton.md
