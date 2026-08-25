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
`Tagger`, `Forge`, `ProjectProber` — et non sur les types concrets. Mieux : le dashboard
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
| `Secret` de registre, par projet | **oui** — un projet publie ailleurs | le dashboard |
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

#### 3. Les images pilotées par la table

`internal/images` porte encore `portal` et `portal-cli` en constantes. La table `projects`
sait déjà décrire ce qu'un dépôt construit — registre, cibles, noms d'images. Il reste à
brancher l'enchaînement de vérification et les suggestions dessus, et à retirer les
constantes.

**Pourquoi c'est structurant** : c'est le premier endroit où « le projet se décrit
lui-même » remplace « le code connaît MSE ». Le chantier est en cours, il mérite d'être
fini plutôt que laissé à mi-chemin.

#### 4. Nommer les frontières de fournisseurs

Écrire les interfaces, garder les implémentations actuelles comme seules réalisations.
Aucun plugin, aucun changement fonctionnel.

Trois sur cinq sont tractables tout de suite — `Tracker`, `Forge`, `Attester` — parce que
ce sont des **lectures** dont le sens ne prête pas à discussion. `Deployer` est reporté :
il exige de répondre à « que veut dire promouvoir », qui est précisément ce qu'on ne veut
pas trancher dans l'abstrait.

**Pourquoi c'est structurant** : à partir de là, tout ajout touchant un fournisseur atterrit
dans un endroit nommé. Sans cela, chaque nouvelle fonctionnalité recommence à mélanger les
couches, et le coût du découplage futur augmente à chaque sprint.

#### 5. Marqueurs et préfixes

`##MSE-META##`, `##MSE-SUMMARY##`, et les préfixes `v` / `MSE ` de la normalisation
deviennent des réglages. Une demi-journée.

**Pourquoi ça compte plus que sa taille** : ces marqueurs sont dans le **protocole**, pas
dans l'interface. Un pipeline générique qui émet `##MSE-META##` reste marqué MSE dans son
code source, et c'est exactement ce qu'on cherche à retirer.

#### 6. L'enchaînement des vérifications

`verificationKinds` est une liste en dur dans l'observateur : les quatre pipelines lancés
après une construction réussie. Elle devient une donnée du catalogue.

**Pourquoi c'est structurant** : c'est une décision de processus — « ce projet vérifie ceci
après avoir construit » — aujourd'hui écrite en Go. La sortir du code la rend visible,
et un projet sans tests E2E cesse d'avoir un pipeline fantôme dans sa file.

#### Ce que cet horizon ne résout pas, et l'assume

Le schéma de version, le cycle de release, le multi-projet, le sens de « promouvoir ». Ces
quatre points restent entiers. Le terrain préparé ne les rend pas plus faciles à trancher —
il évite seulement qu'ils deviennent plus difficiles.

---

### Horizon long — le découplage

À reprendre quand un second projet réel existe, parce que c'est lui qui tranchera les
sémantiques. L'ordre reste celui-ci ; l'échéance, non.

### Lot A — Le contrat des fournisseurs *(sans plugin)*

Écrire les cinq interfaces en questions métier, avec les implémentations actuelles comme
seules réalisations. Aucun plugin, aucun changement fonctionnel.

**Pourquoi d'abord** : c'est l'exercice qui révèle les fuites de sémantique, à coût
faible et sans risque. Tant qu'il n'est pas fait, tout découpage est spéculatif.

**Livrable** : `internal/provider/` avec les contrats et leur documentation.

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
`versions.redmine_id` → `(tracker, external_id)`. Supprime le contournement du
`redmine_id` négatif.

### Lot D — Extraire Deployer et Attester

Flux + `PortalData`, séparés. C'est ici que le contrat de [12.4](#124-la-frontière-difficile-la-sémantique)
est mis à l'épreuve : le premier `Deployer` alternatif — même trivial, « aucun
déployeur » — vaut plus que dix pages de conception.

### Lot E — Extraire la Forge

Le plus intriqué (6 fichiers) : références, tags, PR, et la composition de note qui mêle
forge et suivi. À faire en dernier pour cette raison.

### Lot F — Bibliothèque de pipelines

Publier le contrat, et livrer le noyau — construction, re-tag — dans le `deploy/` du
dashboard. Un déploiement neuf obtient alors les pipelines essentiels sans forker
`mse-tekton`, ce qui suffit à rendre une seconde instance possible.

Pas d'installation par le dashboard à ce stade : le noyau ne change qu'avec la version du
binaire, et le déploiement sait déjà poser ce qui bouge à ce rythme.

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

### Jalon de vérification

Le projet est agnostique le jour où **une instance tourne sans plugin MSE** sur un projet
qui n'a ni `PortalData`, ni bon de livraison, ni Redmine — et où l'interface le dit
proprement au lieu d'afficher des trous.

Il est **autonome** le jour où cette instance n'a eu besoin d'aucun dépôt de pipelines
préexistant : le dashboard a posé son noyau, le projet a déclaré son dépôt, et une
première release s'est construite.

---

## 12.10 Ce que je recommande de ne pas faire

- **Ne pas commencer par les plugins.** C'est la partie visible et la moins chère. Sans
  le multi-projet, elle ne débloque presque rien.
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
