# 15. Construire et tester par déclaration

> Sortir du code la décision de **quoi construire** et **quoi tester**, pour la poser dans
> l'onglet automatisation.

---

## 15.1 Ce qui est en dur aujourd'hui

Trois endroits, et il vaut mieux les nommer avec précision : ce sont eux qu'on retire.

**1. Trois images, à la création d'une release.**
[`internal/http/releases.go:1470`](../internal/http/releases.go) — `enqueueBuild` cherche le
pipeline `build` du catalogue, parcourt `project.Buildable()` — la colonne `targets`, une
liste de couples *étape de Dockerfile → nom d'image* — et enfile un job par cible avec sept
paramètres écrits en clair : `revision`, `version`, `tag-name`, `dockerfile`, `context`,
`image-name`, `target`.

Le modèle est donc **« un projet = un Dockerfile à étapes »**. Un projet qui produirait un
binaire, une archive ou une image par un autre moyen n'entre pas.

**2. La chaîne de vérification, après une construction réussie.**
[`internal/worker/verificationplan.go`](../internal/worker/verificationplan.go) — le plan est
composé en Go depuis `catalog.VerificationKinds()`, c'est-à-dire les pipelines portant
`verification: true`, filtrés par ce que le projet active. Le drapeau lui-même annonçait sa
fin : « Ce drapeau disparaîtra le jour où l'enchaînement deviendra un plan déclaré. »

**3. La pondération du score.**
[`internal/results/score.go:52`](../internal/results/score.go) — six signaux, six poids en
dur, valeurs de repli d'une ConfigMap.

---

## 15.2 Le principe : une release ouvre **un** plan

Aujourd'hui la vie d'une release est une suite de sauts entre deux mondes : l'API enfile les
constructions, l'observateur constate qu'elles ont abouti, émet un fait, et le moteur ouvre
*un second* plan pour les vérifications.

Demain, **un seul plan**, ouvert par `release.created`, dont les étapes sont celles que le
projet a déclarées :

```
étape 1 — Construire      : build(portal) ∥ build(portal-cli) ∥ build(portal-dev)
étape 2 — Vérifier        : unit-tests ∥ static-tests ∥ e2e-tests ∥ security-audit
   (si l'étape précédente a réussi)
```

Le moteur sait déjà faire cela : les étapes s'exécutent en séquence, les gestes d'une étape en
parallèle, `when: success` conditionne la suite. **Rien à ajouter au modèle de plan.**

Ce que ça retire est plus intéressant que ce que ça ajoute : l'aller-retour par un fait
intermédiaire disparaît, et avec lui la question « pourquoi mes tests n'ont pas démarré »
dont la réponse vivait dans trois fichiers.

---

## 15.3 La réduction des faits

La proposition est de ne garder, hors plugin, que **la création d'une version** et **la
création d'une release**.

### La règle qui la justifie

Un fait existe quand **quelque chose arrive qu'aucun plan n'a causé**. Ce qu'un plan
provoque lui appartient : il n'a pas à ressortir dans le fil des faits pour y rentrer
aussitôt.

Appliquée aux six faits actuels :

| Fait | Sort | Pourquoi |
|---|---|---|
| `release.created` | **reste** | point d'entrée : une décision humaine |
| `version.created` | **à créer** | point d'entrée : le suivi déclare une version — aujourd'hui produit par la sonde Redmine sous `redmine.version.created` |
| `release.built` | **part** | étape interne d'un plan |
| `release.verified` | **part** | étape interne d'un plan |
| `pipeline.finished` | **part** | étape interne ; `when: failure` couvre le besoin |
| `release.promoted` | *à trancher* | promotion : décision hors plan |
| `release.released` | *à trancher* | validation de recette : décision humaine hors plan |

### Là où je ne suis pas d'accord avec la proposition

**Garder `release.promoted` et `release.released` coûte presque rien et retire une
capacité s'ils partent.** Ce ne sont pas des étapes de plan : ce sont une promotion et une
validation de recette, décidées par quelqu'un, hors de toute automatisation. Ce sont
exactement les deux moments où une équipe veut être prévenue.

Les retirer supprimerait « prévenir la messagerie quand une release est livrée », qui est le
cas d'usage le plus évident de tout ce chantier.

Ma recommandation : **quatre faits du noyau** — `version.created`, `release.created`,
`release.promoted`, `release.released` — et non deux. La règle reste la même ; c'est son
application à ces deux-là qui diffère.

**Tranché : quatre faits.** `version.created`, `release.created`, `release.promoted`,
`release.released`.

> **Révisé par doc 16 (décision 7).** `release.promoted` est devenu `mse.release.promoted`,
> fait du plugin MSE : c'est lui qui définit ce qu'est une promotion. Le noyau compte trois
> faits. Le besoin de prévenir une équipe reste couvert : une réaction écoute le fait du plugin.

### Ce que la réduction impose ailleurs

`release.verified` ne servait pas qu'à déclencher : il faisait **avancer l'état de la
release** de `verifying` vers `ready`.

Ma première proposition — « c'est la conclusion du plan qui avance la release » — était
**fausse**, et il vaut mieux dire pourquoi. Elle confondait *l'automatisation est terminée*
avec *la release est vérifiée*. Ajoutez une troisième étape de notification, et un webhook
en échec ferait échouer la release. L'ancrage doit être plus précis que « le plan ».

### L'ancrage : le rôle d'une étape

Une étape déclare ce qu'elle **est** :

```yaml
stages:
  - role: build
    label: Construire
    gestes: [build(portal), build(portal-cli), build(portal-dev)]
  - role: test
    label: Vérifier
    gestes: [unit-tests, static-tests, e2e-tests]
  - label: Prévenir          # sans rôle
    gestes: [webhook]
```

- la conclusion de la dernière étape `build` fait passer `building` → `verifying` ou
  `failed` ;
- la conclusion de la dernière étape `test` fait passer `verifying` → `ready` ou `failed` ;
- **une étape sans rôle ne touche à rien**. C'est elle qui répare le défaut ci-dessus : une
  notification qui échoue n'a jamais à faire échouer une release.

#### Pourquoi l'étape, et pas un geste composite

La proposition initiale était de grouper les pipelines dans un geste `build` et un geste
`test`, dont l'exécution avancerait le statut. **Le mécanisme est le bon** — un point
d'ancrage nommé, plus fin que « le plan » — et c'est celui retenu. Seul son emplacement
change, pour une raison de modèle.

Un geste composite introduirait une **imbrication** : une étape contient des gestes, et un
geste contiendrait des pipelines. Or l'étape *est déjà* le groupe : elle rassemble des gestes
parallèles, et le moteur calcule déjà sa conclusion. Le geste composite referait, un niveau
plus bas, ce que l'étape fait déjà — et doc 14 avait gardé le modèle plat délibérément.

Le rôle est donc **un champ sur l'étape**, et rien d'autre ne bouge : ni le moteur, ni la
table des exécutions, ni l'écran des exécutions.

À l'écran, l'éditeur présentera bien deux blocs « Construction » et « Vérification » comme
demandé — c'est une question de rendu, pas de structure.

#### Le rôle est vérifié, pas cru sur parole

Une étape `role: build` dont un geste ne produit pas d'artefact est **refusée à
l'enregistrement**. Sans ce contrôle, ajouter une action à une étape de construction
changerait silencieusement le cycle de vie d'une release. Le catalogue déclare déjà
`produces` : le contrôle est une comparaison, pas une heuristique.

---

## 15.4 Les artefacts, et leur passage d'une étape à l'autre

C'est la partie neuve, et la seule qui demande un mécanisme.

### Constater ce qu'une construction a produit

Un pipeline de construction rend déjà ses résultats Tekton — le pipeline `build-image`
déclare `image` (référence complète, tag compris) et `tag`. Aujourd'hui personne ne les lit.

Proposition : à la conclusion d'un geste, le moteur lit les résultats du run et enregistre
les **artefacts** dans une table dédiée, rattachés à l'exécution *et* à la release.

```
plan_artifacts(plan_run_id, step_stage, step_position, name, kind, reference, release_id)
```

`kind` reprend le vocabulaire déjà posé par `Produces` : `image`, `results`. `reference` est
ce qui désigne l'artefact — pour une image, `reg.cadoles.com/vcarroy/portal:3.2.0-rc8`.

### Le consommer à l'étape suivante

Un geste de test doit pouvoir viser l'image que l'étape précédente a publiée. La forme :

```
image: {artifacts.portal.image}
```

**Ce n'est pas un langage d'expressions**, et la distinction compte : c'est une **recherche**
dans une carte, exactement comme `{tag}` ou `{git_commit}`. Pas d'opérateur, pas de
condition, pas d'appel. Le moteur compose la carte des artefacts des étapes déjà conclues et
l'ajoute au contexte ; `action.Expand` fait le reste, sans une ligne de plus.

La clé — `portal` ci-dessus — est le **nom du geste** qui l'a produit. Deux gestes de
construction dans la même étape produisent donc deux entrées distinctes, ce qui est
précisément ce dont on a besoin quand on construit trois images.

### Ce qui reste à trancher

Un geste qui référence un artefact absent doit-il **échouer** ou **être sauté** ? Je penche
pour échouer, avec un motif qui nomme l'artefact manquant : un test qui ne tourne pas
silencieusement est un test qu'on croit passé.

---

## 15.5 Déclarer une construction

L'onglet automatisation gagne les gestes de type **pipeline de construction**. Le formulaire
se compose comme celui des actions : depuis ce que le catalogue déclare.

Cela suppose une chose que le catalogue ne fait pas encore : **déclarer ses paramètres avec
leur libellé et leur aide**, comme `plugin.ActionField` le fait pour les actions. Aujourd'hui
il n'y a que le mécanisme de `suggest` employé par `VerificationInputs`. C'est une extension
étroite et sans surprise.

Une fois cela posé, déclarer « construire l'image `portal` depuis l'étape `web` » est une
saisie, et non une colonne `targets`.

---

## 15.6 Le score, pipeline par pipeline

### Ce qui change

Le score est aujourd'hui calculé sur **six signaux** de poids fixes. La demande est de
pondérer **chaque pipeline de test** déclaré.

Proposition : le poids se déclare **sur le geste**, et le score se calcule sur les gestes.
Le `Signal` ne disparaît pas — il reste ce qui dit au parseur quoi extraire d'un artefact de
résultats (doc 13) — mais il **cesse d'être l'axe de pondération**.

Conséquence directe et voulue : deux campagnes E2E distinctes pèsent chacune leur poids, ce
que le modèle actuel ne sait pas exprimer.

### Un drapeau levé

Les poids actuels — 25/20/20/10/15/10 — sont désignés dans le code comme **« la pondération
acceptée par le client (B11) »**. Les rendre libres par projet **déplace une valeur
contractuelle vers une saisie d'interface**.

**Tranché : la saisie passe en formulaire.** Deux conséquences à tenir, puisque la décision
est prise en connaissance de cause :

- les poids d'origine restent le **défaut** d'un projet qui n'a rien saisi, et ce défaut
  garde sa référence à B11. Personne ne perd la pondération convenue en ne faisant rien ;
- un plan **figé** garde les poids qui ont servi à calculer son score. Rejouer le score d'une
  release livrée il y a six mois avec les poids d'aujourd'hui réécrirait un chiffre qui a
  été communiqué.

---

## 15.7 L'écran d'une release

Deux sections deviennent dynamiques.

**Les artefacts** remplacent la section « images ». Elle liste ce que l'automatisation a
réellement produit, avec le geste qui l'a produit — et non ce que la colonne `targets`
laissait attendre. Un projet qui ne construit pas d'image y voit ses binaires.

**Les résultats de tests** cessent d'être six cases figées. Une case par geste de test
déclaré, avec son poids et sa contribution. Un pipeline ajouté à l'automatisation fait
apparaître sa case ; retiré, elle disparaît de la release suivante — mais **reste sur les
releases passées**, dont le plan est figé (c'est déjà la règle des exécutions de plan).

---

## 15.8 La migration depuis `project.targets`

Les projets existants portent leurs cibles en colonne. Le passage doit être silencieux :

1. une migration lit `targets` et compose, pour chaque projet, une automatisation par
   défaut — une étape de construction, une étape de vérification reprenant les pipelines que
   le projet activait ;
2. `targets` cesse d'être lu, et la colonne reste le temps de vérifier que rien ne
   régresse ;
3. l'écran des paramètres généraux perd sa section « cibles », qui pointe désormais vers
   l'onglet automatisation.

Sans cette migration, tous les projets cessent de construire au déploiement — ce qui vient
d'arriver pour une autre raison, et qu'on ne va pas refaire.

---

## 15.9 Tranches

| # | Tranche | Ce qu'elle livre | Porte |
|---|---|---|---|
| 1 | **Le plan d'une release** | `release.created` ouvre un plan à deux étapes composé depuis `targets` et le catalogue. Rien de déclaré encore : on prouve que le plan unique remplace les deux chemins. | **oui** — si l'enchaînement construction → test n'entre pas dans un plan unique, tout le reste tombe |
| 2 | **Les artefacts** | Lecture des résultats Tekton, table `plan_artifacts`, `{artifacts.<geste>.<nom>}` dans le contexte | **oui** — le passage d'artefact est le seul mécanisme neuf |
| 3 | **La réduction des faits** | Retrait des faits internes, `version.created`, machine à états branchée sur la conclusion du plan | |
| 4 | **Déclarer les constructions** | Paramètres déclarés au catalogue, éditeur, migration depuis `targets` | |
| 5 | **Le score par geste** | Poids sur le geste, calcul par geste, cases dynamiques | dépend de l'accord sur B11 |
| 6 | **L'écran d'une release** | Artefacts et résultats dynamiques | |

Les deux premières sont des **portes**, au sens de doc 14 §14.12 : elles se traitent comme
telles, et si elles ne passent pas on révise avant d'aller plus loin.

---

## 15.10 Décisions à prendre avant d'écrire

| # | Question | Réponse |
|---|---|---|
| 1 | Deux faits du noyau, ou quatre ? | **quatre** — tranché. `release.promoted` et `release.released` restent : décisions hors plan, et cas d'usage principal des notifications (§15.3) |
| 2 | Les poids du score deviennent-ils une saisie par projet ? | **oui** — tranché, en connaissance de la portée contractuelle. Défaut B11 conservé, poids figés dans un plan exécuté (§15.6) |
| 3 | Comment la release avance-t-elle sans `release.verified` ? | par le **rôle d'une étape**, `build` ou `test`. Pas par la conclusion du plan, qui ferait échouer une release sur une notification ratée (§15.3) |
| 4 | Un geste dont l'artefact manque échoue-t-il ou est-il sauté ? | **échoue**, en nommant l'artefact : un test sauté en silence se lit comme un test passé (§15.4) |
| 5 | La colonne `targets` est-elle retirée tout de suite ? | **non** : conservée non lue le temps d'un déploiement, pour pouvoir revenir en arrière sans migration inverse (§15.8) |
| 6 | Un projet sans automatisation déclarée construit-il encore ? | **non**, et c'est le sens du chantier — mais la migration §15.8 fait qu'aucun projet existant n'y arrive par surprise |

---

## 15.11 Ce que la réduction a tranché à l'écriture

La §15.3 disait quels faits partent. Trois questions restaient ouvertes, tranchées en
écrivant la tranche 3.

### `version.created` est du noyau, et pourtant observé par un plugin

La sonde du suivi le produit. Il aurait donc pu rester `redmine.version.created`, préfixé
comme le veut doc 14. Il ne l'est pas, pour une raison qui tient en une phrase : **tous les
suivis déposent leurs versions dans la même table**, et une réaction déclarée sur « une
version existe » n'a pas à être réécrite le jour où le client change d'outil.

Deux conséquences.

`Event.Source` change de sens. Il disait « du noyau, ou de tel plugin » ; il dit désormais
**qui l'a constaté**. Un fait du noyau peut porter `redmine`. Ce n'est plus le préfixe du
type écrit deux fois, et l'écran des faits le montre : `version.created` s'affiche avec sa
sonde.

La clé de déduplication porte la **ligne locale**, `version.created:<id>`, et non plus le
projet du suivi et l'identifiant Redmine. C'est ce qui rend le fait indépendant de l'outil :
une instance qui migrerait ne reconstaterait pas tout son historique sous des identifiants
neufs — et n'enverrait pas une notification par version. L'outil et son identifiant vivent
dans la charge, où ils servent à remonter à la fiche.

La demande close, elle, reste `redmine.issue.closed` : elle n'a pas d'équivalent au noyau, et
rien ne justifie de l'y faire monter.

### Le type `Outcome` part avec les faits qui le portaient

Aucun des quatre faits restants ne se termine d'une façon ou d'une autre. Un point d'entrée
**arrive** ; ce qui conclut, ce sont les étapes d'un plan, et leur issue se lit dans
l'exécution.

Cela retire une capacité qu'il vaut mieux nommer : `state: {outcome}` sur un statut de forge
n'a plus de source. Une réaction qui veut refléter une issue devra la lire là où elle est —
sur l'exécution —, ce qu'aucune action ne sait faire aujourd'hui. C'est acceptable parce que
la condition d'étape `when: failure` couvre le besoin **à l'intérieur** d'un plan, qui est
là où il se pose.

### Le contrat de résultats perd son seul débouché

`checkResultsContract` signale un pipeline qui annonce des résultats et n'en laisse aucun.
Son affichage devait passer par `pipeline.finished`. Ce fait est parti, et rien ne l'a
remplacé : le signalement vit dans le journal, et l'écran d'un run ne dit toujours pas
pourquoi il est vide.

L'écart était déjà ouvert — il n'est pas créé ici, il est **reconduit**. Il se refermera avec
l'écran d'une release (tranche 6), qui a de toute façon à dire ce que chaque geste a produit.

### Ce qui n'a pas eu besoin d'être fait

Aucune migration. Une réaction déclarée sur un fait retiré reste lisible en base et cesse
simplement de se déclencher — c'est le comportement que 0047 documentait pour un plugin
disparu, et l'écran l'affiche **inerte, en le disant**. La base de développement n'en portait
aucune ; le fil, lui, garde ses lignes, et l'interface garde leurs libellés pour que
l'histoire d'avant la réduction reste lisible.

---

## 15.12 Ce que la déclaration a tranché à l'écriture

La §15.5 disait qu'il fallait déclarer les constructions. Trois choses ont été décidées en
l'écrivant.

### Le catalogue disait déjà d'où vient chaque valeur

Le plan posait `revision`, `version` et `tag-name` **en clair**, derrière un
`if geste.Ref == "build"`. Ces trois noms étaient la recopie de ce que le catalogue déclare
par ses `suggest` — il suffisait de le lire.

Une **origine** par entrée le remplace, et elle se déduit du `suggest` sans déclaration
supplémentaire :

| Origine | Qui gagne | Pour quoi |
|---|---|---|
| `context` | l'exécution, toujours | la révision et le tag. Deux gestes d'une même release qui viseraient deux commits publieraient deux images sous le même tag (B17) |
| `fallback` | la déclaration si elle dit quelque chose | une référence d'image. Sans déclaration, elle se recompose depuis le registre, comme avant |
| `declared` | la déclaration seule | le dépôt à publier, la cible du Dockerfile, l'environnement visé |

Conséquence directe : **la famille `build` n'apparaît plus dans le plan d'une release**. Il
ne reste aucun nom de pipeline en dur dans ce chemin.

### La reprise, et pourquoi elle n'est pas une migration SQL

Composer ce plan en SQL demanderait de recopier la règle du Dockerfile résolu, les familles
de vérification et le filtre par projet — trois choses qui vivent dans le binaire, dont une
dans une ConfigMap. La reprise appelle donc le **même code** qui composait le plan jusqu'ici :
le plan repris est identique à celui qui tournait, par construction et non par relecture.

Elle tourne dans le worker, après la validation du catalogue contre le cluster — figer depuis
un catalogue en désaccord écrirait en base des plans qui ne partiraient jamais, et une reprise
ne repasse pas.

**Le critère est l'absence de ligne, jamais un plan vide.** Un projet dont quelqu'un a vidé
l'automatisation a une ligne : il ne construit plus, et c'est une décision. Sans cette
distinction, vider l'automatisation la ferait réapparaître au redémarrage suivant.

### L'artefact est visé là où il existe, et pas ailleurs

La §15.4 veut qu'un geste de test vise `{artifacts.portal.image}`. La reprise l'écrit —
**mais seulement quand le projet construit effectivement cette image**.

Ailleurs, elle laisse le repli recomposer la référence depuis le registre, comme la veille.
Viser un artefact que personne ne produit ferait échouer le geste en le nommant, ce qui est
juste sur le fond ; mais une reprise ne doit rien casser en passant, et un plan qui refuse de
partir là où il partait hier est une régression, pas une amélioration.

C'est aussi ce qui donne enfin un **usage réel** au passage d'artefact posé en tranche 2 :
l'audit de sécurité analyse l'image que la construction vient de publier, et non une
référence recomposée qui peut désigner une image que personne n'a poussée.

---

## 15.13 Ce que `targets` décide encore

La §15.8 prévoyait que la colonne cesse d'être lue. **Elle l'est encore**, et il vaut mieux
le dire : `Promotable()` s'en sert pour savoir quelles images un re-tag propage. La
construction n'en dépend plus, la promotion oui.

L'écran des paramètres généraux garde donc sa section, avec une phrase qui dit ce qu'elle ne
décide plus. La retirer aurait rendu la promotion inconfigurable ; la laisser muette aurait
fait croire qu'on y modifie ce qui se construit.

Ce qui reste à faire un jour : donner à la promotion sa propre déclaration. Ce n'est pas ce
chantier-ci, et l'y ajouter l'aurait fait déborder.

---

## 15.14 Ce que le score par geste a tranché à l'écriture

La §15.6 posait le principe : le poids se déclare sur le geste, le score se calcule sur les
gestes. Quatre choses ont été décidées en l'écrivant.

### Le poids voyage avec la mesure

`Score(measures, weights)` cherchait le poids d'une mesure **dans une table indexée par
signal**. C'était exactement ce qui empêchait deux campagnes E2E de compter séparément : deux
mesures de même signal trouvaient forcément le même poids.

La table a donc disparu de la signature. Une mesure porte son poids, et le calcul n'a plus
d'avis sur d'où il vient. `DefaultWeights` survit, réduite à ce qu'elle est vraiment : le
**défaut par signal** d'un geste qui ne déclare rien.

### Le rattachement n'a rien demandé au schéma

Un résultat porte son run, un run porte son job, et un geste est le seul à porter ce job.
Une requête suffit — `ResultGestes` — et elle rattache d'un coup les rapports de tests, les
audits de sécurité et les campagnes de charge, puisque tous passent par `test_results`.

Conséquence qui n'était pas cherchée et qu'on garde : **un résultat qu'aucun geste n'a
produit n'entre pas au score**. Il vient d'un lancement manuel depuis la file, et le compter
ferait noter une release sur une exécution que son automatisation n'a pas demandée.

### Les poids figés n'ont demandé aucune machinerie

`plan_runs.plan` porte déjà le plan tel qu'il était à l'ouverture. Le poids étant un champ du
geste, il y est figé avec le reste. Rejouer le score d'une release livrée il y a six mois
emploie les poids de ce jour-là, sans table d'historique ni copie.

### Trois états de poids, et pourquoi le pointeur

| Saisie | Ce que ça veut dire |
|---|---|
| vide | le geste pèse ce que sa nature pesait (B11). Personne ne perd la pondération convenue en ne saisissant rien |
| `0` | le geste tourne et **ne note pas**. C'est une décision, et sa case reste affichée |
| une valeur | elle s'applique, relativement aux autres |

Un `float64` nu aurait confondu les deux premiers, et vider un champ aurait retiré le geste
du score au lieu de lui rendre son défaut.

### Deux limites assumées

**L'audit et la campagne de charge se lisent « le plus récent de la release »**, puis se
rattachent au geste qui les a produits. Deux gestes de sécurité dans un même plan ne
seraient donc pas notés séparément : le second se déclarerait non mesuré, en le disant,
plutôt que d'afficher la valeur du premier sous son nom. C'est la seule forme **vraie** sans
requête nouvelle, et le cas n'existe dans aucun catalogue déployé.

**L'écran n'a pas bougé.** Les composants portent désormais une clé et un libellé, et les
cases restent celles de la maquette — six tuiles par signal. Les rendre dynamiques est la
tranche 6, et le faire ici aurait mélangé un changement de calcul avec un changement de
rendu.

---

## 15.15 Ce que l'écran a tranché à l'écriture

### La section « images » ne listait pas ce qu'on croyait

Elle ne dérivait **pas** de la colonne `targets`, contrairement à ce que §15.7 supposait.
Elle prenait **un seul dépôt** — `registry.image` dans la configuration — et le déclinait en
ses tags : construction, release, promotions.

Le défaut était donc pire que décrit. Un projet qui publie trois images n'en voyait qu'une,
sans que rien ne dise que les deux autres existaient.

### Le contrôle de digest a été conservé, et élargi

Cette section avait une raison d'être que §15.7 ne nommait pas : elle prouvait qu'une
promotion n'avait pas reconstruit, en comparant le digest du tag promu à celui du tag
d'origine (B17). La remplacer sans reprendre ce contrôle aurait perdu une garantie de
livraison pour gagner un affichage.

Il vaut maintenant pour **chaque image produite**, et non pour le seul dépôt configuré.

### Le `kind` d'un artefact ne sert pas à l'écran

Le relevé attribue à chaque résultat le `produces` du **pipeline**, si bien qu'un `tag`
porte `kind: image`. Cette imprécision avait été signalée en tranche 2 ; l'écran la tranche
en se fiant au **nom** du résultat, qui est juste, et non à `kind`, qui est descriptif.

Seul un artefact nommé `image` se voit chercher un digest. Corriger le relevé demanderait au
catalogue une déclaration par résultat qu'il ne porte pas, pour un gain nul.

### Trois tables de l'interface ont disparu

`signal → pipeline`, `pipeline → signaux`, `signal → famille de rapports`. Elles disaient ce
que le serveur sait depuis que le score se pèse par geste : un composant porte le geste qui
l'a produit, donc sa famille de pipeline et sa famille de rapports.

Elles devenaient **fausses** dès qu'un projet déclarait deux campagnes de même nature : elles
ne relancent pas le même pipeline, et une table indexée par signal ne pouvait pas le dire.

### Un défaut introduit puis corrigé dans la même tranche

`redistributed` portait des noms de signaux ; il porte désormais des **clés** de composant.
Le bandeau « non mesurés : … » les traduisait par une table de libellés indexée par signal,
et aurait donc affiché `geste:1.0`. Les libellés viennent maintenant des composants
eux-mêmes.

C'est le prix d'un changement d'axe qui traverse la frontière HTTP : les deux bouts se
compilent, et seule la lecture attrape ce genre d'écart.
