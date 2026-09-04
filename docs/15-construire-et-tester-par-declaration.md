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

### Ce que la réduction impose ailleurs

`release.verified` ne servait pas qu'à déclencher : il faisait **avancer l'état de la
release** de `verifying` vers `ready`. Sans lui, c'est la **conclusion du plan** qui avance
la release. Le moteur sait quand un plan se termine et avec quelle issue ; il faut brancher
la machine à états dessus. C'est le seul point où la réduction n'est pas gratuite.

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

### Un drapeau à lever

Les poids actuels — 25/20/20/10/15/10 — sont désignés dans le code comme **« la pondération
acceptée par le client (B11) »**. Les rendre libres par projet ne casse rien
techniquement, mais **déplace une valeur contractuelle vers une saisie d'interface**. Il faut
que ce soit une décision assumée, pas un effet de bord : je ne l'implémenterai pas sans un
accord explicite là-dessus.

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

| # | Question | Ma recommandation |
|---|---|---|
| 1 | Deux faits du noyau, ou quatre ? | **quatre** : garder `release.promoted` et `release.released`, qui sont des décisions hors plan et le cas d'usage principal des notifications (§15.3) |
| 2 | Les poids du score deviennent-ils une saisie par projet ? | **à confirmer explicitement** : c'est une valeur contractuelle B11 qu'on déplace vers l'interface (§15.6) |
| 3 | Un geste dont l'artefact manque échoue-t-il ou est-il sauté ? | **échoue**, en nommant l'artefact : un test sauté en silence se lit comme un test passé (§15.4) |
| 4 | La colonne `targets` est-elle retirée tout de suite ? | **non** : conservée non lue le temps d'un déploiement, pour pouvoir revenir en arrière sans migration inverse (§15.8) |
| 5 | Un projet sans automatisation déclarée construit-il encore ? | **non**, et c'est le sens du chantier — mais la migration §15.8 fait qu'aucun projet existant n'y arrive par surprise |
