# 14. Déclencheurs et automatisations

> **Objet** — rendre configurable ce qui s'enchaîne après un fait : lancer des pipelines,
> poser un statut, appeler un webhook. Aujourd'hui la chaîne est écrite en Go, la même
> pour tous les projets.

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

Un **fait**, au passé, avec une charge utile. Émis par le dashboard aux points qu'il connaît
déjà. Jamais un ordre : `release.built` dit qu'une construction s'est terminée, il ne dit
pas quoi faire.

La distinction n'est pas cosmétique. Un événement est **rejouable** — on peut le relire, le
réémettre, l'archiver — là qu'un ordre ne l'est pas.

| Événement | Émis quand | Charge |
|---|---|---|
| `release.created` | une release est déclarée | tag, nature, git_ref, version |
| `release.built` | la construction se conclut | + `outcome`, git_commit, images |
| `release.verified` | toutes les vérifications ont conclu | + `outcome`, score, familles |
| `release.released` | une release est validée pour livraison | tag, auteur |
| `release.promoted` | une promotion aboutit | + `environment` |
| `pipeline.finished` | tout run se termine | kind, `outcome`, job, release |

Six, et pas davantage au départ. `pipeline.finished` est le générique dont les autres sont
des commodités : un projet qui veut réagir à une famille précise le fait par une condition
plutôt que par un septième type.

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
| `forge.status` | pose un statut de commit | Gitea expose `POST /repos/{o}/{r}/statuses/{sha}` |
| `forge.comment` | publie un commentaire | sur la demande de fusion rattachée, quand il y en a une |

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

---

## 14.4 Ce qui ne bouge pas : la machine à états

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

## 14.5 Livraison au plus juste, et l'idempotence

Le piège le plus coûteux de ce chantier n'est ni le modèle ni l'interface. C'est celui-ci :

> L'observateur **redélivre** les runs terminés — resynchronisation de l'informer,
> redémarrage du worker. Le code actuel s'en garde explicitement, et le commentaire de
> `advanceRelease` raconte le jour où cinq paires de tests sont parties pour une seule
> construction.

Un moteur naïf enverrait un message de messagerie à chaque redémarrage du worker. Trois
règles structurelles :

1. **Les événements sont persistés avec une clé de déduplication** dérivée du fait lui-même
   — `release.built:<release_id>:<job_id>` — insérés en `on conflict do nothing`. Réémettre
   le même fait ne crée rien.
2. **Une exécution de plan est unique par (réaction, événement)**, tenue par un index. Deux
   workers, ou deux redélivrances, produisent une exécution.
3. **Les actions portent une clé d'idempotence** dans leur requête sortante, pour que le
   destinataire puisse se protéger aussi. Le dashboard promet une livraison **au moins une
   fois** ; il ne peut pas promettre mieux, et le dire est plus honnête que de le laisser
   croire.

---

## 14.6 Sécurité

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

### L'interrupteur

Un interrupteur global d'arrêt des automatisations, et un par réaction. Quand une
configuration part en boucle un vendredi soir, la première chose dont on a besoin est de
l'arrêter sans redéployer.

---

## 14.7 Schéma

Quatre tables. Les noms suivent l'usage du dépôt : le français pour le domaine, l'anglais
pour ce qui vient de l'infrastructure.

```sql
-- Les faits. Persistés parce qu'ils sont rejouables, et parce que la
-- déduplication a besoin d'une trace.
events (
  id, type, project_id, subject, payload jsonb,
  occurred_at, depth,
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

---

## 14.8 Où le code se pose

```
internal/event/       le vocabulaire des faits, leurs types, leur clé de déduplication
internal/plan/        le modèle de plan, sa validation, son avancement — sans I/O
internal/action/      le registre des actions internes et leur contrat
internal/webhook/     le client sortant et son transport à cible contrôlée
internal/worker/      la boucle qui consomme les événements et fait avancer les plans
```

`internal/plan` **sans entrées-sorties** est délibéré, et c'est le même geste que pour
`internal/release` : l'avancement d'un plan — quelle étape est réglée, quel geste part
ensuite, que faire d'un échec — est de la logique de décision. Elle s'éprouve sans base et
sans cluster, ce qui est la seule façon d'avoir confiance dans un moteur qu'on ne verra
tourner en entier que rarement.

La boucle vit dans le `worker`, **sous le bail de leader**, comme tout ce qui sort. L'`api`
ne fait que lire et écrire des déclarations : elle tourne sans jeton de cluster,
délibérément, parce que c'est la surface exposée (§12.7).

---

## 14.9 L'interface

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

## 14.10 Le découpage

Cinq tranches. Chacune se termine sur quelque chose de vérifiable, et la troisième est le
point de non-retour.

### Tranche 1 — Les faits, sans personne pour les écouter

Émettre les six événements, les persister, les afficher. **Aucun comportement ne change.**

Livrable pauvre en apparence, décisif en réalité : il éprouve que les points d'émission sont
les bons et que la déduplication tient à travers un redémarrage du worker. On les découvre
faux ici, où la correction ne coûte rien, plutôt qu'après avoir écrit un éditeur.

### Tranche 2 — Le moteur, et la preuve

`internal/plan`, la boucle du worker, l'enfilement des gestes de type pipeline.

**La chaîne de vérification actuelle est réécrite comme plan par défaut**, et doit se
comporter à l'identique — mêmes familles enfilées, même conclusion, même idempotence à la
redélivrance. Les tests existants de `verification_test.go` sont le juge, et ils ne sont pas
réécrits pour l'occasion.

Si cette tranche n'aboutit pas, le modèle est trop pauvre. On l'apprend avant d'avoir écrit
une ligne d'interface, et c'est tout l'intérêt de l'ordre choisi.

### Tranche 3 — Les actions sortantes

`webhook`, `forge.status`, `forge.comment`, et le transport à cible contrôlée avec ses
tests — y compris ceux qui vérifient qu'une adresse de métadonnées cloud et une redirection
sont refusées.

C'est la tranche qui fait sortir des requêtes du cluster au nom de l'instance. Elle ne part
pas avant que la précédente soit tenue.

### Tranche 4 — L'interface

L'éditeur structuré, la vue des exécutions, le fil des faits.

### Tranche 5 — Les actions de plugin

Le registre s'ouvre. Rien ne l'exige avant qu'un plugin réel ait quelque chose à déclarer :
l'écrire d'avance produirait un point d'extension calqué sur les trois actions internes,
qu'on croirait général (§12.4).

---

## 14.11 Ce que ce chantier coûte, et l'alternative

Il est plus gros que les précédents : cinq tranches, deux boucles de worker, un éditeur de
graphe simplifié, et une surface sortante nouvelle à sécuriser. Le dire d'emblée est plus
utile que de le découvrir à la troisième.

**Une version courte existe**, si le besoin réel est plus étroit que l'énoncé. Les tranches
1 et 3, sans moteur de plans : des réactions **à un seul geste**, sans étapes ni parallélisme
ni conditions. « Sur `release.verified`, appeler ce webhook. » Cela couvre la notification,
le statut Gitea et le commentaire — donc les trois exemples donnés — pour une fraction du
coût, et sans toucher à la chaîne existante.

Ce qu'elle ne couvre pas : « trois builds en parallèle, puis quatre tests si les builds ont
réussi ». Si cet enchaînement-là est le vrai besoin, la version longue est la bonne. Si
c'est une illustration, la version courte livre l'essentiel en deux tranches et laisse la
porte ouverte : les événements et les actions sont les mêmes, seul le moteur manque.

---

## 14.12 Décisions à prendre avant d'écrire

| # | Question | Recommandation |
|---|---|---|
| 1 | Version longue (moteur de plans) ou courte (réaction à un geste) ? | dépend du besoin réel derrière l'exemple — à trancher |
| 2 | Les actions internes ont-elles besoin d'écrire dans le suivi ? | **non** en l'état : ouvrir le transport Redmine est une décision à part |
| 3 | Les plans peuvent-ils écrire l'état d'une release ? | **non**, et c'est structurel (§14.4) |
| 4 | Le déploiement autorise-t-il des cibles de webhook privées ? | par une variable explicite, refusé par défaut |
| 5 | Un plan peut-il être défini au niveau de l'instance, et hérité ? | oui, même partage que les plugins : défauts globaux, écarts par projet |
