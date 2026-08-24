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
| Natures `pre` / `final` / `patch` | `release/`, schéma | **G** — vocabulaire de release courant |
| Re-tag du même digest ([B17]) | machine à états | **G** — doctrine saine, à garder par défaut |
| Suffixe SemVer de promotion, séparateur `_` | catalogue, `release/` | **C** — déjà en configuration |
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

Trois exemples tirés du code existant.

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
pipelines eux-mêmes vivent dans `mse-tekton`, un dépôt indépendant, dont le nom dit le
couplage.

### Ce qu'une bibliothèque doit fournir

Un pipeline réutilisable n'est pas un pipeline sans paramètres : c'est un pipeline dont
les **contrats sont explicites**.

| Contrat | Aujourd'hui | À expliciter |
|---|---|---|
| Entrées | paramètres du catalogue | déjà fait |
| Sorties | artefacts déclarés + URL pré-signées ([B14]) | déjà fait |
| Format des résultats | familles + parseurs | à documenter comme contrat public |
| Marqueurs de flux | `##MSE-…##` | préfixe réglable |
| Version de l'image | `--build-arg VERSION` → `version.txt` ([P10]) | à documenter comme contrat |

**Le vrai travail n'est pas d'installer des pipelines : c'est d'écrire le contrat qu'ils
doivent honorer.** Une fois écrit, la bibliothèque peut être un simple dépôt de
kustomizations versionnées.

### Installer, ou pas

Le dashboard crée aujourd'hui des `PipelineRun`, **pas** des `Pipeline` ni des `Task`.
Lui donner `create/update` sur les définitions lui donne le pouvoir de modifier ce qu'il
exécute lui-même — un saut de privilège réel, à mettre en regard de la ligne tenue
jusqu'ici : il n'a délibérément pas `secrets` ([07 §7.3]).

Deuxième risque, plus insidieux : **la validation au démarrage ne protège plus rien** si
c'est le dashboard qui a posé ce qu'il valide.

**Position recommandée** — le dashboard *propose* et *vérifie*, il n'installe pas :

1. une bibliothèque versionnée de pipelines, en kustomizations ;
2. le déploiement (Flux, kustomize) les applique — comme aujourd'hui ;
3. l'écran des projets affiche ce qui est **disponible**, ce qui est **installé**, et
   l'écart ;
4. l'installation reste un geste d'exploitation, hors du dashboard.

On garde la découvrabilité — le vrai manque — sans le privilège ni la perte du
garde-fou. Un mode « installateur » pourra venir plus tard, explicitement activé, si le
besoin se confirme.

---

## 12.7 Ce que la mise en produit ajoute

Hors périmètre immédiat, mais à savoir avant de s'engager :

- **Cloisonnement.** Les rôles sont globaux (`user_roles.role`). Plusieurs projets dans
  une instance exigent des rôles **par projet**.
- **Secrets par projet.** La clé de déploiement unique tient pour trois dépôts d'une même
  équipe. Elle ne tient pas pour des clients distincts — et générer une clé par projet
  demande le droit `secrets` qu'on a refusé.
- **Montée de version du schéma** chez des tiers, sans accès à leur base.
- **Surface de test.** Généraliser multiplie les combinaisons *forge × suivi ×
  déployeur*. Il faut **désigner une combinaison de référence** — Gitea + Redmine + Flux
  — tenue à 100 %, et décider explicitement que les autres le sont moins, plutôt que de
  laisser la couverture se diluer sans que personne ne l'ait choisi.

---

## 12.8 Plan

Six lots, dans cet ordre. L'ordre n'est pas négociable sur les deux premiers : le reste
en dépend.

### Lot A — Le contrat des fournisseurs *(sans plugin)*

Écrire les cinq interfaces en questions métier, avec les implémentations actuelles comme
seules réalisations. Aucun plugin, aucun changement fonctionnel.

**Pourquoi d'abord** : c'est l'exercice qui révèle les fuites de sémantique, à coût
faible et sans risque. Tant qu'il n'est pas fait, tout découpage est spéculatif.

**Livrable** : `internal/provider/` avec les contrats et leur documentation.

### Lot B — Le multi-projet

Le lot lourd. Déplacer l'identité : `project_id` sur `versions`, `releases`, et tout ce
qui en dépend ; unicité `(project_id, tag)` ; routes `/projects/{p}/releases/{tag}` ;
rôles par projet.

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

Contrat public des pipelines, dépôt versionné, écran de découverte. **Sans installation
par le dashboard** (voir [12.6](#126-architecture-de-pipelines)).

### Jalon de vérification

Le projet est agnostique le jour où **une instance tourne sans plugin MSE** sur un projet
qui n'a ni `PortalData`, ni bon de livraison, ni Redmine — et où l'interface le dit
proprement au lieu d'afficher des trous.

---

## 12.9 Ce que je recommande de ne pas faire

- **Ne pas commencer par les plugins.** C'est la partie visible et la moins chère. Sans
  le multi-projet, elle ne débloque presque rien.
- **Ne pas construire le gRPC tout de suite.** Coût élevé, bénéfice nul tant que les
  plugins sont écrits par la même équipe.
- **Ne pas rendre Tekton pluggable.** L'hypothèse est saine ; la remettre en cause
  doublerait le travail pour un gain hypothétique.
- **Ne pas généraliser les parseurs de résultats.** JUnit, Trivy, k6 sont des formats
  d'outils, pas de projet : ils sont déjà génériques.
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
