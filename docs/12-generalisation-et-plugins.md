# 12 — Généralisation : rendre le dashboard agnostique du MSE

Ce document répond à une question : **que faudrait-il pour que ce dashboard pilote un
autre projet que MSE**, d'abord chez nous, éventuellement comme produit.

Il ne propose pas de tout rendre générique. Il trie ce qui l'est déjà, ce qui peut le
devenir par configuration, ce qui ne peut l'être que par un plugin, et ce qui doit
rester spécifique — en assumant qu'un **plugin MSE** porte ce dernier reliquat.

---

## 12.1 Ce que « générique » veut dire ici

Trois axes, indépendants et de coûts très différents :

| Axe | Question | Coût |
|---|---|---|
| **Fournisseurs** | Une autre forge, un autre outil de suivi, un autre déployeur | Moyen |
| **Multi-projet** | Plusieurs applications dans une même instance | **Élevé** |
| **Domaine** | Un cycle de release qui n'est pas celui du MSE | Variable |
| **Autonomie** | Fonctionner sans dépôt de pipelines préexistant | Moyen |

Le troisième est le plus mal compris, et c'est celui qui décide de la faisabilité des
deux autres. On y revient en [12.4](#124-la-frontière-difficile-la-sémantique).

---

## 12.2 État des lieux : où est le couplage

### Le couplage par dépendances est faible

Le nombre de fichiers important chaque client externe, hors le paquet lui-même :

| Paquet | Fichiers importateurs |
|---|---|
| `internal/flux` | 2 |
| `internal/redmine` | 3 |
| `internal/tekton` | 4 |
| `internal/gitea` | 6 |

Les handlers travaillent déjà sur des **interfaces locales** — `ForgeReader`, `Images`,
`Tagger`, `CommitResolver`, `ProjectProber` — et non sur les types concrets. Mieux : le dashboard
**dégrade déjà proprement** quand un fournisseur manque.

- Registre non configuré → les digests ne sont pas lus, l'écran le dit.
- Forge non configurée → le formulaire retombe sur la saisie libre, et le dit.
- Sonde absente → le test répond « indisponible », et non « échec ».
- Production non observable → état distinct de « inconnu » ([B8]).

**C'est déjà la moitié d'un système de plugins.** Ce qui manque n'est pas la capacité de
brancher autre chose, c'est la *frontière nommée* et le contrat écrit.

### Le couplage par le schéma est fort

```sql
versions.redmine_id       bigint not null unique
issues.redmine_id         bigint not null unique
release_notes.redmine_id  bigint not null
```

Le modèle de données **nomme le fournisseur**. Le symptôme est déjà visible dans le
code : une version créée sans Redmine reçoit un `redmine_id` **négatif**
(`-nextval('versions_id_seq')`). Ce contournement dit exactement où est la couture.

> **Résolu** — migrations 0028, 0030 et 0031. Les trois colonnes sont devenues
> `(tool, external_id)`, l'identifiant en texte, l'unicité bornée au projet. La
> nature d'une version se lit dans `origin` et non dans le signe d'un nombre. Voir
> le [lot C](#lot-c--extraire-le-tracker).

### Le couplage par l'identité est bloquant pour le multi-projet

```sql
releases.tag text not null unique
```

Globalement unique. Deux applications qui sortent chacune un `1.0.0` entrent en
collision. Toutes les routes sont `/releases/{tag}`, tous les agrégats keyent sur le tag,
`versions` suit la même logique, et le schéma porte 37 références à
`release_id` / `version_id`.

Rendre cela multi-projet n'est pas ajouter une colonne : c'est **déplacer la clé
d'identité de toutes les entités métier**, et reprendre chaque requête, chaque route,
chaque écran.

---

## 12.3 Inventaire de la logique propre au MSE

C'est le cœur de la question posée. Chaque ligne est classée par ce qu'elle **peut
devenir**.

Légende : **C** = configuration du dashboard · **P** = configuration de pipeline ·
**PL** = plugin fournisseur · **MSE** = plugin MSE · **G** = déjà générique

| Logique | Où | Devient |
|---|---|---|
| Normalisation `v3.2.0` → `3.2.0` | `release/naming.go` | **C** — liste de préfixes réglable |
| Normalisation `MSE 3.1.2` → `3.1.2` | `release/naming.go` | **C** — même liste |
| `MSE3-1.5` laissé intact, ambigu | `release/naming.go` | **MSE** — décision propre à un historique |
| Natures `pre` / `final` / `patch` | `release/`, schéma | **C** — un cycle déclaré, pas trois noms figés ([12.4](#124-la-frontière-difficile-la-sémantique)) |
| Livrer bit pour bit ce qui a été validé ([B17]) | machine à états | **G** — objectif de qualité, pas une convention |
| *Mécanisme* : une finale re-tague sa pré-release | `releases.go`, schéma | **C** — propriété du mode `retag`, pas du mot `final` |
| Schéma de version SemVer | 12 fichiers, 4 couches | **PL** — capacité remplaçable ([12.4](#124-la-frontière-difficile-la-sémantique)) |
| Version suivante déduite des RC existantes | `CreateReleaseButton.tsx` | **PL** — question posée au schéma |
| Plage sans `-0` excluant les pré-releases | `flux/`, catalogue | **PL** — couple schéma × déployeur |
| Suffixe de promotion, séparateur `_` | catalogue, `release/` | **C** — déjà en configuration, mais suppose un schéma qui sait porter un marqueur |
| `qualif` → `testing` | catalogue `environments` | **C** — déjà en configuration |
| Environnements `unstable/qualif/preprod/prod` | catalogue | **C** — déjà en configuration |
| CRD `PortalData` (`mse.cnous.fr`) | `flux/client.go` | **MSE** — atteste la version déployée |
| `version.txt` + `--build-arg VERSION` ([P10]) | pipelines, Dockerfile | **P** — convention, à documenter comme contrat |
| Images `portal` / `portal-cli` / `portal-dev` | `internal/images` | **C** — en cours de migration vers la table `projects` |
| Cibles `web` / `cli` / `dev` | `internal/images`, projets | **C** — idem |
| Familles `unit/integration/e2e/static/security/load` | `results/results.go` | **G** — familles de test universelles |
| Signaux et pondération du score ([B11]) | `results/score.go` + ConfigMap | **C** — déjà en configuration |
| Marqueurs `##MSE-META##` / `##MSE-SUMMARY##` | `results/markers.go` | **C** — préfixe réglable |
| Parseurs JUnit, PHPStan, cs-fixer, Trivy, k6 | `results/`, `bench/` | **G** — formats d'outils, pas de projet |
| Statuts Redmine → seaux d'avancement | `redmine/`, ConfigMap | **PL** — propre à l'outil de suivi |
| Règle de note : statut « en revue » + PR fusionnée | `worker/releasenotes.go` | **PL** + **C** — le statut est réglable, la règle appartient au couple forge/suivi |
| Référence `#1234` dans titre de PR ou commits | `release/notes.go` | **C** — motif réglable |
| Paramètres `app-revision`, `mse-kustom-revision`… | catalogue | **P** — déjà en configuration |
| Enchaînement build → 4 vérifications | `worker/observer.go` | **C** — liste réglable, aujourd'hui en dur |
| `deploy-mse`, overlay `pipeline`, namespace `mse-pipeline` | pipelines | **P** — déjà paramétré |
| Bon de livraison : contenu et rendu | `worker/exports.go` | **MSE** — livrable contractuel d'un client |

### Ce que l'inventaire montre

**La majorité est déjà de la configuration ou peut le devenir.** Le projet a
systématiquement sorti les faits de déploiement du code — catalogue en ConfigMap,
pondération du score, environnements, secrets désignés et non manipulés. C'est ce qui
rend l'exercice réaliste.

**Un point échappe pourtant à ce tri, et il est structurant** : le schéma de version.
Ce n'est ni de la configuration ni du domaine MSE — c'est une **hypothèse ambiante**,
répandue sur quatre couches, dont plusieurs autres lignes de ce tableau dépendent.
Elle est traitée à part en [12.4](#124-la-frontière-difficile-la-sémantique).

**Quatre éléments seulement résistent** et forment le noyau d'un plugin MSE :

1. **`PortalData`** — un CRD propriétaire qui atteste la version réellement déployée.
   C'est la meilleure source d'observation dont dispose le dashboard, et elle n'existe
   que chez MSE.
2. **Le bon de livraison** — sa structure et son contenu sont un livrable contractuel.
3. **L'ambiguïté `MSE3-1.5`** — une décision prise sur un historique précis.
4. **La règle de composition de note** — « demande en revue dont la PR est fusionnée »
   est un *workflow d'équipe*, pas une forme de données.

Les trois premiers sont de vrais candidats à un plugin. Le quatrième est traité en
[12.4](#124-la-frontière-difficile-la-sémantique) : il illustre le piège.

---

## 12.4 La frontière difficile : la sémantique

C'est le point que je crois le plus sous-estimé dans le projet de généralisation.

**Un plugin qui ne fait que traduire un protocole produit du code qui compile et des
verdicts faux.**

Le même piège existe un cran plus bas, dans le vocabulaire du domaine : un mot courant
peut encoder des décisions propres à une équipe, et le classer « générique » parce qu'il
est répandu revient à imposer ces décisions à tout le monde.

Cinq exemples tirés du code existant, du plus structurant au plus fin.

### Promouvoir

[B7] n'est pas un détail Flux. C'est une doctrine :

- promouvoir = re-taguer le **même digest**, jamais reconstruire ;
- la métadonnée SemVer sélectionne l'environnement ;
- Flux constate le tag et déploie ; le dashboard **n'intervient pas**.

Un plugin ArgoCD ou `kubectl apply` ne change pas un client HTTP : il change ce que
*promu* **signifie**, et donc quand la release passe `ready`, ce qu'on observe pour dire
« déployé », et si l'on peut le **prouver**.

### Attester

`version.txt` + `PortalData` répondent à « quelle version tourne réellement ». Sans
équivalent, un déployeur ne peut répondre que « le tag demandé était X », ce qui n'est
pas la même affirmation. Le dashboard sait déjà distinguer ces deux niveaux
(`resolution: portaldata | digest | git | unobservable`) — **c'est ce vocabulaire qu'il
faut promouvoir en contrat de plugin**, pas une méthode `Deploy()`.

### Versionner

Il faut dézoomer avant de parler des natures de release, parce que **deux problèmes
distincts** sont aujourd'hui confondus dans le code.

**Le premier est le schéma de version.** Le SemVer est générique par construction, mais
il n'est pas universel : tous les projets n'en font pas. Le voisinage immédiat en donne
l'exemple — les tags de `mse-kustom` s'écrivent `2026.7.27-k8sunstable.1651.91a39c48b`,
qui est du calendaire déguisé. D'autres projets numérotent leurs constructions, ou
n'ordonnent rien du tout.

**Le second est ce que l'outil en déduit.** Et là, une part est propre au MSE.

L'hypothèse SemVer n'est pas cantonnée à `internal/release` : elle traverse douze
fichiers sur quatre couches — la forge, le registre, Flux, le magasin, les écrans. Ce
n'est pas une brique qu'on remplace, c'est une **hypothèse ambiante**.

#### Ce que le SemVer donne réellement

Quatre choses, et quatre seulement :

- un **ordre total** entre deux versions ;
- la notion de **pré-release** — la composante après `-` ;
- un emplacement de **métadonnée de build** — après `+`, ou `_` dans notre convention ;
- une **ligne de version** — le triplet `majeur.mineur.correctif`.

#### Ce que nous avons bâti par-dessus, et qui ne vient pas du SemVer

- qu'une release finale **re-tague** l'image d'une pré-release validée ;
- que la promotion **écrit une métadonnée** que Flux sélectionne par plage ;
- qu'une plage sans `-0` **exclut structurellement** les pré-releases ;
- que la RC suivante se **déduit** des RC existantes ;
- que le nom de version Redmine est la **base** du tag de release.

Aucune de ces cinq lignes n'est une conséquence du SemVer. Ce sont des décisions de
processus qui *s'appuient* sur lui.

#### La séparation à faire

Une capacité **schéma de version**, à laquelle le reste pose des questions au lieu de
lire des champs :

| Question | SemVer | Calendaire | Opaque |
|---|---|---|---|
| Ce tag est-il bien formé ? | oui | oui | tout l'est |
| Quelle est sa ligne de version ? | `3.2.0` | `2026.7` | **inconnue** |
| Est-ce une pré-release ? | oui/non | selon la convention | **inconnue** |
| Quelle est la suivante ? | calculable | calculable | **inconnue** |
| Comment y attacher un marqueur ? | métadonnée | suffixe | **impossible** |
| Comment ordonner deux versions ? | précédence SemVer | ordre lexical daté | **inconnu** |

La colonne « opaque » est la plus instructive : elle doit rester **utilisable**. Un projet
dont les tags s'écrivent `build-4711` perd la suggestion de version suivante et les
plages de sélection, mais garde tout le reste — construire, tester, noter, déployer par
tag explicite. L'outil doit le **dire** plutôt que calculer n'importe quoi.

#### Le couplage à déclarer, pas à découvrir

Conséquence importante : **le schéma et le déployeur sont couplés**. Flux sélectionne par
plage SemVer ; avec un schéma opaque, la promotion par plage est impossible et il faut
épingler un tag explicite.

Ce genre d'incompatibilité doit être **refusé à la configuration**, pas découvert au
premier déploiement qui ne part pas. C'est la doctrine déjà tenue ailleurs — le worker
refuse de démarrer sur un catalogue incohérent avec le cluster ([06 §6.2]) — et elle
s'applique telle quelle à la combinaison schéma × déployeur × cycle.

### Nommer une release

La suite découle de ce qui précède. `pre` / `final` / `patch` ont d'abord été classés
« génériques » dans ce document, au motif que le vocabulaire est courant. **C'est une
erreur de méthode** : la fréquence d'un mot ne dit rien de sa généralité, et il fallait
regarder ce que ces natures *font*.

Elles ne nomment pas trois vocables mais **trois points dans un espace de quatre traits
indépendants** :

| Trait | `pre` | `final` | `patch` |
|---|---|---|---|
| Source de l'image | construction | **re-tag** d'une release source | construction |
| Forme du tag | pré-release **exigée** | pré-release **interdite** | pré-release **interdite** |
| Permission | `release:create` | `release:create:final` | `release:create:final` |
| Prérequis | une référence Git | une source validée | une référence Git |

Le tableau montre le défaut : **`pre` et `patch` ne diffèrent que par la forme du tag et
la permission.** Leur comportement est le même — construire depuis du code. Trois noms
pour deux comportements, et un `check (kind in ('pre','final','patch'))` en base qui
interdit d'en déclarer un quatrième.

Un projet en `alpha` / `beta` / `rc` / `ga` n'entre pas dans ces trois cases. Un projet
qui ne distingue rien — une seule sorte de release — doit en choisir une arbitrairement.

### Ce que la généralisation demande ici

Séparer trois couches aujourd'hui fondues dans une énumération :

1. **Le schéma de version** — « ce tag porte-t-il une composante de pré-release ? » est
   une question posée au schéma, qui peut répondre « je ne sais pas ». Générique, et
   remplaçable.
2. **Les modes** — construire depuis une référence Git, ou re-taguer une release
   existante. Un jeu fermé de primitives, générique.
3. **Le cycle** — quelles natures existent, comment elles s'appellent, quel mode et
   quelles contraintes chacune porte. **Propre au projet**, donc en configuration.

Une nature qui exige une forme de tag n'est déclarable que si le schéma sait répondre :
sur un schéma opaque, `prerelease-required` n'a pas de sens et doit être refusé à la
configuration.

Une nature devient alors une entité déclarée :

```yaml
releaseKinds:
  - id: rc
    label: "Release candidate"
    mode: build            # build | retag
    tag: prerelease-required   # required | forbidden | any
    permission: release:create
  - id: ga
    label: "Livraison"
    mode: retag
    source: rc             # la nature dont elle reprend l'image
    tag: prerelease-forbidden
    permission: release:create:final
```

Le coût est réel : trois branchements dans le handler de création, une contrainte de
schéma à transformer en référence, et l'écran de création à piloter par la déclaration
plutôt que par une liste en dur. Ce n'est pas un renommage.

Le gain l'est aussi. [B17] — une finale est un re-tag, jamais une reconstruction — cesse
d'être une propriété de la valeur `final` pour devenir une propriété du **mode**
`retag` : elle vaut alors pour toute nature qui le déclare, quel que soit son nom.

### Composer une note

« Demande en revue dont une PR fusionnée référence `#1234` » suppose : un outil de suivi
avec des statuts, une forge avec des PR, et **une convention d'équipe**. Jira a des *fix
versions* ; il n'a pas cette règle. GitLab a des MR ; le motif de référence diffère.

### Conséquence pour le contrat de plugin

Les interfaces doivent être écrites en **questions métier**, pas en opérations :

```go
// Mauvais — traduit un protocole, perd le sens.
type Deployer interface {
    Deploy(ctx context.Context, image, environment string) error
}

// Bon — pose la question, et autorise « je ne sais pas ».
type Deployer interface {
    // Plan dit comment faire arriver cette version, ou pourquoi c'est impossible.
    Plan(ctx context.Context, release Release, target Environment) (Plan, error)
    // Observe rend ce qui tourne, et **avec quel niveau de preuve**.
    Observe(ctx context.Context, target Environment) (Observation, error)
}

type Observation struct {
    Version string
    // Resolution dit d'où vient l'information : attestation applicative, digest
    // du registre, référence Git, ou rien. C'est ce qui distingue « je sais » de
    // « je suppose », et le dashboard sait déjà le rendre.
    Resolution Resolution
}
```

`Observation.Resolution` existe déjà en base. Le contrat de plugin le reprend au lieu de
l'inventer.

---

## 12.5 Architecture de plugins

### Le mécanisme

Le paquet `plugin` de Go est à écarter : même build, mêmes dépendances, Linux seulement,
pas de déchargement. Trois options réalistes :

| Mécanisme | Isolation | Coût | Quand |
|---|---|---|---|
| **Registre à la compilation** (interfaces + `init()`) | aucune | faible | usage interne, plusieurs projets maison |
| **Hors-process gRPC** (`hashicorp/go-plugin`) | forte | élevé | produit, plugins tiers |
| **Adaptateur déclaratif** (config → appels HTTP) | — | moyen | fournisseurs de forme proche |

**Recommandation : registre à la compilation d'abord.** Un plugin est un paquet, une
« installation » est une reconstruction. On garde le typage, les tests, et surtout le
**refus de démarrer sur une configuration incohérente** — garde-fou qui a déjà évité une
panne silencieuse cette semaine (voir [06 §6.2]).

Le gRPC ne se justifie que le jour où des tiers écrivent des plugins sans recompiler.
Le construire avant coûte cher pour rien, et rend chaque appel faillible.

### Les points d'extension

Cinq, et pas un de plus au départ :

```
Tracker    versions, demandes, statuts, avancement      → Redmine, Jira, Gitea Issues
Forge      dépôts, branches, tags, PR, commits          → Gitea, GitLab, GitHub
Deployer   promotion, observation, preuve               → Flux, ArgoCD, aucun
Attester   quelle version tourne réellement             → PortalData, /version, aucun
Exporter   livrables contractuels                       → bon de livraison MSE, générique
```

`Attester` est séparé de `Deployer` **délibérément** : chez MSE ils viennent de deux
sources différentes (Flux pour l'intention, `PortalData` pour le constat), et les fondre
perdrait la distinction qui fait la valeur de l'écran des environnements.

Tekton n'est **pas** un point d'extension. C'est une hypothèse assumée du produit, comme
PostgreSQL.

### Le plugin MSE

Il porte ce que l'inventaire n'a pas su rendre générique :

- l'`Attester` `PortalData` ;
- l'`Exporter` du bon de livraison ;
- la règle de normalisation `MSE3-…` ;
- la variante de composition de note propre à l'équipe.

**Il est la preuve du découpage.** Si le plugin MSE reste petit, la frontière est bonne.
S'il grossit, c'est que le noyau a laissé fuir du domaine — et c'est un signal à
surveiller à chaque ajout.

---

## 12.6 Architecture de pipelines

### Le problème

Le catalogue est déjà déclaratif et validé contre le cluster au démarrage. Mais les
pipelines eux-mêmes vivent dans `mse-tekton`, un dépôt indépendant dont le nom dit le
couplage. Un second projet devrait le forker, ou réécrire ses pipelines — et
divergerait dès la première correction.

### La bibliothèque est une dépendance du dashboard, pas de l'exploitation

C'est le point qui décide de tout le reste, et il mérite d'être posé clairement.

Les pipelines de construction et de re-tag ne sont pas de l'infrastructure que
l'exploitation choisit : ce sont les **mécanismes du dashboard**. Il en connaît les
paramètres, en attend les résultats, et sa machine à états conclut sur leur issue. Les
laisser à chaque déploiement, c'est garantir des implémentations divergentes de ce que
le dashboard croit piloter.

C'est encore plus net pour les tests. Le format des résultats — familles, marqueurs de
flux, artefacts publiés par URL pré-signée ([B14]) — est **le protocole du dashboard**.
Un pipeline qui ne l'honore pas produit un écran vide, et rien ne dira pourquoi.

**L'analogie juste est celle du schéma de base.** Le dashboard possède son schéma
PostgreSQL et l'applique lui-même, par le rôle `migrate` ; personne ne trouverait sain de
demander à l'exploitation de poser les tables à la main. Les pipelines qui portent ses
mécanismes sont de la même nature : sa propre machinerie, versionnée avec lui.

Ce raisonnement lève l'objection posée dans les premières versions de ce document. Il
reste deux exigences, qui deviennent des règles de conception plutôt que des motifs de
refus.

### Deux tiers, un contrat

| Tier | Contenu | Hypothèse de pile | Installation |
|---|---|---|---|
| **Noyau** | construction, re-tag | aucune | toujours |
| **Référence** | tests par pile — PHP, Node, Go | forte | à la demande |

Le noyau est purement mécanique : cloner, construire une cible, pousser, re-taguer un
digest. Rien n'y suppose un langage. C'est le tier qui rend le projet autonome.

Les implémentations de référence supposent une pile. Elles sont **installables et
forkables** : un projet dont la pile n'est pas couverte écrit les siennes, et n'a qu'un
contrat à honorer.

**Dans les deux cas, le normatif est le contrat, pas l'implémentation.**

| Contrat | Aujourd'hui | À expliciter |
|---|---|---|
| Entrées | paramètres du catalogue | déjà fait |
| Sorties | artefacts déclarés + URL pré-signées ([B14]) | déjà fait |
| Format des résultats | familles + parseurs | à publier comme contrat |
| Marqueurs de flux | `##MSE-…##` | préfixe réglable, à publier |
| Version de l'image | `--build-arg VERSION` → `version.txt` ([P10]) | à publier comme contrat |

### Le modèle d'installation

Les deux tiers ne s'installent pas de la même façon, et le critère de
[12.7](#127-posture-dopérateur) tranche : le noyau ne change qu'avec la version du
dashboard, les implémentations de référence changent quand un projet déclare sa pile.

| Tier | Livré par | Pourquoi |
|---|---|---|
| Noyau | le `deploy/` du dashboard | suit la version du binaire, comme le Job de migration |
| Référence | le dashboard, à la demande | suit la déclaration d'un projet |

Dans les deux cas la bibliothèque est **versionnée avec le binaire** — les manifestes du
noyau vivent dans le dépôt du dashboard, les Task de référence y sont embarquées
(`//go:embed`). Aucun dépôt tiers à synchroniser, aucune dérive silencieuse, et un
déploiement neuf obtient les pipelines essentiels sans forker quoi que ce soit.

Quatre propriétés en découlent, et elles répondent aux risques :

**1. Versionnée avec le dashboard.** Une version du dashboard attend une version de
bibliothèque.

**2. Propriété explicite.** Tout objet posé porte un label
`app.kubernetes.io/managed-by: mse-dashboard` et une version. Le dashboard **ne touche
jamais** ce qu'il n'a pas posé : `deploy-mse`, l'E2E maison, tout pipeline propre à un
déploiement restent à leur propriétaire, et le catalogue continue de les référencer.

**3. La validation au démarrage devient plus forte, pas plus faible.** C'était mon
objection, et elle était mal posée : `goose` valide bien un schéma qu'il a lui-même
appliqué, et cette validation sert — elle détecte l'application partielle, la dérive
manuelle, le décalage de version. Ici de même :

- ce qui est installé ne correspond pas à la version attendue → refus de démarrer ;
- un objet a été modifié à la main → l'écart est nommé ;
- le catalogue référence un pipeline absent → refus de démarrer, comportement actuel
  ([06 §6.2]) conservé.

**4. Le privilège est borné par la propriété, pas par la liste des verbes.** `create` et
`patch` sur `Pipeline` et `Task`, dans le namespace des pipelines, sur les objets qu'il a
posés et marqués comme siens. La question des secrets, que j'avais tranchée trop vite
ici, est reprise en [12.7](#127-posture-dopérateur).

### La porte de sortie

Un mode **bibliothèque externe** reste possible : le dashboard n'installe rien, vérifie
seulement, et signale l'écart. Il sert les déploiements où l'application des manifestes
passe obligatoirement par un pipeline d'exploitation. Ce n'est pas le défaut, c'est une
dérogation — et elle ne coûte presque rien puisque la vérification existe de toute façon.

## 12.7 Posture d'opérateur

### La règle disait moins que ce que j'en avais fait

`deploy/base/rbac-tekton.yaml` motive l'absence de `secrets` ainsi :

> `secrets` : le dashboard n'a aucune raison de **lire** les secrets de Tekton, et ce
> namespace en contient (registre, clé SSH de déploiement, MinIO).

C'est un interdit de **lecture**. En conclure que le dashboard ne doit jamais créer de
secret était une extrapolation de ma part, et elle ne tient pas : en RBAC Kubernetes les
verbes sont indépendants. `create`, `patch` et `delete` s'accordent **sans** `get` ni
`list`.

Un rôle qui crée les secrets qu'il possède, et qui ne peut lire aucun secret du
namespace, est donc exprimable — et il honore le motif d'origine à la lettre.

### Ce qui contient réellement un pipeline

Deuxième correction. Ce n'est pas la RBAC du dashboard qui empêche une Task de se servir
dans le namespace, c'est l'annotation portée par le compte de service :

```yaml
kubernetes.io/enforce-mountable-secrets: "true"
```

Le dashboard crée déjà des `PipelineRun`, donc indirectement des pods. Sans cette
annotation, il pourrait déjà faire monter n'importe quel secret montable du namespace.
**La défense est ailleurs que là où le document la plaçait**, et elle reste valable quelle
que soit la posture retenue.

### L'invariant à conserver

Ce qui compte n'est pas le verbe RBAC, c'est ceci :

> **La matière d'un secret ne transite jamais par une spec, un paramètre, un journal ou
> une réponse d'API.**

C'est cela que [B14] protège — les URLs pré-signées existent pour que les Tasks ne
détiennent pas les identifiants MinIO — et [07 §7.3] avec sa règle « le dashboard désigne,
il ne distribue pas ». Cet invariant survit intact à la posture d'opérateur, et [B14]
garde sa raison d'être propre : monter des identifiants MinIO dans chaque pod de test
élargirait l'exposition à tous les pipelines, ce qu'une URL à durée de vie bornée évite.

### Ce que la posture d'opérateur débloque

Elle résout un problème que [12.8](#128-ce-que-la-mise-en-produit-ajoute) laissait
ouvert : **une clé de déploiement par projet**.

Le dashboard génère la paire dans le cluster, écrit la moitié privée dans un secret que
les pipelines montent, et n'affiche que la moitié publique. Il **n'a jamais besoin de
relire** ce qu'il a écrit. Une clé compromise se révoque projet par projet ; aujourd'hui,
une seule clé ouvre tous les dépôts déclarés, ce qui ne tient pas à l'échelle d'un
produit.

C'est un gain de sécurité, pas une concession.

### L'exposition nouvelle, et comment la borner

Elle est réelle et il faut la nommer : les identifiants **fournis par un humain** — jeton
de forge, clé d'API du suivi, mot de passe de registre — transiteraient désormais par le
navigateur et l'API, ce qui n'est pas le cas aujourd'hui où ils viennent de Vault.

Quatre règles :

1. **Champs en écriture seule.** Une valeur saisie n'est jamais renvoyée, ni en clair ni
   masquée. L'écran affiche « renseigné le … », jamais la valeur.
2. **Jamais en base.** Ces identifiants vont dans des `Secret`, pas dans PostgreSQL, qui
   n'est ni chiffré au repos ni couvert par la RBAC du cluster.
3. **Vault reste une option de premier rang.** `ExternalSecret` doit rester un chemin
   supporté, et non un vestige : c'est le mode attendu en production réglementée.
4. **Journalisé comme une action sortante.** « Qui a changé le jeton de la forge, et
   quand » relève du journal d'audit au même titre qu'une promotion.

### La frontière de propriété

Comme pour les pipelines, mais avec des conséquences plus lourdes :

- le dashboard ne crée que des secrets **qu'il nomme**, marqués `managed-by` ;
- il **n'adopte jamais** un secret préexistant — trouver `regcred` déjà présent ne lui en
  donne pas la propriété ;
- il ne demande pas `get` ni `list` : il ne sait pas ce que le namespace contient d'autre ;
- il **gère l'annotation `enforce-mountable-secrets`** du compte de service, puisqu'en
  devenant propriétaire des secrets il devient responsable de ce qui peut les monter.

Ce dernier point est une charge nouvelle, et elle est le prix de la posture. Elle doit
être couverte par un test : un secret créé hors de la liste montable ne doit pas devenir
montable par accident.

### Le critère de partage : ce qui bouge sous l'action d'un utilisateur

Le périmètre ne se décide pas par nature de ressource — « les secrets oui, les Task
non » — mais par une question unique :

> **L'état désiré de cette ressource change-t-il quand quelqu'un agit dans
> l'interface ?**

Oui → le dashboard la crée et la réconcilie. Non → elle reste dans la kustomization.

Le critère est meilleur que « tout ce que Tekton consomme », pour trois raisons. Il se
décide sans débat pour chaque ressource nouvelle. Il borne l'opérateur à ce qu'il est
seul à savoir — l'état vit dans sa base, et lui seul sait quand il change. Et il laisse
au déploiement ce que le déploiement sait déjà faire, avec l'élagage, la revue et la
traçabilité Git qui vont avec.

| Ressource | Change sur action ? | Propriétaire |
|---|---|---|
| Tekton — contrôleur, CRD | non | **le cluster** |
| PostgreSQL, MinIO — son propre socle | non | le déploiement |
| `Role`, `RoleBinding` **du dashboard** | non | le déploiement |
| `ServiceAccount` des pipelines, son existence | non | le déploiement |
| `Pipeline` / `Task` du **noyau** — construction, re-tag | non | le déploiement |
| Identifiants d'outillage partagés — tirage d'images | non | le déploiement |
| Modèles de volume, classe de stockage | non | le déploiement |
| `Pipeline` / `Task` de **référence**, par pile | **oui** — un projet déclare sa pile | le dashboard |
| `Secret` de clé de déploiement, par projet | **oui** — on déclare un projet | le dashboard |
| `Secret` de registre, par projet | **oui** — un projet publie ailleurs | le déploiement, **désigné** par le dashboard — voir plus bas |
| Liste des secrets montables | **oui** — dérivée des précédents | le dashboard |
| `PipelineRun` | **oui** *(déjà)* | le dashboard |

### Ce que ce partage corrige dans la section précédente

**Le noyau de pipelines repasse au déploiement**, et c'est plus juste. Sa version suit
celle du **binaire**, pas les actions d'un utilisateur : le livrer dans le `deploy/` du
dashboard le fait arriver avec lui, exactement comme le Job de migration — qui est bien
une ressource de la kustomization, et qui exécute pourtant du code du dashboard.

L'autonomie est préservée : un nouveau déploiement obtient les pipelines essentiels sans
forker quoi que ce soit, puisqu'ils viennent du dépôt du dashboard. Et la validation au
démarrage retrouve sa forme la plus simple — elle vérifie ce que le déploiement a posé,
comme aujourd'hui.

Ne reste dynamique, côté pipelines, que **l'installation à la demande d'une
implémentation de référence** : déclarer un projet PHP pose les Task PHP. Là, l'état
désiré vient bien de la base.

### Deux points que le critère laisse ouverts

**La liste des secrets montables.** Elle est *dérivée* de ressources dynamiques mais
portée par un objet fixe, le `ServiceAccount`. Deux voies :

- le dashboard **patche ce seul champ** — chirurgical, mais il écrit dans un objet dont
  il n'est pas propriétaire, ce que le critère cherchait justement à éviter ;
- **un compte de service par projet**, entièrement dynamique — propriété nette, au prix
  d'un objet de plus par projet et d'un `RoleBinding` à poser.

La seconde est plus cohérente avec le critère. À trancher au moment de l'implémentation,
avec le cas réel sous les yeux.

**La RBAC du dashboard reste fixe, et ce n'est pas négociable.** Un opérateur qui peut
modifier ses propres droits n'a plus de bornes. Elle est déclarée par le déploiement,
revue en Git, et le dashboard ne demande jamais `roles` ni `rolebindings` sur lui-même.
C'est ce qui rend le reste de la posture tenable.

### Le modèle d'opérateur, sans CRD

Le dashboard est déjà à moitié un opérateur : il observe les `PipelineRun` par informer,
réconcilie au démarrage, et tient un `Lease` d'élection de leader. Lui donner la
propriété de ressources est un petit pas.

Une différence assumée : **l'état désiré vit dans PostgreSQL, pas dans des CRD**. C'est
une variante légitime — l'état est déjà relationnel, multi-entités, et interrogé par des
écrans — mais elle a deux conséquences à écrire :

- l'intention n'est **pas lisible par `kubectl`** ; le seul endroit qui dit ce que le
  dashboard veut est sa base, et son interface ;
- la dérive à réconcilier n'est pas « spec contre status » mais **base contre cluster**,
  ce qui appelle une boucle de réconciliation continue, et non une installation unique.

Cette boucle est un gain : un `Task` modifié à la main revient à sa version connue, au
lieu de rester modifié jusqu'au prochain déploiement.

### Ce que cela change au plan

Le lot F cesse d'être « installer des pipelines » pour devenir **« posséder ses
ressources »** : définitions, secrets, comptes de service, et la boucle qui les maintient.
C'est plus large, mais c'est le lot qui rend l'autonomie réelle — sans lui, déclarer un
projet dans l'interface laisse encore un geste manuel à faire dans le cluster, et
l'autonomie s'arrête là.

---

## 12.8 Ce que la mise en produit ajoute

Hors périmètre immédiat, mais à savoir avant de s'engager :

- **Cloisonnement.** Les rôles sont globaux (`user_roles.role`). Plusieurs projets dans
  une instance exigent des rôles **par projet**.
- **Secrets par projet.** La clé de déploiement unique tient pour trois dépôts d'une même
  équipe ; elle ne tient pas pour des clients distincts. La posture d'opérateur
  ([12.7](#127-posture-dopérateur)) la résout : une paire générée par projet, dont seule
  la moitié publique est affichée.
- **Montée de version du schéma** chez des tiers, sans accès à leur base.
- **Surface de test.** Généraliser multiplie les combinaisons *forge × suivi ×
  déployeur*. Il faut **désigner une combinaison de référence** — Gitea + Redmine + Flux
  — tenue à 100 %, et décider explicitement que les autres le sont moins, plutôt que de
  laisser la couverture se diluer sans que personne ne l'ait choisi.

---

## 12.9 Plan

Deux horizons, et c'est délibéré. Le découplage complet demande de trancher des
sémantiques — schéma de version, cycle de release, sens de « promu » — qui sont
difficiles à trancher **dans l'abstrait**, avant d'avoir un second projet réel sous les
yeux. Les repousser n'est pas les fuir : c'est refuser de décider trop tôt.

En attendant, on peut préparer le terrain. Ce qui suit ne rend pas l'outil générique ;
cela le rend **prêt à le devenir**, et chaque élément a une valeur immédiate.

---

### Horizon court — préparer le terrain

**Le critère de sélection**, et il tranche seul les cas douteux :

> Est-ce que cela **déplace une décision du code vers une donnée**, sans exiger qu'on
> tranche une sémantique ?

Si oui, c'est du terrain préparé : la décision devient visible, modifiable, et le jour où
il faudra la rendre pluggable, elle sera déjà sortie du code. Si non — s'il faut d'abord
répondre à « que veut dire promouvoir » — cela relève de l'horizon long.

Six chantiers, du plus rentable au moins :

| # | Chantier | Valeur structurante | Coût |
|---|---|---|---|
| 1 | Publier le contrat des pipelines et des résultats | **forte** | faible |
| 2 | Livrer le noyau de pipelines dans le `deploy/` du dashboard | **forte** | faible |
| 3 | Images pilotées par la table `projects` | **forte** | moyen |
| 4 | Nommer les frontières de fournisseurs | **forte** | moyen |
| 5 | Marqueurs et préfixes de normalisation configurables | moyenne | très faible |
| 6 | Enchaînement des vérifications configuré | moyenne | faible |

#### 1. Publier le contrat

Aucun code. Écrire ce qu'un pipeline doit honorer pour que le dashboard sache l'exploiter :
familles de résultats, marqueurs de flux, artefacts déclarés et publiés par URL pré-signée
([B14]), `--build-arg VERSION` → `version.txt` ([P10]).

**Pourquoi en premier** : c'est le document qui permet à quelqu'un d'écrire un pipeline
conforme sans lire notre code. Tout le reste s'y adosse, et il coûte une journée.

#### 2. Le noyau de pipelines dans le dépôt du dashboard

`build-image` et `retag-image` sont **déjà génériques** : le nom d'image et la cible du
Dockerfile sont des paramètres, plus rien n'y suppose MSE. Il ne reste qu'à les déplacer
de `mse-tekton` vers le `deploy/` du dashboard.

**Ce que cela débloque immédiatement** : un second projet n'a plus besoin de forker
`mse-tekton` pour construire et promouvoir. C'est le premier pas d'autonomie réelle, et il
consiste à déplacer deux fichiers.

#### 3. Les images pilotées par la table — **fait**

`internal/images` portait `portal`, `portal-cli` et `portal-dev` en constantes, doublées de
deux listes : ce qu'une release construit, ce qu'un re-tag propage. La table `projects`
décrivait déjà ce qu'un dépôt construit — registre, cibles, dépôts d'image alimentés. Elle
dit désormais aussi lesquels sont **promus**, et c'est elle que le dashboard lit.

Ce qui a bougé :

- `store.Target` gagne `Promote`, et `Project` les méthodes `Buildable()` / `Promotable()`.
  `BuildableProject` rend le dépôt dont une release construit les images, et refuse
  plusieurs candidats plutôt que d'en choisir un au hasard. La migration `0024` renseigne
  le drapeau sur les lignes déjà écrites — muettes, elles auraient promu l'image de
  développement en préproduction.
- Les trois chemins de mise en file — construction, re-tag de conclusion, promotion — lisent
  la table à chaque appel plutôt qu'un tableau figé au démarrage : les étapes se modifient
  depuis l'administration, où une case dit maintenant si l'étape est promue.
- Le **tableau de noms en dur** disparaît, et c'est le gain principal. Il existait en
  double — une copie dans l'enchaînement automatique, une dans la relance manuelle — et
  énumérait `revision`, `app-revision`, `mse-revision`, `portal-image`, `consumer-image`.
  `Pipeline.VerificationInputs` le remplace en s'appuyant sur ce que le catalogue déclare
  déjà : `suggest: git-ref` dit qu'un paramètre veut une référence Git, quel que soit son
  nom. Les natures `portal-image` / `consumer-image` cèdent la place à `suggest: image`
  accompagné de `image: portal`, et la liste proposée par le formulaire se constitue à
  partir des images que le catalogue nomme.
- `internal/images` ne fait plus que composer `registre/nom:tag`.

**Ce que ça vaut** : c'est le premier endroit où « le projet se décrit lui-même » remplace
« le code connaît MSE ». Publier une image de plus ne demande plus de recompiler, et un
pipeline qui nomme autrement ses paramètres devient enchaînable sans toucher au Go.

**Ce qui reste attaché** : les *cibles* du Dockerfile — `web`, `cli`, `dev` — sont
maintenant du contenu de table et d'amorçage, pas du code. La migration `0024`, elle, nomme
`dev` : c'est assumé, une migration de données porte sur les données qui existent, et le
champ reste libre pour un déploiement qui appellerait autrement ce qu'il ne promeut pas.

#### 4. Nommer les frontières de fournisseurs — **fait**

`internal/provider` porte `Tracker`, `Forge`, `TagWriter`, `RepositoryProber` et `Attester`,
et le vocabulaire qu'ils échangent. Aucun plugin, aucun registre, aucune sélection à
l'exécution : la configuration désigne toujours Redmine et Gitea en clair, et le binaire ne
sait construire qu'eux.

Les types qui traversent la frontière ont **déménagé** plutôt que d'être recopiés :
`redmine.Version`, `redmine.Issue`, `gitea.Ref`, `gitea.PullRequest`, les deux `Account`
sont devenus des types de `provider`. Les clients décodent désormais dans une structure de
fil interne — `is_admin` chez Gitea, `admin` chez Redmine — et rendent le type de
frontière. C'était possible sans couche d'adaptation parce que ces types étaient déjà des
structures de domaine, distinctes du JSON reçu.

Deux déplacements dans la foulée :

- `GiteaProber` quitte `internal/http` pour devenir `gitea.Prober`. Il y était le seul appel
  à une API extérieure du paquet : la couche HTTP traduisait un échec en constat lisible
  **et** parlait à Gitea.
- `flux.PortalData` devient `Client.Attestations`, rendant `provider.Attestation`. Le CRD
  reste propre au portail — c'est ce que le plugin MSE portera — mais ce qu'il rapporte,
  « telle version tourne et elle est exploitable », ne l'est pas. `status.schemaVersion`
  cesse d'être décodé : personne ne le lisait.

**Les deux séries d'assertions** sont ce qui rend la frontière contraignante plutôt que
décorative :

```go
var _ provider.Tracker = (*Client)(nil)   // auprès de chaque réalisation
var _ RedmineReader = provider.Tracker(nil)  // auprès de chaque appelant
```

La seconde est la plus utile. Une méthode propre à Redmine qui entrerait dans l'interface
étroite d'un appelant ne casserait rien à l'exécution — le client concret la porte — mais
rendrait silencieusement le dashboard inapte à tout autre suivi. Ici, elle ne compile pas.

Un renommage a suivi : `http.Forge` (qui ne portait que `ResolveCommit`) devient
`CommitResolver`, le nom `Forge` désignant maintenant le contrat complet.

**Ce qui n'y est pas** : `Deployer`, parce qu'il exige de répondre à « que veut dire
promouvoir » et qu'une interface écrite avant cette réponse serait un calque de Flux qu'on
croirait générique. `Exporter` non plus : le bon de livraison est un livrable contractuel
sans source extérieure à abstraire.

**Ce que ça vaut** : tout ajout touchant un fournisseur atterrit désormais dans un endroit
nommé, et le débordement se voit à la compilation. Sans cela, chaque fonctionnalité
recommençait à mélanger les couches, et le coût du découplage futur augmentait à chaque
sprint.

#### 5. Marqueurs et préfixes — **fait**

Deux noms de client qui vivaient en constantes Go sont devenus des réglages.

**Les préfixes de normalisation.** `release.Normalizer` remplace la fonction de paquet
`NormalizeVersionName`, qui retirait `v` et `MSE ` en dur. Le binaire ne connaît plus
d'office que `v` — il vient du versionnement sémantique et ne suppose rien d'un projet —,
et `deploy/base` déclare `VERSION_NAME_PREFIXES=v,MSE `.

Trois conséquences qui ne se voyaient pas dans l'énoncé du chantier :

- **La règle est devenue uniforme, donc plus stricte.** `MSE ` était retiré sans
  condition, `v` seulement devant un chiffre. La garde du chiffre vaut désormais pour tout
  préfixe — sans quoi un projet déclarant `rel` ramènerait « release-candidate » à
  « ease-candidate ». Sur les vingt noms de l'instance réelle, ancienne et nouvelle règles
  donnent le **même** résultat ; `MSE3-1.5` reste intact dans les deux cas.
- **L'espace final de `MSE ` est significatif, et fragile à transporter.** `MSE` sans
  espace ramènerait `MSE3-1.5` à `3-1.5`, c'est-à-dire rattacherait treize versions closes
  à des numéros inventés. Le lecteur de configuration ne coupe donc que l'espace *avant*
  un élément, et le littéral kustomize est quoté — un scalaire YAML non quoté perd son
  espace de fin sans le moindre message.
- **`versions.normalized_name` est une colonne dérivée.** Changer les préfixes la rend
  obsolète, et le rapprochement comparerait alors une forme calculée avec les nouveaux à
  une forme écrite avec les anciens : une release cesserait de trouver sa version, en
  silence. `store.RenormalizeVersions` la réaligne au démarrage du worker. Son premier
  passage en cluster a corrigé **trois** lignes — pas à cause du changement de règle, qui
  est neutre sur ces données, mais parce que la colonne avait déjà dérivé.

Un point d'ouverture unique, `openStore`, applique la convention aux quatre pools du
binaire : une ouverture qui l'oublierait ne casserait rien à la compilation.

**Les marqueurs.** `results.MarkerSet` remplace les constantes `##MSE-META##` et
`##MSE-SUMMARY##` : le jeu neutre est `##META##` / `##SUMMARY##`, et le jeton `MSE` rend
les formes historiques. Un jeton et non deux chaînes libres — les deux marqueurs d'un même
protocole doivent porter la même marque.

Le canal était alors **débranché**, et aucune configuration n'était câblée pour lui : en
ajouter une pour un protocole que personne ne consomme aurait été de la surface morte. Il a
été raccordé depuis, et `deploy/base` déclare `MARKER_TOKEN=MSE` — le jeton que `mse-tekton`
émet déjà. L'ordre a payé : les Tasks n'ont pas eu à changer.

**Pourquoi ça compte plus que sa taille** : ces marqueurs sont dans le **protocole**, pas
dans l'interface. Un pipeline générique qui émet `##MSE-META##` reste marqué MSE dans son
code source, quelle que soit la propreté de ce qui le lit.

#### 6. L'enchaînement des vérifications — **fait**

La liste des pipelines lancés après une construction réussie existait **deux fois** en
Go : `verificationKinds` dans l'observateur, `VerifiableKinds` dans l'API, tenues alignées
par un test — plus une troisième copie morte dans le front, que personne n'importait.

Elle devient un drapeau `verification: true` sur chaque pipeline du catalogue. Porté par
le pipeline plutôt que par une liste en tête : une liste peut nommer une famille absente,
et l'ordre d'enchaînement deviendrait une seconde vérité à côté de l'ordre de déclaration.
`Catalog.VerificationKinds()` et `Catalog.IsVerification()` servent les quatre usages —
enchaînement, décompte de conclusion, reconnaissance d'un run enchaîné, relance à l'unité.

Trois choses en sont sorties, qui n'étaient pas dans l'énoncé :

- **Une règle de validation.** Une vérification qui ne déclare aucun artefact tourne,
  conclut, et son verdict n'atteint jamais le dashboard : la release attend un résultat que
  rien ne remonte. C'était vérifié par un test sur le catalogue livré ; c'est désormais
  refusé au démarrage, pour tout catalogue (doc 06 §6.2).
- **Un commentaire mort.** L'observateur expliquait pourquoi `integration-tests` n'était
  *pas* enchaîné, au-dessus d'une liste qui le contenait. Un lecteur pouvait en tirer
  l'inverse de ce que le code faisait.
- **Un endpoint sans test.** `Verify` — la relance à l'unité — n'était couvert par aucun
  test. Le changement l'aurait rendu silencieusement inopérant, ou pire, aurait laissé
  passer une relance de `build` qui republie les images au lieu de rejouer un test.

**Pourquoi c'est structurant** : c'est une décision de processus — « ce projet vérifie ceci
après avoir construit » — qui était écrite en Go. La sortir du code la rend visible, et un
déploiement sans campagne E2E cesse d'avoir un pipeline fantôme dans sa file.

#### Ce que cet horizon ne résout pas, et l'assume

Le schéma de version, le cycle de release, le multi-projet, le sens de « promouvoir ». Ces
quatre points restent entiers. Le terrain préparé ne les rend pas plus faciles à trancher —
il évite seulement qu'ils deviennent plus difficiles.

---

### Horizon long — le découplage

À reprendre quand un second projet réel existe, parce que c'est lui qui tranchera les
sémantiques. L'ordre reste celui-ci ; l'échéance, non.

### Lot A — Le contrat des fournisseurs *(sans plugin)* — **fait**

`Deployer` rejoint `Tracker`, `Forge` et `Attester`. Trois questions, aucune opération :

- `Plan` — comment faire arriver cette version dans cet environnement, **ou pourquoi c'est
  impossible** ;
- `Observe` — ce qui tourne, et avec quel niveau de preuve ;
- `Check` — si ce que `Plan` proposera aura un effet, vérifié au démarrage.

Aucune ne demande au déployeur d'agir sur le cluster cible. Il **décrit**, la file du
dashboard **fait**. Ce n'est pas une commodité : c'est ce qui garde l'action traçable,
soumise aux mêmes droits que le reste, au lieu d'être un effet de bord enfoui dans une
implémentation.

#### Ce que la réalisation triviale a changé

`provider.NoDeployer` — le déployeur d'un projet qui n'en a pas — n'est pas un bouchon de
test : c'est le comportement exact d'un déploiement non configuré, et c'est lui qui a
éprouvé le contrat. Une interface écrite sur une seule implémentation en épouse toujours
la forme sans qu'on s'en aperçoive. Quatre points ont bougé en le confrontant :

- **`Plan` rend une erreur nommée**, `ErrNoDeployment`, et non un plan vide. « Aucun outil
  ne sait faire » n'est pas « la demande est fautive » : l'API répond 501 et le dit, au
  lieu de faire chercher un incident là où il n'y a qu'une absence de configuration.
- **`Target` ne porte plus le suffixe de promotion.** Il n'a de sens que pour une sélection
  par métadonnée SemVer ; l'inscrire dans le type commun l'imposerait à un déployeur qui
  procède autrement. Les réglages passent dans le vocabulaire du déployeur, qui les nomme
  et les valide dans `Check`.
- **`Observe` rend une résolution explicite**, jamais un zéro. `unobservable` est une
  réponse, pas une absence de réponse.
- **`Deployment` distingue `Version` et `Artifact`.** L'écart est le piège central de
  B17 — une release finale porte `1.8.0` alors que son image existe sous `1.8.0-rc3` — et
  une signature qui n'en prend qu'un le masquerait.

#### Ce que ce lot ne prouve pas

Un second déployeur **réel** — ArgoCD, `kubectl apply` — reste à voir. Il éprouverait ce
que `NoDeployer` ne peut pas : la forme de `Plan{Kind, Params}` est-elle assez expressive
pour un acheminement qui ne se réduit pas à poser un tag ? La réponse ne s'obtient pas en
raisonnant ; elle s'obtient en en écrivant un.

**Livrable** : `internal/provider/` complet, `flux.Deployer` comme seule réalisation
réelle, et les deux appelants — observation et promotion — qui en dépendent au lieu de
dépendre de Flux.

### Lot B — Le multi-projet et le cycle déclaré

Le lot lourd. Déplacer l'identité : `project_id` sur `versions`, `releases`, et tout ce
qui en dépend ; unicité `(project_id, tag)` ; routes `/projects/{p}/releases/{tag}` ;
rôles par projet.

**Le schéma de version et le cycle de release s'y rattachent**
([12.4](#124-la-frontière-difficile-la-sémantique)), dans cet ordre : le schéma d'abord,
puisque le cycle lui pose ses questions. Les natures cessent alors d'être une énumération
en base pour devenir une déclaration par projet.

Les trois changements touchent les mêmes tables et les mêmes écrans ; les séparer ferait
payer trois fois la même migration. C'est ce qui fait de ce lot le plus lourd, et le seul
dont l'ordre interne compte.

**Pourquoi si tôt** : sans lui, les plugins ne servent qu'à changer de Redmine **pour un
projet unique**. C'est le prérequis dur de tout le reste, et le repousser rend chaque lot
suivant plus cher.

**Attention** : la table `projects` existe déjà (migration 0021) mais n'est que
descriptive. Ce lot la rend porteuse.

### Lot C — Extraire le Tracker

Redmine en premier plugin : le plus isolé (3 fichiers), le plus demandé.
`versions.redmine_id` → `(tool, external_id)`. Supprime le contournement du
`redmine_id` négatif.

**Fait.** En deux tranches, parce que les demandes traversent bien plus de code que
les versions.

*Les versions* (migration 0030) : `tool` + `external_id`, l'identifiant en **texte**
— Redmine numérote, un autre outil écrit `MSE-1234`, et un entier fermait la porte
au second sans qu'aucune erreur ne le dise. La colonne se nomme `tool` et non
`tracker` bien que ce soit le mot du contrat : `issues.tracker` existe déjà et
désigne le *type* d'une demande (« Anomalie », « Évolution »). Les deux tables se
joignent en permanence, et deux colonnes homonymes de sens différents dans une même
requête sont une faute qu'on ne voit qu'en relisant à froid.

*Les demandes* (migration 0031) : mêmes colonnes sur `issues`, plus un `project_id`
que la table ne portait pas — son unicité était donc **globale**, et deux projets
suivant deux instances se seraient écrasés au premier numéro commun. Le paramètre
existait pourtant déjà, il bornait la recherche de la version, mais n'était pas
conservé. `release_notes.redmine_id` devient `external_id` et ne porte **pas**
d'outil : une note appartient à une release, donc à un projet, et c'est le projet
qui déclare son suivi — le dupliquer ici créerait deux vérités à tenir d'accord.

La borne par projet devait ensuite être posée dans les requêtes : la jointure de
`ListNoteEntries` et le vivier de `NoteCandidates` cherchaient par le seul
identifiant. Sans erreur levée, la note d'un client aurait affiché le sujet d'un
autre. `NoteCandidates` prend maintenant la release plutôt qu'un identifiant de
projet — l'appelant l'a sous la main, et un paramètre qu'on peut oublier de passer
n'est pas une borne.

Le croisillon disparaît des écrans : `#1234` est la convention de numérotation de
Redmine, et `#MSE-1234` ne veut rien dire. Il reste toléré **à la saisie**, où il
n'est que l'habitude de qui tape.

Contrat côté fournisseur : `provider.Version.ID` et `provider.Issue.ID` sont des
`string`, `Issue.VersionID` un `*string`. La conversion depuis les entiers de
Redmine se fait dans la réalisation, pas dans le contrat.

### Lot D — Extraire Deployer et Attester

Flux + `PortalData`, séparés. C'est ici que le contrat de [12.4](#124-la-frontière-difficile-la-sémantique)
est mis à l'épreuve : le premier `Deployer` alternatif — même trivial, « aucun
déployeur » — vaut plus que dix pages de conception.

**Fait.** Trois coutures, et l'épreuve a effectivement fait tomber plus que la
séparation annoncée.

*L'attestation quitte le client Flux* (`internal/portaldata`). Un seul client
résolvait les trois CRD d'un coup et **échouait si l'un manquait** : un cluster
sans le CRD du portail — c'est-à-dire tout cluster qui n'est pas celui du MSE —
ne pouvait donc pas avoir de déployeur, alors que Flux y était parfaitement
installé. La dépendance facultative faisait échouer la dépendance principale.
Elle est désormais optionnelle et se déclare absente par une erreur nommée, le
dashboard retombant sur ce que le déploiement déclare. `provider.NoAttester`
double `NoDeployer` : c'est elle qui a confirmé que `Attestations` devait pouvoir
rendre une liste vide **sans erreur**, faute de quoi l'absence se serait déclarée
en panne.

*Les réglages du déployeur deviennent opaques* (migration 0033). Trois colonnes
— `promotion_suffix`, `accepts_prerelease`, `flux_kustomization` — nommaient
Flux dans le catalogue, dans le schéma, dans le worker et dans l'API. Le
catalogue **exigeait** de surcroît un `promotionSuffix` non vide sur chaque
environnement : un projet sans outil de déploiement ne pouvait déclarer aucun
environnement. Elles deviennent une carte que le dashboard transporte sans la
lire ; seul `internal/flux` nomme ses clés, et la règle d'unicité des suffixes a
suivi, du catalogue vers le garde-fou du déployeur — où elle est confrontée aux
politiques réelles du cluster, ce qui est plus fort qu'une comparaison de
chaînes. Ajouter un second déployeur n'ajoute plus de colonne.

*`flux_state` devient `declared_state`* (migration 0032). Même faute que
`versions.redmine_id`, même correction : la colonne porte ce que la donnée est —
une intention, au sens de `provider.ResolutionDeclared` — et non de quel produit
elle vient. `portal_version` et `migration_state` gardent leur nom : « portal » y
désigne la source de l'attestation, pas l'outil de déploiement.

Ce que le lot a mis au jour en chemin : **l'observateur d'environnements n'avait
aucun test**. Toute la hiérarchie des preuves de [B8] — l'écart entre ce qu'un
déployeur déclare et ce qu'une application atteste — reposait sur du code que
rien ne tenait, y compris le cas qui compte le plus, celui d'une image déployée
dont les migrations ont échoué.

### Lot E — Extraire la Forge

Le plus intriqué (6 fichiers) : références, tags, PR, et la composition de note qui mêle
forge et suivi. À faire en dernier pour cette raison.

**Fait**, et la prévision était fausse sur le point principal : la composition de note
n'était pas intriquée. Elle passait déjà par `provider.PullRequest` et une interface
étroite, et les lots précédents avaient fait le reste. Ce qui restait était ailleurs, et
tenait en deux points.

*Les issues étaient nommées par leur réalisation.* `gitea.ErrTagExists`,
`gitea.ErrWriteForbidden`, `redmine.ErrNotConfigured` : chaque paquet portait les
siennes, et un appelant qui voulait les interpréter devait importer la réalisation. Le
gestionnaire HTTP comparait littéralement contre `gitea.ErrTagExists` — une frontière
traversée pour une seule comparaison, et un second forgeron aurait rendu une erreur que
ce code n'aurait pas reconnue : la pose de tag aurait échoué en « erreur interne » là où
elle avait simplement déjà été faite. Les trois issues vivent maintenant dans le contrat,
les réalisations les enveloppant pour que le journal nomme l'outil et qu'`errors.Is`
reconnaisse l'issue. `internal/gitea` n'est plus importé que par le câblage.

*La configuration nommait les produits.* `GITEA_TOKEN`, `REDMINE_API_KEY`,
`config.GiteaConfig`, `RedmineSync`, le rôle `check-redmine`, et jusqu'au champ
`redmine_version` du corps de création de release. Même faute que `versions.redmine_id`
avant le lot C, à ceci près qu'elle était visible par l'exploitant. Le vocabulaire est
désormais celui des frontières — `FORGE_*`, `TRACKER_*` — et les anciens noms sont
**refusés au démarrage**, en nommant leur remplaçant. C'est le point qui comptait : une
variable héritée laissée en place ne produirait aucune erreur, la forge serait
« non configurée » — état parfaitement valide — et le suivi cesserait simplement de se
synchroniser. Deux dégradations silencieuses que personne n'aurait reliées au renommage.

Ce qui reste, et qui est nommé dans le code : `ReleasesHandler.issueURL` compose
`<base>/issues/<id>`, qui est le chemin de Redmine. Un second suivi l'écrirait autrement.
C'est la dernière chose que cette frontière ne sait pas encore demander à sa
réalisation — le contrat rend des identifiants, pas des liens.

### Lot F — Bibliothèque de pipelines

**Fait par l'horizon court** pour l'essentiel : contrat publié, noyau livré dans le
`deploy/` du dashboard.

Ne restent ici que les **implémentations de référence par pile** — les Task PHP, Node, Go
posées à la demande quand un projet déclare la sienne. Elles relèvent des ressources
dynamiques, donc du lot G.

**Vérifié, et le contrat n'était pas vrai.** « Le normatif est le contrat, pas
l'implémentation » ([12.6](#126-architecture-de-pipelines)) : encore faut-il que
l'implémentation l'honore. Doc 13 annonçait que le répertoire nomme la famille ; trois
familles sur six ne le respectaient pas.

`e2e` et `load` n'étaient reconnues que par le nom du fichier, ou par le répertoire
d'**outil** que les Tasks du MSE emploient — `mocha/`, `k6/`. Les pipelines du MSE
fonctionnaient donc parfaitement, ce qui est exactement pourquoi l'écart ne s'était jamais
vu : il ne se manifeste que pour le second projet, celui que tout ce document existe pour
rendre possible. Un projet écrivant `test-results/e2e/report.json` — la convention
publiée — n'obtenait **rien**, sans message. Écran vide, famille jamais mesurée, aucune
trace : le mode de défaillance que doc 13 §13.9 nomme lui-même comme celui à éviter.

Les répertoires de famille sont désormais lus, ceux d'outil restant acceptés pour ne pas
casser l'existant. Le cas de `static` est tranché explicitement plutôt que masqué : deux
outils y produisent deux formats JSON, le répertoire de famille seul ne dit pas lequel
lire, et un rapport dont l'outil n'est pas identifiable est **ignoré** au lieu d'être
deviné — un parseur choisi au hasard rendrait des violations fausses.

Trouvé en chemin : le faux magasin des tests de charge ne modélisait pas la garde
d'idempotence de la base, si bien que `TestIngestIsIdempotent` passait sans jamais la
franchir. La déduplication ne repose plus sur un read-after-write dans la boucle.

### Lot G — Propriété des ressources dynamiques

Ce que le dashboard est seul à savoir : clés de déploiement par projet, secrets de
registre, implémentations de référence installées à la demande, et la boucle qui les
réconcilie ([12.7](#127-posture-dopérateur)).

C'est ce lot qui rend l'autonomie réelle — sans lui, déclarer un projet dans l'interface
laisse encore un geste manuel à faire dans le cluster. Il vient **après** le multi-projet
(lot B), qui donne son sens à « par projet ».

**Ce lot peut avancer en parallèle des autres.** Il ne dépend ni du multi-projet ni des
contrats de fournisseurs, et c'est le moins cher de tous depuis qu'il se réduit à
déplacer des manifestes et à écrire un contrat. À prendre tôt si un second projet se
présente avant que le lot B ne soit fini.

**Deux tranches faites : la clé de déploiement, puis le secret de registre.** Reste les
implémentations de référence par pile, nommées plus bas.

La RBAC accorde `create`, `patch` et `delete` sur les Secrets, **sans** `get` ni `list`.
La restriction n'est pas un compromis, elle produit la garantie : n'ayant aucun moyen de
demander au cluster si un Secret lui appartient, le dashboard lit sa propriété dans sa
base — `projects.deploy_key_public` renseignée — et **refuse d'adopter** un Secret
homonyme qu'il n'a pas créé. La rotation, elle, remplace, parce que la base atteste que
la clé est nôtre.

*Où la pose s'exécute.* Pas dans l'API : elle tourne avec
`automountServiceAccountToken: false`, délibérément, parce que c'est la surface exposée.
Lui accorder un jeton de cluster pour la commodité d'une pose synchrone à la déclaration
aurait été un recul de sécurité déguisé en fonctionnalité. L'API **demande** en base ; le
worker réalise, sous le bail. C'est ce qui a donné au lot sa boucle de réconciliation :
un projet sans clé en reçoit une, une rotation demandée s'exécute, un redémarrage
rattrape ce qu'une panne a interrompu.

*La liste des secrets montables.* `enforce-mountable-secrets` est ce qui empêche
réellement une Task de se servir dans le namespace, et devenir propriétaire de secrets
rend responsable de ce qui peut les monter. Le dashboard n'ajoute que des noms qu'il
dérive lui-même d'un slug — la garantie est structurelle, ces méthodes ne prennent jamais
un nom de secret — et ne retire jamais une entrée qu'il n'a pas posée. Elle est tenue sur
**chaque** compte de service que le catalogue nomme : n'en tenir qu'un rendrait la clé
montable pour certains pipelines d'un projet et pas pour les autres.

*Ce que la mise en service a failli casser.* Poser une clé et l'**employer** sont deux
instants séparés par un geste humain que rien n'automatise : la coller dans les réglages
du dépôt, côté forge. La première version basculait les six projets déclarés sur une clé
que la forge ignorait — le prochain build aurait échoué au clone, sur un dépôt qui
fonctionnait la veille, avec un message désignant le dépôt et non la clé. Exactement le
mode de défaillance que ce lot existe pour supprimer, reproduit par sa propre mise en
service. D'où `deploy_key_active`, faux par défaut : c'est à celui qui a collé la clé de
dire qu'elle y est.

*Trouvé en chemin.* La file était restée aveugle au multi-projet du lot B — un job ne
portait pas son projet, et l'ordonnanceur ne pouvait donc pas savoir quelle clé monter.
Le rattachement se **dérive** de la release dans le magasin plutôt que d'être renseigné
aux huit sites qui enfilent des jobs : exiger de chacun qu'il y pense garantissait qu'un
l'oublierait, et le run serait parti avec la clé partagée sans qu'aucune erreur ne le
dise.

#### Le secret de registre : désigné, non créé

`image_registry` était déjà par projet ; le secret qui autorise à y pousser était resté
global. Publier ailleurs demandait donc des identifiants que ce secret-là ne porte pas, et
le run échouait au `push` **après** avoir construit l'image.

La table ci-dessus confiait ce secret au dashboard, comme la clé de déploiement. **Ce
n'est pas ce qui a été fait**, et la ligne a été corrigée.

La différence entre les deux tient en un mot : une clé de déploiement, le dashboard
l'**engendre**. Aucune matière ne vient d'un humain, rien ne traverse le navigateur, et la
moitié privée naît dans le processus qui l'écrit. Un identifiant de registre, lui, vient
d'un humain. Le faire entrer dans un `Secret` depuis l'interface le ferait transiter par
le navigateur et l'API — l'exposition que [12.7](#127-posture-dopérateur) nomme elle-même
« réelle » —, et l'API tourne sans jeton de cluster, délibérément.

Les deux seules façons de l'y faire arriver seraient de lui accorder ce jeton, ou de
garder l'identifiant en base : la première défait la décision prise à la tranche
précédente, la seconde viole la règle « jamais en base » de la même section. Aucune des
deux ne vaut la commodité d'un formulaire.

Le projet **désigne** donc un secret que le déploiement provisionne — par `ExternalSecret`
adossé à Vault en production, ce que §12.7 exige de garder comme chemin de premier rang.
C'est doc 07 §7.3 appliquée sans aménagement : le dashboard désigne, il ne distribue pas.

Bénéfice qui n'était pas cherché : le secret désigné est **déjà** déclaré montable par le
déploiement, comme `regcred` l'est. Aucune entrée à ajouter à la liste des secrets
montables, donc aucun nom étranger à y faire entrer — ce que la garantie structurelle de
la tranche précédente interdit précisément.

### Jalon de vérification

Le projet est agnostique le jour où **une instance tourne sans plugin MSE** sur un projet
qui n'a ni `PortalData`, ni bon de livraison, ni Redmine — et où l'interface le dit
proprement au lieu d'afficher des trous.

Il est **autonome** le jour où cette instance n'a eu besoin d'aucun dépôt de pipelines
préexistant : le dashboard a posé son noyau, le projet a déclaré son dépôt, et une
première release s'est construite.

---

### Le jalon intermédiaire

Avant même le second projet, l'horizon court a un résultat vérifiable : **un `grep -ri mse`
sur `internal/` ne rend plus que le plugin MSE et l'historique des décisions**. Les
constantes, les marqueurs et les listes en dur en sont sortis.

Ce n'est pas la généricité, et il ne faut pas le présenter comme telle. C'est la preuve
que la frontière est **trouvable** — condition nécessaire pour que le découplage suivant
soit un déplacement plutôt qu'une réécriture.

---

## 12.10 Ce que je recommande de ne pas faire

- **Ne pas commencer par les plugins.** C'est la partie visible et la moins chère. Sans
  le multi-projet, elle ne débloque presque rien.
- **Ne pas trancher une sémantique dans l'abstrait.** Le schéma de version, le cycle de
  release et le sens de « promouvoir » se décident bien avec un second projet réel sous
  les yeux, mal sans. Préparer le terrain est utile ; deviner ne l'est pas.
- **Ne pas construire le gRPC tout de suite.** Coût élevé, bénéfice nul tant que les
  plugins sont écrits par la même équipe.
- **Ne pas rendre Tekton pluggable.** L'hypothèse est saine ; la remettre en cause
  doublerait le travail pour un gain hypothétique.
- **Ne pas installer ce qu'on ne possède pas.** Le dashboard pose son noyau et ses
  implémentations de référence, jamais les pipelines propres à un déploiement. La
  frontière est un label, et elle doit être vérifiée par un test, pas par la discipline.
- **Ne pas demander `get` sur les secrets.** Créer ce qu'on possède ne suppose pas de
  pouvoir lire ce qu'on ne possède pas. Les verbes RBAC sont indépendants, et cette
  asymétrie est ce qui rend la posture d'opérateur défendable.
- **Ne pas provisionner son propre socle.** PostgreSQL et MinIO restent au déploiement.
  Une application qui installe sa propre base change de métier.
- **Ne pas gérer ce qui ne bouge pas.** Une ressource dont l'état désiré ne change
  qu'avec la version du binaire appartient au déploiement : il sait l'élaguer, la revoir
  en Git et la tracer. La réconciliation est un coût, elle doit acheter quelque chose.
- **Ne jamais posséder sa propre RBAC.** Un opérateur qui peut modifier ses droits n'a
  plus de bornes. C'est ce qui rend le reste de la posture tenable.
- **Ne pas généraliser les parseurs de résultats.** JUnit, Trivy, k6 sont des formats
  d'outils, pas de projet : ils sont déjà génériques.
- **Ne pas confondre courant et générique.** Un vocabulaire répandu peut encoder des
  décisions propres à une équipe. Le test n'est pas « ce mot est-il usuel » mais « quel
  comportement ce mot sélectionne-t-il, et un autre projet le voudrait-il ? ».
- **Ne pas confondre le SemVer et ce qu'on en a tiré.** Il donne un ordre, une notion de
  pré-release, un emplacement de métadonnée. Le re-tag d'une finale, la sélection par
  plage et la déduction de la RC suivante sont des décisions **nôtres**, et c'est là que
  se trouve la part MSE.
- **Ne pas laisser une combinaison impossible s'installer.** Schéma opaque et sélection
  par plage SemVer ne vont pas ensemble. Cela se refuse à la configuration, comme le
  catalogue incohérent, et non au premier déploiement qui ne part pas.
- **Ne pas dissoudre les doctrines.** [B7] et [B17] sont les meilleures décisions du
  projet. Elles doivent rester le **défaut**, et un plugin qui s'en écarte doit le
  déclarer et l'assumer à l'écran.

[B7]: 02-blocages-et-decisions.md#b7
[B8]: 02-blocages-et-decisions.md#b8
[B11]: 02-blocages-et-decisions.md#b11
[B14]: 02-blocages-et-decisions.md#b14
[B17]: 02-blocages-et-decisions.md#b17
[P10]: 10-prerequis-mse-tekton.md
[06 §6.2]: 06-orchestration-tekton.md
[07 §7.3]: 07-securite-et-permissions.md
