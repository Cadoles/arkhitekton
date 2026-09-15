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

---

## 16.11 Tranche 1 — ce que l'écriture a tranché

**Le réglage a disparu avec le préfixe.** Retirer `MSE ` laissait `v` comme seule valeur, et
`v` est déjà le défaut. La variable `VERSION_NAME_PREFIXES`, la liste de préfixes
configurable et son injection dans le magasin, les handlers et la synchronisation ne servaient
plus qu'à transporter une constante. Ils sont retirés : `release.NormalizeVersionName` est
redevenue une fonction.

Doc 12 §12.9 (chantier 5) en avait fait une valeur injectée pour une raison précise : les
préfixes venaient de la configuration, et une fonction libre aurait permis de normaliser sans
eux. Sans configuration, cette raison tombe.

**Les tests d'adoption portaient la convention `MSE `.** Les paires ambiguës étaient
`MSE 3.1.2` close et `v3.1.2` ouverte. Elles deviennent `3.1.2` et `v3.1.2`, qui se réduisent
toujours au même numéro : le cas éprouvé — une version close homonyme ne bloque pas
l'adoption, deux actives restent indécidables — est inchangé.

**`deployments` est supprimée par la migration 0050**, qui sait la recréer en retour arrière.

---

## 16.12 Tranche 2 — ce que l'écriture a tranché

**Deux propositions appliquées.** Le plugin est de `Kind` `module` (décision 6) ; `deployer` et
`attester` disparaissent du registre, faute de réalisation. Sans le plugin, la page d'accueil
est Releases (décision 9). Les deux restent révisables : la tranche les pose, elle ne les
fige pas.

**La migration 0051 reprend Flux.** L'activation et le namespace de `flux` deviennent ceux de
`mse`. Un `flux` éteint à la main donne un `mse` éteint. Les réglages par projet de
`portaldata` disparaissent : le plugin vaut pour toute l'instance.

**Le namespace vient enfin du réglage du plugin.** La configuration `FLUX_NAMESPACE` ne sert
plus que de repli — c'était l'un des deux défauts de §16.1.

**Deux moments de lecture, et c'est assumé.**

| Où | Quand | Pourquoi |
|---|---|---|
| `api` | à chaque requête | l'interrupteur se bascule depuis l'administration ; le lire au démarrage ferait mentir l'onglet jusqu'au déploiement suivant |
| `worker` | au démarrage | ouvrir ou refermer un client Flux en cours de route toucherait au bail et à la boucle d'observation |

Conséquence : **basculer l'interrupteur demande un redémarrage du worker** pour que Flux,
PortalData et l'observation suivent. L'interface et les routes suivent immédiatement.

**Ce qui est coupé quand le plugin est éteint :**

- `api` : les routes `/environments`, la promotion et les validations de recette répondent
  404 ; `/me` n'annonce pas le module ; une release ne rend ni cibles de promotion ni
  environnements où elle tourne ;
- `worker` : ni client Flux, ni PortalData, ni synchronisation ni observation des
  environnements ;
- front : pas d'onglet Environnements, accueil sur Releases, pas de section Déploiement ni de
  pastille d'environnement ; une URL d'environnement renvoie aux releases.

**Ce qui ne l'est pas encore.** `CheckScheme` s'applique toujours à la configuration d'un
projet (couture 8) et la suggestion `environment` du formulaire de lancement lit toujours le
catalogue (couture 9) : ce sont les tranches 3 et 5.

---

## 16.13 Tranche 3a — ce que l'écriture a tranché

**La tranche 3 part en deux commits.** La première déplace ce qui se déplace sans toucher aux
écrans : paquets, règle de promotion, contrôle du schéma, fait de promotion. La seconde (3b)
emporte l'observation et les handlers d'environnement, et retire `Deployer` et `Attester` —
elle demande un point d'entrée de routes dans le routeur du noyau.

**`internal/flux` et `internal/portaldata` deviennent `internal/mse/flux` et
`internal/mse/portaldata`.** `internal/mse` porte ce qui est propre au plugin sans être un
client : pour l'instant, le fait de promotion.

**La règle de promotion rejoint Flux.** `PlanPromotion`, `PromotionTag` et le gabarit quittent
`internal/release` pour `internal/mse/flux`. `Target` y devient `PromotionTarget`, pour ne pas
se confondre avec la cible du déployeur.

**`Mark` quitte l'interface `version.Scheme`.** Elle n'existait que pour que ce déployeur pose
la question à un schéma calendaire qui n'existe pas. `CheckScheme` accepte le SemVer et refuse
le reste, et le marqueur underscore est posé par `flux.Mark`.

**Le noyau ne connaît plus de déployeur pour valider un projet.** `ProjectsHandler` reçoit une
fonction `WithSchemeCheck`. `main` la branche sur le déployeur Flux, et elle ne refuse rien
quand le module est éteint. Elle suit l'interrupteur à chaque requête, comme les routes.

**`release.promoted` devient `mse.release.promoted`** (décision 7) :

- le noyau compte trois faits ;
- le descripteur `mse` déclare le fait et sa charge, et l'éditeur de réactions le propose
  seulement quand le module est allumé ;
- le worker l'émet par un crochet `WithPromotionFacts`, branché au démarrage si le module est
  allumé ;
- le fait porte `Source` `mse`.

**Les réactions suivent, les faits passés restent.** La migration 0052 renomme le type des
réactions déclarées sur `release.promoted`. Les faits déjà écrits gardent leur type : le fil
est une histoire, et le front garde leur libellé.

---

## 16.14 Tranche 3b — ce que l'écriture a tranché

**Les routes du plugin sont montées par le plugin.** `NewAPIRouter` accepte des `Mount` : des
fonctions qui reçoivent le multiplexeur et le middleware. `mse.Handler.Routes` y déclare les
cinq routes :

- cibles d'une release ;
- promotion ;
- vue d'ensemble des environnements ;
- détail d'un environnement ;
- historique d'un environnement.

Chacune porte `RequireModule`. Pour y répondre dans le format de l'API, le plugin emprunte au
noyau `WriteJSON`, `WriteProblem` et `RequestProject`. Ce sont des exports minces, réunis dans
`internal/http/plugin.go`.

**Les cibles de promotion quittent la charge d'une release.** Elles étaient calculées dans
`GET /releases/{tag}` et rendues vides quand le module était éteint. Elles sont servies par
`GET /projects/{project}/releases/{tag}/targets`, route du plugin, que la section Déploiement
interroge. La liste des promotions **demandées** reste dans la charge : la table appartient au
schéma, et les artefacts s'en servent pour signaler un digest divergent.

**L'observation part dans `internal/mse`.** `EnvironmentObserver` devient `mse.Observer`. Il
dépend de deux interfaces étroites déclarées par le plugin : ce qu'il demande à Flux et à
PortalData. Sans le CRD du portail, l'attestation est nulle plutôt qu'un `NoAttester`.

**`provider.Deployer` et `provider.Attester` sont supprimés.** Leurs types d'échange suivent
leur seul appelant :

| Type | Destination |
|---|---|
| `Target`, `Deployment`, `Plan`, `Observation`, `Resolution` | `internal/mse/flux` |
| `Attestation` | `internal/mse/portaldata` |

`NoDeployer` et `ErrNoDeployment` disparaissent. « Aucun déployeur » s'écrit désormais
« module éteint », et la route répond 404 au lieu de 501.

**Le test du catalogue sans réglage de déployeur reste**, déplacé dans `internal/catalog` :
c'est une propriété du catalogue, pas de l'observateur.

**Ce qui reste dans le noyau** relève des tranches 4 et 5 : les validations de recette et
leurs routes, `environmentPolicies()` dans `main.go`, la table `environments` et ses lectures
dans `internal/store`.

---

## 16.15 Tranche 4 — ce que l'écriture a tranché

**La sensibilité se déclare au catalogue** (décision 8). Chaque environnement porte
`sensitive`. `mse.Policies` en tire la table que l'autorisateur consomme. La table écrite dans
`main.go`, et son TODO, disparaissent.

L'absence de `sensitive` vaut **sensible**, comme un environnement inconnu de l'autorisateur :
un oubli rend la promotion plus difficile, jamais plus facile. Le catalogue déployé déclare les
quatre valeurs explicitement, et un test vérifie qu'il reproduit l'ancienne table :

| Environnement | Sensible |
|---|---|
| `unstable`, `qualif` | non |
| `preprod`, `prod` | oui |

**Le noyau garde le mécanisme.** `auth.PermDeploy`, le rôle minimal selon la sensibilité et
`RequireDeploy` restent dans `internal/auth` et `internal/http` : ce sont des permissions, pas
des environnements. Le noyau ne sait plus quels environnements existent.

**Conséquence au démarrage de l'`api`.** Le catalogue est chargé **avant** l'autorisateur, qui
en dépend. Les environnements sensibles sont journalisés : c'est ce qui décide qui peut
promouvoir où, et un catalogue mal édité ne se verrait sinon qu'au premier refus.

**Les validations de recette partent dans le plugin** (décision 10). `SignoffsHandler` et ses
trois routes vivent dans `internal/mse`, montées comme celles de la tranche 3b. Le routeur du
noyau perd son paramètre `signoffs`. Côté front, le panneau n'est affiché que par l'écran
d'un environnement : il ne dépend déjà que du module.

**Ce qui reste :**

- les lectures et écritures des tables `environments`, `image_promotions`,
  `environment_observations` et `release_signoffs` vivent encore dans `internal/store`. Les
  déplacer demanderait au plugin ses propres migrations, un chantier distinct du découpage du
  code (couture 7) ;
- tranche 5 : le dossier front `features/mse`, la suggestion `environment` et le test de la
  règle d'import.

---

## 16.16 Tranche 5 — ce que l'écriture a tranché

**Le front a son point d'assemblage.** Un module décrit ce qu'il apporte dans une définition :

- son identifiant ;
- sa page d'accueil ;
- ses entrées de navigation et ses écrans ;
- ses sections et pastilles pour les écrans d'une release.

Le type vit dans `lib/modules.ts`. `src/modules.ts` est le seul fichier du noyau qui importe un
module, comme `cmd/mse-dashboard` côté serveur. La coquille, les routes, l'accueil, l'écran d'une
release et ses pastilles lisent la liste des modules allumés, et plus aucun ne nomme le MSE.

**`features/mse` regroupe l'interface du plugin** :

- les trois écrans d'environnement ;
- le panneau de validation de recette ;
- la section Déploiement, sortie de l'écran d'une release ;
- les pastilles « déployée sur » ;
- leurs hooks : promotion, cibles, validations.

`MODULE_MSE` quitte la session pour la définition du module. La page introuvable renvoie à
l'accueil plutôt qu'aux environnements.

**La liste des modules est une fonction.** Les modules importent des écrans qui importent le
noyau. Une constante évaluée au chargement pourrait l'être avant eux ; une fonction est appelée
au rendu, une fois tout chargé.

**La suggestion `environment` vient du plugin** (couture 9). La file reçoit une fonction
`WithEnvironmentSuggestions`. Le plugin la fournit et ne rend rien quand le module est éteint :
le champ reste en saisie libre, même si le catalogue déclare des environnements. Lue à chaque
requête, comme l'interrupteur des routes.

**La règle d'import est vérifiée des deux côtés.**

| Côté | Test | Ce qu'il refuse |
|---|---|---|
| serveur | `internal/plugin/boundary_test.go` | tout fichier Go sous `internal/`, hors `internal/mse`, qui importe `internal/mse` — tests compris |
| front | `src/modules.test.ts` | tout fichier hors `features/mse` et de l'assemblage qui importe `features/mse` |

Chacun vérifie aussi qu'il lit bien des sources, pour ne pas passer en silence sur un chemin
faux.

**Le chantier est au bout de ses tranches.** Reste hors du découpage du code la couture 7 : les
tables d'environnement et leurs lectures dans `internal/store` (§16.15). Et les décisions 6, 7, 8
et 9 restent à confirmer.
