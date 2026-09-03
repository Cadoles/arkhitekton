# 14. Déclencheurs et automatisations

> **Objet** — rendre configurable ce qui s'enchaîne après un fait : lancer des pipelines,
> poser un statut, appeler un webhook. Aujourd'hui la chaîne est écrite en Go, la même pour
> tous les projets.
>
> Les **causes** sont extensibles au même titre que les conséquences : un plugin déclare les
> faits qu'il sait observer, et le noyau n'a pas à les connaître.

---

## 14.1 Le constat de départ, et ce qu'il change au problème

L'énoncé naturel est « ajouter un système de déclencheurs au dashboard ». Il est trompeur,
et s'y tenir produirait la mauvaise architecture.

**Le dashboard est déjà un orchestrateur.** Il a une file avec priorités, plafonds de
concurrence, classes d'exclusion et plafond de tentatives ; un observateur qui réagit à la
conclusion d'un run ; une machine à états sur les releases. Ce qui lui manque n'est pas un
moteur — c'est que **son graphe est en dur** :

```
build réussi ──▶ launchVerification(toutes les familles du projet)
                        │
                        ▼
              concludeVerification ──▶ ready | failed
```

Trois arêtes, dans `internal/worker/observer.go`. Le besoin exprimé — poser un statut
Gitea, commenter des résultats, notifier une messagerie — n'est pas un besoin de moteur
nouveau : c'est le besoin de **déclarer ces arêtes** au lieu de les compiler.

**La conséquence de méthode est nette.** Greffer un second moteur à côté du premier
donnerait deux orchestrateurs qui s'ignorent, et une machine à états de release qui finit
par contredire le moteur d'automatisation. Le travail consiste donc à **extérioriser les
transitions qui existent déjà**, puis à laisser les projets en déclarer d'autres.

C'est aussi ce qui donne le critère de réussite du chantier, et il est brutal :

> **La chaîne de vérification actuelle doit se réécrire intégralement comme un plan
> déclaré, et se comporter à l'identique.** Si elle n'y entre pas, le modèle est trop
> pauvre, et on l'apprend avant d'avoir écrit l'interface.

---

## 14.2 Trois notions, et le refus d'une quatrième

### Événement

Un **fait**, au passé, avec une charge utile. Jamais un ordre : `release.built` dit qu'une
construction s'est terminée, il ne dit pas quoi faire.

La distinction n'est pas cosmétique. Un événement est **rejouable** — on peut le relire, le
réémettre, l'archiver — là qu'un ordre ne l'est pas.

Les faits viennent de **deux sources**, et la seconde est ce qui rend le mécanisme
réellement extensible.

#### Les faits du noyau

Ce que le dashboard sait déjà, aux points où il le sait déjà :

| Événement | Émis quand | Charge |
|---|---|---|
| `release.created` | une release est déclarée | tag, nature, git_ref, version |
| `release.built` | la construction se conclut | + `outcome`, git_commit, images |
| `release.verified` | toutes les vérifications ont conclu | + `outcome`, score, familles |
| `release.released` | une release est validée pour livraison | tag, auteur |
| `release.promoted` | une promotion aboutit | + `environment` |
| `pipeline.finished` | tout run se termine | kind, `outcome`, job, release |

Six, et pas davantage. `pipeline.finished` est le générique dont les autres sont des
commodités : un projet qui veut réagir à une famille précise le fait par une condition
plutôt que par un septième type.

#### Les faits déclarés par un plugin

Un plugin déclare les faits qu'il sait observer, et le dashboard n'a pas à les connaître.
Un plugin de forge annonce ainsi une demande de fusion ouverte ; un plugin de suivi, une
version publiée ou une demande fermée.

C'est la moitié manquante de l'extensibilité. Sans elle, on peut brancher n'importe quelle
**conséquence** sur un jeu de **causes** figé — et le jour où quelqu'un veut réagir à un
fait que le dashboard ne produit pas, il faut à nouveau modifier le noyau. C'est exactement
la limite qu'on cherche à lever.

**Les types sont préfixés par l'identifiant du plugin.** `gitea.pull_request.opened`, jamais
`pull_request.opened`. Deux plugins de forge coexisteront, et une réaction doit dire lequel
elle écoute : un type nu deviendrait ambigu au premier ajout, et l'ambiguïté se
manifesterait par un plan qui part sur le fait d'un autre.

Le descripteur d'un plugin gagne donc, à côté de ses champs et de ses actions, la
déclaration de ses faits :

```
Événement    gitea.pull_request.opened
Libellé      « Demande de fusion ouverte »
Charge       number, title, author, source_branch, target_branch, sha
```

Les champs de la charge sont **déclarés** et non libres, pour la même raison que les champs
de configuration le sont : c'est ce qui permet à l'éditeur de proposer les appariements
possibles et aux gabarits d'être validés à l'enregistrement, plutôt que de découvrir à
l'exécution qu'on interpole une clé qui n'existe pas.

Un fait dont le plugin est désactivé sur l'instance n'apparaît pas dans l'éditeur, et une
réaction qui l'écoutait ne se déclenche plus — sans être effacée, et en le disant. Même
règle que pour les réglages : éteindre n'est pas supprimer.

### Réaction

La **déclaration** : « sur tel événement, dans tel projet, si telle condition, exécuter tel
plan ». Portée par projet, comme les plugins, et pour la même raison — c'est là que les
spécificités vivent (§12.5).

### Plan

Le **graphe**. C'est ici que se joue la sobriété du chantier, et le piège principal.

Un DAG libre — dépendances arbitraires entre pas — appelle un langage d'expressions, un
ordonnanceur de graphe, une détection de cycles, et une interface que personne ne sait
lire. C'est la pente sur laquelle GitHub Actions et Argo Workflows ont chacun trois cents
pages de documentation.

**Un plan est donc une suite d'étapes.** Chaque étape contient des **gestes** exécutés en
parallèle. L'étape suivante démarre quand la précédente est réglée.

```
étape 1 : [ poser le statut « en cours » ]
étape 2 : [ build web ] [ build cli ] [ build worker ]      ← parallèle
étape 3 : [ tests unitaires ] [ statique ] [ e2e ] [ audit ] ← si l'étape 2 a réussi
étape 4 : [ poser le statut final ]                          ← toujours
```

Ce n'est pas une restriction subie : c'est **exactement** la forme de l'exemple demandé, et
exactement la forme de la chaîne existante. Un DAG achèterait une expressivité que personne
n'a réclamée au prix d'un moteur qu'on ne saurait pas déboguer.

### Ce qu'on refuse d'ajouter

**Un langage d'expressions.** Les conditions restent pauvres, délibérément :

- sur la réaction, un **appariement** sur la charge de l'événement — `kind = final`,
  `environment ∈ {preprod, production}`. Égalité et appartenance, rien d'autre ;
- sur un geste, un `quand` : `succès` / `échec` / `toujours`, relatif à l'étape précédente.

Le jour où quelqu'un demande « si le score est supérieur à 80 et que le tag ne contient pas
`hotfix` », la bonne réponse est d'ajouter un opérateur nommé, pas un interpréteur. Une
configuration qui devient un programme est une configuration qu'on ne peut plus relire, et
le dashboard n'offre aucun débogueur pour elle.

---

## 14.3 Les gestes

Un geste est **une action** ou **un pipeline**. La seconde forme est ce qui fait tenir tout
l'édifice : elle réutilise la file existante, ses plafonds, ses classes d'exclusion, son
plafond de tentatives. Il n'y a pas de second exécuteur.

### Les actions internes

Trois, tirées de ce qui a été demandé, et pas une de plus :

| Action | Fait | Notes |
|---|---|---|
| `webhook` | un POST JSON vers une URL | c'est aussi la notification de messagerie — Mattermost, Slack et le reste sont des webhooks, et leur donner une action dédiée serait trois fois le même code |
| `gitea.status` | pose un statut de commit | **action de plugin** — Gitea expose `POST /repos/{o}/{r}/statuses/{sha}` |
| `gitea.comment` | publie un commentaire | **action de plugin** — sur la demande de fusion que le fait désigne |

**Les deux dernières ont changé de camp**, et c'est le résultat de la tranche 7. Elles
s'appelaient `forge.status` et `forge.comment`, et vivaient dans le registre interne. Poser
un statut de commit est un geste de **forge** : le déclarer dans le noyau faisait qu'ajouter
une seconde forge aurait demandé de modifier `internal/action`. Le renommage est sans coût —
elles étaient déclarées et rien ne les exécutait.

**Ce qui n'y est pas, et pourquoi.** Écrire dans le suivi — commenter une demande Redmine —
n'est pas une action manquante mais une **décision non prise** : le transport de
`internal/redmine` refuse structurellement toute méthode autre que GET et HEAD, et c'est un
choix documenté, pris parce que la clé visée est celle de la production du client. L'ouvrir
est un chantier à part, avec sa propre décision.

### Les actions de plugin

Le registre de `internal/plugin` déclare déjà des descripteurs avec leurs champs, leur
portée et leurs secrets. Il gagne une seconde déclaration : les **actions** qu'un plugin
fournit, avec leurs paramètres décrits de la même façon.

Un plugin de messagerie déclarerait ainsi `mattermost.post` avec un champ `canal`, et
hériterait sans rien écrire de la configuration par projet, du scellement des secrets et de
la validation des champs.

**Une propriété structurelle est apparue à l'écriture** : une action de plugin ne peut
déclarer aucun champ sensible. Une action interne en **désigne** un — l'appel sortant nomme
le jeton à poser dans son en-tête — parce qu'elle parle à un destinataire quelconque. Une
action de plugin parle à *sa* frontière, dont la connexion est déjà réglée et scellée : elle
n'a rien à demander. La conséquence vaut d'être dite, parce qu'elle ferme une surface sans
qu'on ait eu à la garder : **une action de plugin ne peut pas servir à faire sortir un secret
vers une cible choisie dans un formulaire**, puisqu'elle ne choisit pas sa cible.

Un champ peut en revanche déclarer un **jeu fermé** de valeurs — l'état d'un statut de commit
vaut `pending`, `success`, `failure` ou `error`. La liste vient du plugin et non d'une
condition écrite dans le validateur : c'est ce qui permet à une seconde forge d'avoir son
propre vocabulaire. Un gabarit traverse ce jeu sans être refusé, sans quoi un état qui
reflète l'issue du fait déclencheur ne pourrait s'écrire qu'en dur — et une réaction en
deviendrait deux.

### Ce qu'un pipeline produit

Un geste de type pipeline ne dit encore rien de ce qui en sortira. Or un pipeline de
construction publie des **images**, un pipeline de test publie des **résultats formalisés**
selon le contrat de doc 13 — et ces deux issues ne se lisent, ne se rattachent ni ne
s'affichent de la même façon.

#### Le mot « famille » n'est pas libre

Il désigne déjà, dans doc 13 et dans tout le code, une **famille de résultats de tests** :
`unit`, `static`, `e2e`, `load`, `security`, `integration`. S'en resservir pour classer les
pipelines produirait deux sens du même mot à un niveau d'écart, et la confusion serait
permanente.

La déclaration porte donc sur le **produit** : ce que le pipeline laisse derrière lui.
C'est plus juste que de le classer, d'ailleurs — un pipeline qui construit *et* teste
n'entre dans aucune catégorie, mais rend parfaitement compte de ses deux produits.

| Produit | Ce que le dashboard en fait |
|---|---|
| `image` | lit les digests publiés, les rattache à la release, autorise le re-tag |
| `résultats` | lit les rapports selon doc 13 : familles, marqueurs, artefacts pré-signés |
| `aucun` | ne retient que le verdict — un déploiement, une migration, un nettoyage |

`produit` est une **liste**, pas une valeur. Un pipeline qui construit une image puis lance
ses tests unitaires dans la foulée déclare les deux, et n'a pas à être coupé en deux pour
entrer dans le modèle.

#### Ce que cela remplace, et non ce que cela double

Le catalogue porte aujourd'hui `verification: bool`, dont le commentaire dit deux choses :
« une construction réussie enchaîne ce pipeline » **et** « il est relançable seul depuis
l'écran d'une release ». Deux propriétés, un booléen.

Doc 14 en emporte la première : l'enchaînement devient un **plan**, déclaré. Ne reste que la
seconde, qui n'est pas une propriété d'enchaînement mais de produit — on relance seul ce
dont on veut relire les résultats.

**Le catalogue réel montre déjà la fêlure.** Sept types y sont déclarés ; six publient
quelque chose ; et l'un d'eux dément le drapeau :

| kind | `verification` | publie des rapports |
|---|---|---|
| `build` | non | non |
| `retag` | non | non |
| `unit-tests` | oui | oui |
| `static-tests` | oui | oui |
| `integration-tests` | oui | oui |
| `security-audit` | oui | oui |
| **`e2e-tests`** | **non** | **oui** |

`e2e-tests` publie trois artefacts et n'est pas une « vérification ». Le drapeau y dit
« non » parce qu'il exprime *l'enchaînement* — une campagne E2E s'exécute contre un
environnement partagé, elle ne part pas d'office après un build —, pendant que l'ingestion,
elle, se fie aux `uploads` déclarés. Deux axes indépendants, dont un seul est nommé : la
ligne où ils divergent existe déjà, dans le déploiement en service.

#### Ce que la déclaration achète

**Le contrat de doc 13 devient vérifiable.** Doc 12 §12.6 le disait sans pouvoir y remédier :
« un pipeline qui ne l'honore pas produit un écran vide, et rien ne dira pourquoi ». Un
pipeline qui déclare `résultats` et n'en publie aucun est désormais en **rupture de
contrat**, nommable et signalable — au lieu d'un écran vide qu'on met une demi-journée à
diagnostiquer.

**Les faits sont typés.** `pipeline.finished` ne porte pas la même charge selon le produit :
des digests d'un côté, des familles et un score de l'autre. Sans la déclaration, l'éditeur
de réaction ne saurait pas quoi proposer d'apparier, et un gabarit interpolerait une clé
absente.

**Un plan se valide.** « Ces gestes construisent, ceux-là vérifient » cesse d'être une
convention de nommage — `*-tests` — pour devenir une propriété lue. Un plan qui attend des
résultats d'un pipeline qui n'en produit pas est refusé à l'enregistrement.

**L'interface groupe.** Le catalogue et les pipelines activés par projet se rangent par ce
qu'ils font, plutôt que par l'ordre du fichier.

#### La migration

Le défaut se déduit sans rien casser : `verification: true` vaut `produit: [résultats]`,
et le type `build` — la seule constante en dur de l'ordonnanceur, `buildJobKind` — vaut
`produit: [image]`. Le reste vaut `[aucun]`.

Restent deux cas que la déduction ne couvre pas, et c'est très bien qu'ils se voient :
`e2e-tests`, dont le produit est `résultats` malgré son drapeau, et `retag`, qui produit une
image sans être une construction. Les corriger est une ligne de catalogue chacun.

---

## 14.4 D'où viennent les faits d'un plugin

Déclarer un fait est facile. Le **produire** est le vrai sujet, et l'exemple donné le dit
sans le dire : « lorsqu'**il en détecte** une nouvelle ». Détecter, pas recevoir. Deux
mécanismes très différents se cachent derrière ce mot.

### Sonde plutôt que réception

| | Sonde | Réception |
|---|---|---|
| Sens | le dashboard interroge | la forge pousse |
| Latence | l'intervalle | immédiate |
| Surface exposée | aucune | **un point d'entrée public, non authentifié par nature** |
| Machinerie | existe déjà | à écrire : signature, anti-rejeu, corrélation |
| Dépend de | rien côté client | un droit d'administration sur le dépôt |

**La sonde d'abord**, pour trois raisons qui tiennent ensemble.

La machinerie existe. `TrackerSync` fait exactement cela depuis le lot C : un curseur par
projet, un intervalle, une lecture incrémentale avec recouvrement, un échec isolé par
projet. La table `sync_state` porte déjà `(source, project_id)` depuis la migration 0043 —
une sonde de forge y écrit sous `gitea.pull_requests` sans qu'aucune table soit ajoutée.

La réception ajoute une **surface exposée**, et c'est une décision d'une autre nature qu'un
point d'extension. Un endpoint que la forge peut appeler est un endpoint que n'importe qui
peut appeler ; il demande une signature partagée, une fenêtre anti-rejeu, et un chemin qui
contourne l'authentification OIDC de tout le reste. Cela mérite sa propre décision, pas
d'être emporté dans celle-ci.

Enfin, [B6] a déjà tranché ce débat pour le suivi : aucun greffon de webhook n'est
déployable sur l'instance Redmine du client, et l'interrogation périodique y est la voie
**définitive** et non un repli. La forge est plus accueillante, mais rien n'oblige à
diverger tant que la sonde suffit.

### Le contrat d'une sonde

Une sonde est une fonction, et le plugin la fournit :

```
Sonde(ctx, connexion, curseur) → (faits, nouveau curseur, erreur)
```

Trois obligations, et chacune corrige une panne connue :

**1. Le fait porte sa propre clé de déduplication.** Le plugin est le seul à savoir ce qui
identifie ses faits — pour une demande de fusion, son numéro dans le dépôt. Le moteur ne
peut pas la deviner, et la dériver de la lecture serait faux : la sonde relit une fenêtre de
recouvrement à chaque passe, comme `TrackerSync` le fait sur une minute pour ne pas perdre
une modification à cheval sur deux secondes. Sans clé stable, **chaque passe renotifierait**.

**2. La date du fait est celle du fait, pas de la lecture.** Une sonde qui rattrape trois
jours d'histoire doit produire des événements datés de ces trois jours, sinon tout paraît
avoir eu lieu à l'instant et l'ordre est perdu.

**3. La première passe n'émet rien.** Elle **établit le curseur** et s'arrête là.

La troisième est la moins évidente et la plus coûteuse à découvrir : sans elle, activer une
sonde sur un dépôt qui porte quatre cents demandes de fusion émet quatre cents faits, donc
déclenche quatre cents plans, donc envoie quatre cents notifications. Une activation ne doit
rien réveiller du passé.

### Isolation et charge

Chaque sonde tourne **par projet**, avec la connexion résolue de son plugin — donc son
serveur et sa clé, superposés comme le reste (§12.5). L'échec de l'une ne suspend pas les
autres : c'est la règle que la synchronisation du suivi vient d'apprendre, et il n'y a pas
de raison de la réapprendre ici.

La charge se compte : *nombre de projets × nombre de sondes × fréquence*. Un plancher
d'intervalle est imposé par le déploiement, et chaque sonde déclare le sien au-dessus. Une
sonde qui interroge une forge tierce toutes les dix secondes pour trente projets est un déni
de service involontaire.

### La preuve du contrat

**La synchronisation du suivi est la première sonde.** Elle produirait
`redmine.version.created`, `redmine.issue.closed` — ce qu'elle observe déjà sans rien en
faire d'autre que projeter en base.

C'est le même argument qu'au §14.1 : un contrat de sonde écrit sur la seule sonde Gitea en
épouserait la forme sans qu'on s'en aperçoive. Écrit sur deux réalisations dont l'une existe
et tourne depuis des mois, il a une chance d'être juste.

#### Ce que la migration a effectivement révélé

**Elle a eu lieu.** Le contrat n'est plus hypothétique, et il n'a pas tenu tel quel : la
seconde réalisation a mis au jour deux manques, tous deux corrigés.

**1. La cadence appartenait à la boucle, pas à la sonde.** Le suivi se relit toutes les cinq
minutes — une saisie humaine ne va pas plus vite —, une forge se regarde plus souvent. Une
cadence unique aurait forcé les deux à s'accorder, et la plus lente aurait fait la loi. Une
sonde déclare désormais son `Schedule`, au-dessus du plancher du déploiement. Le document
l'avait pourtant annoncé — « chaque sonde déclare le sien au-dessus » — et le code ne l'avait
pas fait ; personne ne s'en était aperçu, parce qu'avec une seule sonde la distinction ne se
voit pas.

**2. L'amorçage jetait ses faits au lieu de les retenir, et c'était un défaut.** La règle
disait « la première passe n'émet rien ». Elle suffisait tant que toute sonde **filtrait sur
son curseur** : la sonde Gitea ne rend que les demandes ouvertes depuis, et ne les revoit
jamais.

Le suivi rend un **instantané** — l'API de Redmine ne filtre pas les versions sur
`updated_on`, elles sont relues en entier à chaque passe. Jetés à l'amorçage sans être
retenus, ces faits seraient revenus neufs au tour suivant, et l'auraient tous déclenché.
« Une activation ne réveille pas le passé » n'aurait été vrai que d'un tour.

La règle est donc devenue : la première passe **enregistre sans aiguiller**. C'est la
formulation juste — ce qu'on veut taire est la *notification*, pas le *constat*.

Ce défaut était présent depuis la tranche 5 et invisible avec une seule réalisation. C'est
exactement ce que la garde de §14.1 cherchait à provoquer, appliqué au contrat de sonde.

#### Mesuré contre l'instance réelle

Le défaut ci-dessus n'est pas une déduction. Passé sur `dproj.cnous.fr`, projet `mse-dev`,
en lecture seule :

| Passe | Faits rendus | Dont clés déjà vues |
|---|---|---|
| amorçage (sans curseur) | **58** — 20 versions, 38 clôtures | — |
| passe suivante (avec curseur) | **20** — les versions, relues en entier | **20** |

Les vingt versions reviennent à chaque passe, comme prévu : l'API ne les filtre pas. Leurs
clés sont stables, donc aucune ne se reconstate. **Avant la correction**, l'amorçage les
jetait sans les retenir — et la passe suivante en aurait fait vingt faits neufs, donc vingt
déclenchements, cinq minutes après l'activation.

Le coût de l'amorçage est au passage rassurant : cinquante-huit lignes, pas quatre cents.

`make test-live` rejoue la vérification.

#### Ce que le contrat a absorbé sans se tordre

Tout le reste, et il faut le dire aussi : réglages du plugin par projet, descellement de la
clé, curseur dans `sync_state`, isolation des échecs, activation par projet. La
synchronisation en portait sa propre version, écrite six mois plus tôt ; il n'en reste rien.

La mesure la plus nette est l'interface `TrackerStore` : **neuf méthodes avant, quatre
après**. Ce qui reste est le travail propre au suivi — déposer des versions, adopter les
provisoires, projeter des demandes, lire une correspondance de statuts.

Une seule chose a dû être ajoutée au contrat, et c'est une correction de sa **description**
plutôt que de sa forme : une sonde peut persister ses **propres** données. Elle ne persiste
pas ses *faits* — cela reste le travail de la boucle —, mais lui interdire toute écriture
aurait demandé un second mécanisme pour faire tourner exactement la même boucle.

#### Ce que le suivi constate désormais

`redmine.version.created` et `redmine.issue.closed`, comme annoncé. Deux, et pas davantage :
ce sont des **événements**, quelque chose est arrivé une fois. Une demande qui change de
statut n'en est pas un — elle en change dix fois, et un fait par changement ferait du fil des
faits un journal de saisie.

La sémantique de « created » ne vient pas d'un drapeau rendu par le magasin mais de la
**clé de déduplication** : chaque passe reconstate toutes les versions, et seule l'unicité de
la clé fait qu'une version déjà vue n'est pas réémise. C'est cette forme — un instantané
rendu en entier, dédupliqué par clé — que le contrat ne connaissait pas.

---

La migration n'est pas indispensable au premier jour — la sonde Gitea seule suffit à livrer
le besoin. Mais tant qu'elle n'a pas eu lieu, le contrat reste **hypothétique**, et le
document doit le dire plutôt que de laisser croire qu'il est éprouvé.

---

## 14.5 Ce qui ne bouge pas : la machine à états

**Règle porteuse : le moteur de plans n'écrit jamais l'état d'une release.**

Sans elle, deux mécanismes décideraient de la même valeur, et le premier bogue serait une
release passée `ready` par un plan pendant que l'observateur la croit `verifying`.

Le partage est donc :

| | décide | émet | consomme |
|---|---|---|---|
| Machine à états (Go) | l'état d'une release | des événements | des conclusions de jobs |
| Moteur de plans | ce qui s'exécute | des événements | des événements |

La chaîne existante se relit alors ainsi, sans rien perdre : `advanceRelease` fait passer la
release en `verifying` **et** émet `release.built` ; le plan par défaut de cet événement
enfile les familles de vérification du projet ; `concludeVerification` compte les jobs qu'il
trouve — ce qu'il fait déjà — et conclut.

Les deux composent sans que l'un possède l'autre. C'est le test du §14.1, et il est
réalisable dès la deuxième tranche.

---

## 14.6 Livraison au plus juste, et l'idempotence

Le piège le plus coûteux de ce chantier n'est ni le modèle ni l'interface. C'est celui-ci :

> L'observateur **redélivre** les runs terminés — resynchronisation de l'informer,
> redémarrage du worker. Le code actuel s'en garde explicitement, et le commentaire de
> `advanceRelease` raconte le jour où cinq paires de tests sont parties pour une seule
> construction.

Un moteur naïf enverrait un message de messagerie à chaque redémarrage du worker. Trois
règles structurelles :

1. **Les événements sont persistés avec une clé de déduplication** dérivée du fait lui-même
   — `release.built:<release_id>:<job_id>` — insérés en `on conflict do nothing`. Réémettre
   le même fait ne crée rien. Pour un fait de plugin, **c'est le plugin qui fournit la
   clé** : lui seul sait ce qui identifie ses faits, et une sonde relit une fenêtre de
   recouvrement à chaque passe (§14.4).
2. **Une exécution de plan est unique par (réaction, événement)**, tenue par un index. Deux
   workers, ou deux redélivrances, produisent une exécution.
3. **Les actions portent une clé d'idempotence** dans leur requête sortante, pour que le
   destinataire puisse se protéger aussi. Le dashboard promet une livraison **au moins une
   fois** ; il ne peut pas promettre mieux, et le dire est plus honnête que de le laisser
   croire.

---

## 14.7 Sécurité

Cette section n'est pas un appendice : c'est ce qui décide si le chantier est
raisonnable.

### L'appel sortant configurable est le risque principal

Une URL de webhook saisie dans l'interface devient une requête HTTP émise **depuis
l'intérieur du cluster**, par le worker, qui porte un jeton de compte de service. C'est une
SSRF offerte, et les cibles intéressantes sont connues :

- `169.254.169.254` — les métadonnées cloud, donc des identifiants d'instance ;
- l'API Kubernetes elle-même ;
- les services internes du cluster, que rien n'expose autrement ;
- la boucle locale du pod.

La parade suit le précédent de la maison. `internal/redmine` et `internal/gitea` refusent
l'écriture **dans leur transport**, avant le réseau, plutôt que par la discipline des
appelants — parce qu'une garantie structurelle ne s'oublie pas. Le client de webhooks aura
son transport, et il refusera :

- toute adresse résolue en boucle locale, lien-local, ou plage privée, sauf autorisation
  explicite du déploiement (un dashboard local a besoin de `localhost`) ;
- toute redirection — pas de suivi, du tout : une redirection est le contournement évident
  d'un contrôle fait sur l'URL de départ ;
- tout schéma autre que `https`, sauf la même autorisation explicite.

**Le réamorçage DNS mérite sa ligne.** Vérifier l'URL puis laisser le client résoudre à
nouveau laisse une fenêtre où le nom change de réponse. La résolution a lieu une fois, les
adresses sont contrôlées, et la connexion se fait **sur l'adresse contrôlée**.

### Les secrets ne traversent pas les gabarits

Une action compose sa charge à partir de l'événement. Si un champ scellé pouvait être
interpolé dans une URL ou un corps, il finirait dans un journal d'accès.

Règle : **un champ sensible n'est jamais une variable de gabarit.** Il ne peut être que
*désigné* comme en-tête d'authentification — « le jeton du plugin X va dans
`Authorization` » — et le moteur le pose sans jamais le rendre lisible.

### Les boucles

Une action peut provoquer un fait qui déclenche la même action. Chaque événement porte une
**profondeur**, héritée de l'événement qui l'a causé et incrémentée ; au-delà d'un seuil,
l'événement est enregistré et **non traité**, avec sa raison. Sans ce compteur, une
configuration maladroite occupe la file indéfiniment.

### Le périmètre du droit

Déclarer une réaction, c'est faire émettre des appels au nom de l'instance et lancer des
pipelines. C'est `admin`, comme les plugins — et non `release:edit`, qui appartient à ceux
qui conduisent une livraison.

### Ce qu'une sonde peut faire de travers

Une sonde ne sort pas vers une cible saisie par un utilisateur — elle interroge le serveur
déjà configuré du plugin, celui que la forge ou le suivi déclare. Le risque n'est donc pas
la SSRF mais la **charge** et la **fuite par la charge utile**.

**La charge.** *Projets × sondes × fréquence*, et le multiplicande est une machine qui n'est
pas la nôtre. Un plancher d'intervalle vient du déploiement, chaque sonde déclare le sien
au-dessus, et un échec répété espace les tentatives au lieu de s'entêter.

**La charge utile.** Un fait de plugin porte ce que le plugin y met, et un titre de demande
de fusion contient parfois ce qu'un développeur pressé y a collé. Cette charge finira dans
un corps de webhook, donc chez un tiers. La règle du gabarit vaut ici aussi, dans l'autre
sens : les champs déclarés sont les seuls interpolables, et une sonde qui rendrait un champ
non déclaré le voit **écarté**, pas transmis.

### L'interrupteur

Un interrupteur global d'arrêt des automatisations, un par réaction, et un par sonde. Quand
une configuration part en boucle un vendredi soir, la première chose dont on a besoin est de
l'arrêter sans redéployer.

Les trois sont distincts à dessein : couper une sonde qui martèle une forge tierce ne doit
pas suspendre les plans déclenchés par les faits du noyau.

---

## 14.8 Schéma

Quatre tables, et **aucune pour les curseurs de sondes**. Les noms suivent l'usage du
dépôt : le français pour le domaine, l'anglais pour ce qui vient de l'infrastructure.

```sql
-- Les faits. Persistés parce qu'ils sont rejouables, et parce que la
-- déduplication a besoin d'une trace.
events (
  id, type, project_id, subject, payload jsonb,
  occurred_at,                   -- l'instant du **fait**, jamais celui de sa lecture
  depth,                         -- profondeur d'enchaînement, contre les boucles
  -- `source` distingue le noyau d'un plugin. Rendu explicite plutôt que
  -- déduit du préfixe du type : un plugin retiré d'une version laisse des
  -- faits qu'on doit encore savoir lire et attribuer.
  source text,                   -- '' pour le noyau, l'identifiant du plugin sinon
  dedup_key text unique          -- « release.built:42:1381 »
)

-- Les déclarations, par projet.
reactions (
  id, project_id, name, event_type, enabled,
  match jsonb,                   -- { "kind": "final" }
  plan  jsonb,                   -- les étapes et leurs gestes
  created_by, updated_at
)

-- Une exécution de plan. Unique par (réaction, événement) : c'est la
-- borne qui empêche une redélivrance de tout rejouer.
reaction_runs (
  id, reaction_id, event_id, state, stage,
  started_at, finished_at, error,
  unique (reaction_id, event_id)
)

-- Chaque geste, et son sort. `job_id` est le point de couture avec la
-- file : un geste de type pipeline **est** un job, et n'a pas d'exécuteur
-- à lui.
reaction_steps (
  id, run_id, stage, position, kind, name, state,
  job_id references jobs(id),
  detail jsonb,                  -- code HTTP, extrait de réponse, motif
  started_at, finished_at
)
```

Le `plan` est du JSON et non des tables normalisées : c'est un document qu'on édite d'un
bloc, jamais interrogé par morceaux, et l'éclater en trois tables coûterait trois jointures
pour aucune requête réelle. Il est **validé à l'enregistrement** contre le registre des
actions et le catalogue des pipelines — une action inconnue ou une famille absente est
refusée à la saisie, jamais découverte à l'exécution. C'est la règle déjà appliquée aux
champs de plugins.

**Les curseurs de sondes n'ajoutent aucune table.** `sync_state` porte déjà
`(source, project_id)` depuis la migration 0043, avec sa dernière tentative, son dernier
succès et son dernier message d'erreur. Une sonde de forge y écrit sous
`gitea.pull_requests` comme la synchronisation du suivi y écrit sous `redmine`.

Ce n'est pas une économie opportuniste : ces deux mécanismes **sont** la même chose — une
lecture périodique, incrémentale, bornée par un curseur, par projet — et leur donner deux
tables aurait été le signe qu'on n'avait pas vu qu'elles n'en faisaient qu'une.

---

## 14.9 Où le code se pose

```
internal/event/       le vocabulaire des faits, leurs types, leur clé de déduplication
internal/plan/        le modèle de plan, sa validation, son avancement — sans I/O
internal/action/      le registre des actions internes et leur contrat
internal/webhook/     le client sortant et son transport à cible contrôlée
internal/worker/      deux boucles : les sondes qui produisent, le moteur qui consomme
```

`internal/plugin` gagne deux déclarations à côté de ses champs : les **actions** qu'un
plugin fournit, et les **faits** qu'il sait observer. Rien d'autre ne bouge dans ce paquet —
c'est un registre à la compilation, et deux listes de plus n'en changent pas la nature.

`internal/plan` **sans entrées-sorties** est délibéré, et c'est le même geste que pour
`internal/release` : l'avancement d'un plan — quelle étape est réglée, quel geste part
ensuite, que faire d'un échec — est de la logique de décision. Elle s'éprouve sans base et
sans cluster, ce qui est la seule façon d'avoir confiance dans un moteur qu'on ne verra
tourner en entier que rarement.

**Deux boucles dans le worker, et non une.** Les sondes produisent des faits ; le moteur les
consomme et fait avancer les plans. Les fondre donnerait un tour de boucle dont la durée
dépend à la fois d'une forge tierce et de l'exécution d'un plan — une forge lente
retarderait alors la conclusion d'une étape, pour aucune raison de fond.

Les deux vivent **sous le bail de leader**, comme tout ce qui sort. L'`api` ne fait que lire
et écrire des déclarations : elle tourne sans jeton de cluster, délibérément, parce que
c'est la surface exposée (§12.7).

---

## 14.10 L'interface

### Déclarer

`/admin/projects/:slug/automations`, cinquième sous-onglet de la page d'un projet — à côté
des plugins et des pipelines, parce que c'est le même geste : dire ce que ce projet fait de
particulier.

**Un éditeur structuré, pas un champ de texte.** Un YAML libre est le chemin le moins cher
et le pire : il échoue à l'enregistrement, sur un message de parseur, pour une virgule. Les
étapes sont des lignes, les gestes des vignettes qu'on ajoute dans une ligne, et le JSON
reste visible en lecture seule pour qui veut le relire ou le recopier.

La forme du modèle rend cet éditeur possible : des étapes et des gestes parallèles se
dessinent en colonnes empilées. Un DAG libre aurait demandé un éditeur de graphe, et c'est
la seconde raison de ne pas en faire un.

**L'éditeur se compose à partir des registres**, jamais d'une liste écrite dans l'interface.
Le choix de l'événement énumère les faits du noyau et ceux des plugins actifs sur ce projet ;
choisir un fait détermine les champs appariables et les variables de gabarit disponibles,
puisque la charge est déclarée (§14.2) ; le choix d'un geste énumère les actions du registre
et les familles du catalogue.

C'est ce qui évite l'écran qui accepte tout et échoue à l'exécution — et c'est la même règle
que pour les champs de plugins, où le serveur décrit et le navigateur affiche.

**Un fait dont le plugin a été éteint depuis** ne disparaît pas de la réaction qui l'écoute :
elle s'affiche inerte, en le disant. L'effacer silencieusement ferait perdre une
configuration pour une extinction peut-être temporaire.

### Constater

**Un moteur qu'on ne peut pas déboguer est pire qu'un comportement figé**, parce qu'on ne
sait plus ce qui a eu lieu. L'exécution d'un plan est donc un objet de première classe :

- sur l'écran d'une release, les plans déclenchés par ses faits, avec leur avancement ;
- une vue dédiée sous la coquille des exécutions, où l'on voit les plans comme on voit les
  runs ;
- le détail d'une exécution : l'événement qui l'a causée, chaque étape, chaque geste, son
  sort, et pour une action son code de réponse et un extrait — jamais les en-têtes envoyés,
  qui portent l'authentification.

Un fil d'événements en lecture, aussi. C'est ce qui permet de répondre à « pourquoi rien ne
s'est passé ? » : ou bien le fait n'a pas été émis, ou bien aucune réaction ne l'appariait,
et ce sont deux corrections différentes.

---

## 14.11 Le découpage

Sept tranches. Chacune se termine sur quelque chose de vérifiable, et la quatrième est le
point de non-retour.

### Tranche 1 — Ce que les pipelines produisent

`produit` dans le catalogue, la déduction depuis `verification`, et les deux corrections
qu'elle laisse voir — `e2e-tests` et `retag`.

Elle vient en premier parce que tout le reste s'y adosse : un fait `pipeline.finished` ne
porte pas la même charge selon le produit, et l'écrire avant de savoir le distinguer
obligerait à y revenir. Elle se livre seule, sans rien changer au comportement, et rend déjà
service — la rupture du contrat de doc 13 devient signalable.

### Tranche 2 — Les faits, sans personne pour les écouter

Émettre les six événements, les persister, les afficher. **Aucun comportement ne change.**

Livrable pauvre en apparence, décisif en réalité : il éprouve que les points d'émission sont
les bons et que la déduplication tient à travers un redémarrage du worker. On les découvre
faux ici, où la correction ne coûte rien, plutôt qu'après avoir écrit un éditeur.

### Tranche 3 — Le moteur, et la preuve

`internal/plan`, la boucle du worker, l'enfilement des gestes de type pipeline.

**La chaîne de vérification actuelle est réécrite comme plan par défaut**, et doit se
comporter à l'identique — mêmes familles enfilées, même conclusion, même idempotence à la
redélivrance. Les tests existants de `verification_test.go` sont le juge, et ils ne sont pas
réécrits pour l'occasion.

Si cette tranche n'aboutit pas, le modèle est trop pauvre. On l'apprend avant d'avoir écrit
une ligne d'interface, et c'est tout l'intérêt de l'ordre choisi.

### Tranche 4 — Les actions sortantes

Le transport à cible contrôlée et ses tests — y compris ceux qui vérifient qu'une adresse
de métadonnées cloud et une redirection sont refusées —, le registre des actions, et
`webhook`.

C'est la tranche qui fait sortir des requêtes du cluster au nom de l'instance. Elle ne part
pas avant que la précédente soit tenue.

**`forge.status` est déclarée et pas encore exécutée ; `forge.comment` n'est pas déclarée.**
Les deux demandent un client d'écriture vers la forge, et la seconde demande surtout un
**numéro de demande de fusion** — que rien ne fournit dans un plan déclenché par une
release. Ce numéro arrivera avec `gitea.pull_request.opened`, et l'action prendra alors son
sens en même temps que sa source. L'écrire d'avance produirait un paramètre que personne ne
sait renseigner.

### Tranche 5 — Les sondes, et les faits de plugin

Le contrat de sonde, sa boucle, et la sonde `gitea.pull_request.opened` — celle qui a motivé
la demande.

Trois choses s'éprouvent ici, et aucune ne s'éprouve à la lecture du code : qu'une
activation **n'émet rien du passé**, qu'une seconde passe sur la même fenêtre de
recouvrement **ne renotifie pas**, et qu'une forge injoignable **n'empêche pas** les autres
projets d'avancer. Ce sont trois tests, et ils valent la tranche à eux seuls.

La synchronisation du suivi devient la seconde sonde — ou ne le devient pas, et le contrat
reste alors calqué sur une seule réalisation. Le §14.4 dit pourquoi c'est un compromis et
non un détail d'ordonnancement.

> **Où en est le contrat.** Il est écrit et éprouvé sur une seule réalisation, la sonde
> Gitea. La synchronisation du suivi **n'a pas migré** : elle garde sa boucle, son curseur
> et son isolation par projet, qui sont les mêmes mécanismes sous un autre nom. Tant qu'elle
> n'aura pas migré, le contrat reste **hypothétique** — il n'a pas eu à absorber une seconde
> forme, et rien ne dit qu'il le pourrait sans se tordre.

### Tranche 6 — L'interface

L'éditeur structuré, la vue des exécutions, le fil des faits.

Elle vient après les sondes, et non avant : un éditeur écrit sur les seuls faits du noyau
n'aurait pas à afficher de types préfixés, ni de charge déclarée par un tiers, ni de fait
dont le plugin est éteint. Il faudrait le reprendre.

### Tranche 7 — Les actions de plugin

Le registre s'ouvre dans l'autre sens. Rien ne l'exige avant qu'un plugin réel ait une action
à fournir : l'écrire d'avance produirait un point d'extension calqué sur les trois actions
internes, qu'on croirait général (§12.4).

**Les faits de plugin, eux, sont en tranche 5 et non ici** — ils sont la moitié qui a été
demandée, et celle sans laquelle les causes resteraient figées.

#### Ce que la condition attendait, et qui est arrivé

La garde ci-dessus a été respectée : le point d'extension n'a **pas** été écrit d'avance. Il
l'a été le jour où deux actions réelles l'attendaient — `forge.status`, déclarée en tranche 4
et jamais exécutée faute d'un client d'écriture ; `forge.comment`, qui n'était même pas
déclarée parce qu'elle demandait un numéro de demande de fusion que seule la sonde de la
tranche 5 pouvait fournir. Les deux sont devenues `gitea.status` et `gitea.comment`, et le
plugin qui observait sait maintenant agir.

Ce que ce calendrier a évité se voit dans le résultat : la déclaration d'une action de plugin
n'est pas la copie de celle d'une action interne. Elle n'a **pas** de champ sensible, et elle
a un **jeu fermé** — deux écarts qu'une abstraction écrite en tranche 4, sur les seules
actions internes, n'aurait ni prévus ni permis.

**Ce qui reste ouvert.** L'écriture dans le suivi n'a pas bougé : le transport de
`internal/redmine` refuse toujours toute méthode autre que GET et HEAD, et c'est le même
choix documenté qu'en §14.3. Un plugin peut désormais déclarer une action — le chemin
existe — mais la décision d'ouvrir cette clé-là n'est pas prise.

---

## 14.12 Ce que ce chantier coûte

**Version longue retenue.** Sept tranches, deux boucles de worker, un éditeur de graphe
simplifié, une surface sortante à sécuriser, et un contrat de sonde. Le dire d'emblée est
plus utile que de le découvrir à la cinquième.

Deux tranches sont des **portes** plutôt que des livraisons, et il faut les traiter comme
telles :

- la **tranche 3** décide si le modèle de plan tient : si la chaîne de vérification n'y
  entre pas, on s'arrête et on révise, avant toute interface ;
- la **tranche 5** décide si le contrat de sonde tient : si la sonde Gitea ne s'y coule pas
  sans le tordre, c'est que le contrat a été écrit trop tôt.

Le reste est du travail ordinaire, et son coût est proportionné à ce qu'il livre.

**Ce qui n'est pas dans le périmètre**, et qu'il vaut mieux nommer que laisser croire
acquis : la réception de webhooks entrants (§14.4), l'écriture dans le suivi (§14.3), et un
langage d'expressions dans les conditions (§14.2). Chacun est une décision à part, aucun
n'est fermé pour toujours.

---

## 14.13 Décisions à prendre avant d'écrire

| # | Question | Réponse |
|---|---|---|
| 1 | Version longue ou courte ? | **longue** — tranché |
| 2 | Les plugins déclarent-ils leurs propres faits ? | **oui** — tranché ; par sonde, types préfixés, charge déclarée (§14.4) |
| 2 bis | Comment classer les pipelines ? | par leur **produit**, en liste — `image`, `résultats`, `aucun`. « Famille » est déjà pris par les résultats de tests (§14.3) |
| 3 | Les actions internes écrivent-elles dans le suivi ? | **non** en l'état : ouvrir le transport Redmine est une décision à part |
| 4 | Les plans peuvent-ils écrire l'état d'une release ? | **non**, et c'est structurel (§14.5) |
| 5 | Le déploiement autorise-t-il des cibles de webhook privées ? | par une variable explicite, refusé par défaut |
| 6 | Un plan peut-il être défini au niveau de l'instance, et hérité ? | oui, même partage que les plugins : défauts globaux, écarts par projet |
| 7 | La synchronisation du suivi devient-elle une sonde ? | souhaitable — c'est ce qui éprouve le contrat sur deux réalisations plutôt qu'une (§14.4) |
| 8 | Quel plancher d'intervalle pour les sondes ? | à fixer au déploiement ; une minute est un point de départ raisonnable |
