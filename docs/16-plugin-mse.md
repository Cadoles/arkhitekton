# 16. Le plugin MSE

> Rassembler dans un seul plugin ce qui n'existe que pour le MSE — les environnements, leur
> promotion par Flux, leur attestation par `PortalData` — et vérifier qu'une instance sans
> lui reste cohérente.

---

## 16.1 Le constat

L'onglet Environnements décrit où l'application est déployée. Trois choses y sont mêlées :

| Élément | Ce qu'il fait | Où il vit |
|---|---|---|
| **Les environnements** | liste déclarée, rang, sensibilité, historique des promotions, validations de recette | catalogue, tables `environments` et `image_promotions`, `internal/http/releases.go`, `internal/worker/environments.go` |
| **Flux** | calcule le tag d'une promotion, contrôle les `ImagePolicy` au démarrage, lit l'état des `Kustomization` | `internal/flux`, `internal/release/promotion.go` |
| **`PortalData`** | atteste la version des données et l'état des migrations dans chaque environnement | `internal/portaldata` |

Doc 12 avait séparé ces trois éléments pour les rendre génériques : `provider.Deployer`,
`provider.Attester`, des réglages de déployeur opaques (lot D). Cette séparation supposait
un second client : quelqu'un qui utiliserait Flux sans le MSE, ou des environnements sans
Flux.

**Ce second client n'existe pas.** L'ensemble est propre au projet, et rien ne garantit qu'un
modèle d'environnement tiré d'un seul exemple conviendrait à un autre. Doc 12 met
lui-même en garde contre ce piège à propos du `Deployer` : ce serait « un calque de Flux qu'on
croirait générique ». Un noyau d'environnements construit aujourd'hui serait un calque du
MSE qui se croirait générique.

Le critère retenu n'est donc pas « est-ce conceptuellement générique ? », mais **« le noyau
fonctionne-t-il sans ? »**.

### Deux défauts constatés en l'examinant

- **Les plugins `flux` et `portaldata` ne pilotent rien.** Ils sont déclarés au registre et
  leur interrupteur s'affiche, mais `cmd/mse-dashboard/main.go` câble Flux sans condition, à
  trois endroits, et lit son namespace dans la configuration plutôt que dans le réglage du
  plugin. Les éteindre ne change rien.
- **La table `deployments` n'est lue ni écrite par aucun code.** Elle date de la migration
  0003.

---

## 16.2 Le périmètre

### Dans le plugin

- les environnements : déclaration, rang, sensibilité, observation, historique ;
- la promotion : planification, mise en file du re-tag, historique `image_promotions` ;
- Flux : contrôle des `ImagePolicy`, état des `Kustomization` ;
- `PortalData` : version des données et état des migrations ;
- les validations de recette, qui portent sur « cette release dans cet environnement » ;
- **la règle de formation d'un tag de promotion** (§16.4) ;
- l'onglet Environnements, et les sections de l'écran d'une release qui en dépendent.

### Hors du périmètre

- **Le bon de livraison.** Il peut se généraliser simplement — gabarit et style par projet.
  Il fera l'objet d'un chantier distinct.
- **Le cycle de vie d'une release** (`draft` → `building` → `verifying` → `ready` →
  `released`). Il reste au noyau et ne dépend d'aucun environnement : `release.released` est
  émis par la finalisation d'une release (`internal/http/releases.go`), pas par une
  promotion.

### Un seul plugin

Flux et `PortalData` forment **un** plugin, et non deux. Leur séparation au lot D répondait à un
cas précis : « un cluster sans le CRD du portail — c'est-à-dire tout cluster qui n'est pas celui
du MSE — ne pouvait donc pas avoir de déployeur ». Ce cas suppose un utilisateur de Flux sans
MSE, qui n'existe pas.

Ce qui reste vrai relève de la **robustesse**, pas de la frontière : l'absence de `PortalData`
sur un cluster — premier déploiement, production déclarée `observable: false` — ne doit pas
empêcher la promotion. `portaldata.Open` rend déjà `ErrAbsent` dans ce cas, et on continue sans
attestation. Ce comportement est conservé à l'intérieur du plugin.

---

## 16.3 La règle de nommage des versions : supprimée

`release.Normalizer` retire un préfixe configuré avant de rapprocher un nom de version du suivi
d'un tag de release. `deploy/base` déclare `VERSION_NAME_PREFIXES=v,MSE `.

### Ce que disent les données

Sur les 21 versions de `mse-test-project` :

| Noms | Nombre | Forme normalisée | Releases rattachées |
|---|---|---|---|
| `v3.1.1` … `v3.3.0` | 5 | `3.1.1` … `3.3.0` | 15 |
| `MSE 3.1.0` … `MSE 3.1.2` | 3 | `3.1.0` … `3.1.2` | 0 — toutes closes |
| `MSE3-1.5` … `MSE3-1.21` | 13 | inchangés | 0 — toutes closes |

**Le préfixe `MSE ` n'apporte rien.** Il normalise trois versions closes qui ne portent aucune
release. Deux d'entre elles ont un jumeau ouvert — `MSE 3.1.1` et `v3.1.1` — et c'est la règle
« la version ouverte l'emporte » qui choisit le `v` : le préfixe **crée** une ambiguïté que le
code doit ensuite résoudre.

**La décision `MSE3-1.5` n'était écrite nulle part dans le code.** Elle tenait dans l'absence du
préfixe `MSE` sans espace, protégée par un commentaire et des guillemets YAML. Remplacer la
valeur par `v,MSE` aurait rattaché treize versions closes à des numéros inventés, sans aucun
message.

### Ce qui est retiré, et ce qui reste

**Tranché :** le préfixe `MSE ` est retiré de la configuration, et avec lui la décision qui le
protégeait.

**Le mécanisme reste au noyau**, avec le seul préfixe `v`. Il vient du versionnement sémantique
et ne suppose rien d'un projet : sans lui, `v3.2.0` dans le suivi ne se rattacherait plus au
tag `3.2.0`, et les quinze releases actuelles perdraient leur version. `DefaultPrefix` vaut
déjà `v` : la variable d'environnement devient inutile et disparaît du déploiement.

**Conséquence au déploiement.** `RenormalizeVersions` réaligne la colonne dérivée au démarrage
du worker : les trois `MSE 3.1.x` retrouvent leur nom comme forme normalisée. Aucune release ne
leur est rattachée, donc aucune ne change de version.

Le décalage de portée relevé à l'examen — des préfixes réglés pour toute l'instance alors que le
schéma de version se règle par projet — perd son importance : `v` vaut pour tous les projets.
Aucun réglage par projet n'est ajouté.

---

## 16.4 La règle de formation d'un tag de promotion : dans le plugin

C'est une autre règle. `3.2.0` devient `3.2.0_prod`, et c'est ce que les `ImagePolicy` de Flux
sélectionnent :

- le gabarit `{version}_{suffix}` et le suffixe propre à chaque environnement
  (`release.PromotionTag`, `release.PlanPromotion`). L'underscore, et non `+`, parce que la
  grammaire des tags OCI exclut ce dernier ;
- le refus de promouvoir une pré-release vers un environnement qui n'en accepte pas : la plage
  SemVer de sa politique la rendrait inéligible, et le tag serait ignoré sans erreur ;
- le refus, dès la configuration d'un projet, d'un schéma qui ne sait pas poser de marqueur
  (`flux.Deployer.CheckScheme`, qui interroge `version.SemVer.Mark`). Sans ce contrôle, le tag
  serait poussé, accepté par le registre, ignoré par Flux : la promotion paraîtrait réussie et
  rien ne bougerait (B7).

Ces fonctions vivent dans des paquets du noyau, `internal/release` et `internal/version`, mais
**n'ont qu'un appelant : `internal/flux`**. Elles rejoignent le plugin.

Le schéma de version du noyau garde ce dont le noyau se sert : `Parse`, `Compare`,
`NextPrerelease`. `Mark` sort de l'interface `Scheme`.

---

## 16.5 Les interfaces du lot D disparaissent

`provider.Deployer`, `provider.Attester`, `NoDeployer`, `NoAttester` et les valeurs de
`Resolution` servent à une seule chose : laisser le noyau appeler Flux et `PortalData` sans les
nommer. Hors de `internal/flux` et `internal/portaldata`, leurs seuls appelants sont :

| Appelant | Usage | Devenir |
|---|---|---|
| `internal/worker/environments.go` | observation | part dans le plugin |
| `internal/http/releases.go` | planification d'une promotion, aperçu des cibles | part dans le plugin |
| `internal/http/projects.go` | `CheckScheme` à la configuration d'un projet | couture 8 |

Une fois ces appelants dans le plugin, **le plugin appelle Flux et `PortalData` directement**.
Les interfaces sont retirées.

C'est revenir sur une partie du lot D, et il vaut mieux le dire. L'objectif du lot D — « une
instance sans plugin MSE » — est mieux servi par une frontière de plugin que par des interfaces
qui attendent un second déployeur qui ne viendra pas.

De même, `portal_version` et `migration_state` n'ont pas besoin d'être généralisés en une liste
d'« attestations » libellées : l'écran qui les lit appartient désormais au plugin qui les
produit.

---

## 16.6 La forme du plugin

### Un module, pas un fournisseur

`plugin.Kind` est un jeu fermé — `tracker`, `forge`, `deployer`, `attester`, `exporter` — et un
descripteur n'en porte qu'un. Un plugin qui déploie, atteste et apporte un écran n'entre dans
aucune case. Le déclarer « déployeur » passerait le reste sous silence.

**Proposition :** un nouveau `Kind`, `module`, pour un plugin qui apporte un domaine entier.
`deployer` et `attester` disparaissent du jeu, faute de réalisation.

### Compilé, comme les autres

Le registre reste celui de doc 12 : « un plugin est un paquet, une installation est une
reconstruction ». Il ne s'agit pas de charger du code à l'exécution.

- **Go** : un paquet `internal/mse`, qui regroupe `flux`, `portaldata`, la planification de
  promotion, l'observation et les handlers HTTP des environnements. Ses routes et sa boucle ne
  sont montées par `main.go` que si le plugin est allumé.
- **Front** : un dossier `apps/web/src/features/mse`, compilé dans le bundle. Ses routes et son
  entrée de navigation ne s'affichent que si le serveur annonce le module actif.
- **Migrations** : elles restent centrales — tout est compilé ensemble. Les tables du plugin
  sont nommées comme telles, et le noyau ne les lit plus.

### La frontière se vérifie

Une règle d'import, tenue par un test : aucun paquet du noyau n'importe `internal/mse`. Seul
`cmd/mse-dashboard` l'assemble. Sans ce garde-fou, la frontière se dissoudrait au premier
raccourci.

Les méthodes de magasin des environnements restent dans `internal/store`, qui porte un seul
type `DB` : les en sortir coûterait plus que ce que ça protège. La règle d'import suffit à
garantir qu'elles ne sont appelées que par le plugin.

---

## 16.7 Les coutures

Le travail ne consiste pas à déplacer du code : il consiste à couper les endroits où le noyau
suppose qu'il existe des environnements. Il y en a neuf.

| # | Couture | Aujourd'hui | Après |
|---|---|---|---|
| 1 | **Navigation** | liste fixe dans `AppShell.tsx` ; `/` redirige vers `/environments` | entrée affichée si le module est actif ; page d'accueil du noyau par défaut |
| 2 | **Écran d'une release** | sections Déploiement et validations, badges d'environnement | le plugin apporte une section à l'écran d'une release ; sans lui, elle n'apparaît pas |
| 3 | **Fait `release.promoted`** | fait du noyau, conservé en doc 15 §15.3 | fait du plugin, `mse.release.promoted` |
| 4 | **Droits** | `auth.PermDeploy(environnement)` ; sensibilités écrites en dur dans `main.go` | le noyau garde le mécanisme de permission ; le plugin fournit la table des sensibilités, déclarée au catalogue |
| 5 | **Validations de recette** | `release_signoffs` a une clé étrangère vers `environments` | partent avec le plugin |
| 6 | **Observation** | `EnvironmentObserver` démarré sans condition sous le bail du leader ; client Flux ouvert sans condition | démarrés par le plugin s'il est allumé |
| 7 | **Schéma** | `environments`, `image_promotions`, `environment_observations`, `release_signoffs` lus par le noyau | appartiennent au plugin ; `deployments`, inutilisée, est supprimée |
| 8 | **Validation d'un projet** | `CheckScheme` appelé par `internal/http/projects.go` | le plugin peut refuser une configuration de projet |
| 9 | **Suggestion `environment`** | le formulaire de lancement propose les environnements du catalogue pour `e2e-tests` | la liste est fournie par le plugin ; sans lui, la suggestion est vide et le champ reste libre |

### La couture 3 revient sur doc 15

Doc 15 §15.3 avait retenu quatre faits du noyau, dont `release.promoted`, parce que c'est l'un
des deux moments où une équipe veut être prévenue. Le besoin ne disparaît pas : un plugin
déclare ses propres faits (doc 14), et une réaction peut écouter `mse.release.promoted` comme
elle écoutait `release.promoted`.

Le noyau compte alors **trois** faits : `version.created`, `release.created`,
`release.released`. La règle de doc 15 est respectée, et même mieux : c'est le plugin qui définit
ce qu'est une promotion, c'est donc lui qui la constate.

### Les coutures 1 et 2 demandent un point d'entrée

Le front doit savoir quels modules sont actifs **pour tout utilisateur connecté**. Le seul
endpoint qui le dit aujourd'hui, `/admin/plugins`, exige le rôle administrateur. Il faut exposer
la liste des modules actifs à un niveau de lecture — dans la session ou dans un endpoint dédié.

---

## 16.8 La porte

Le chantier se justifie **si et seulement si une instance sans le plugin reste cohérente.**
C'est une porte au sens de doc 14 §14.12 : si elle ne passe pas, les environnements font partie
du noyau, et on révise avant d'aller plus loin.

Plugin éteint :

- le worker démarre sur un cluster **sans** CRD Flux ni `PortalData` ;
- la navigation n'affiche pas Environnements, et `/` mène à la page d'accueil du noyau ;
- une release va de `draft` à `released` : construction, vérification, finalisation ;
- l'écran d'une release s'affiche **sans** section Déploiement et sans erreur ;
- aucune route ne rend 500 parce qu'une table d'environnements est vide ;
- un catalogue **sans** section `environments` est accepté.

Plugin allumé, à parité avec aujourd'hui :

- une promotion de `3.2.0-rc15` vers `qualif` met en file le re-tag attendu, avec le tag
  `3.2.0-rc15_testing` ;
- la même promotion vers `prod` est refusée, en nommant la pré-release ;
- l'observation enregistre la version attestée par `PortalData` ;
- le contrôle des `ImagePolicy` refuse toujours le démarrage sur un désaccord.

Constaté sur le cluster, pas seulement en test : les défauts de doc 15 ont été trouvés là.

---

## 16.9 Tranches

| # | Tranche | Ce qu'elle livre | Porte |
|---|---|---|---|
| 1 | **Retrait de la règle de nommage** | `MSE ` retiré, variable `VERSION_NAME_PREFIXES` supprimée, commentaires alignés ; table `deployments` supprimée | |
| 2 | **Le noyau sans environnements** | descripteur `mse` de `Kind` `module` ; interrupteur lu ; Flux, `PortalData` et l'observation câblés seulement s'il est allumé ; endpoint des modules actifs ; navigation, page d'accueil et sections de release conditionnelles | **oui** — les critères plugin éteint de §16.8 |
| 3 | **La promotion dans le plugin** | paquet `internal/mse` ; `PlanPromotion`, `PromotionTag`, `Mark` et `CheckScheme` déplacés ; validation de projet apportée par le plugin ; `release.promoted` devient `mse.release.promoted` ; interfaces `Deployer` et `Attester` retirées | |
| 4 | **Droits et validations** | sensibilité déclarée au catalogue ; fin de la table écrite dans `main.go` ; validations de recette dans le plugin | |
| 5 | **L'écran et la règle d'import** | dossier front `features/mse` ; suggestion `environment` fournie par le plugin ; test d'import qui interdit au noyau d'importer `internal/mse` | |

La tranche 1 est indépendante et peut partir tout de suite. La tranche 2 est la porte : elle ne
déplace presque rien, elle **conditionne**, et c'est ce qui dit si le reste vaut la peine.

---

## 16.10 Décisions

| # | Question | Réponse |
|---|---|---|
| 1 | Un plugin, ou deux — Flux d'un côté, `PortalData` de l'autre ? | **un** — tranché. Flux n'existe ici que dans le contexte MSE (§16.2) |
| 2 | Le bon de livraison entre-t-il dans le plugin ? | **non** — tranché. Généralisable par gabarit et style par projet, chantier distinct |
| 3 | Que devient la règle de nommage des versions ? | **supprimée** — tranché. Le mécanisme reste avec `v` seul (§16.3) |
| 4 | Que devient la règle de formation d'un tag de promotion ? | **dans le plugin** — tranché (§16.4) |
| 5 | Les interfaces `Deployer` et `Attester` restent-elles ? | **non** : leurs seuls appelants rejoignent le plugin (§16.5) |
| 6 | Quel `Kind` pour le plugin ? | `module` — à confirmer (§16.6) |
| 7 | `release.promoted` reste-t-il un fait du noyau ? | **non** : `mse.release.promoted`, fait du plugin. Revient sur doc 15 §15.3 — à confirmer |
| 8 | Où se déclarent les environnements et leur sensibilité ? | dans le **catalogue**, section lue par le plugin : ils y sont déjà, et y sont confrontés au cluster au démarrage — à confirmer |
| 9 | Quelle page d'accueil sans le plugin ? | **Releases** — à confirmer |
| 10 | Les validations de recette partent-elles avec le plugin ? | **oui** : elles portent sur une release **dans un environnement** (§16.7, couture 5) |
