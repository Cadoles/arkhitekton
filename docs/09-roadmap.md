# 09 — Roadmap

## 9.1 Principes de découpage

Trois contraintes dictent l'ordre des lots :

1. **Le lot 10 (prérequis `mse-tekton`) conditionne le lot 4.** Sans résultats
   adressables par run et sans format machine, il n'y a pas de vue enrichie des tests.
   Il doit donc démarrer en parallèle du lot 0, pas quand on en aura besoin.
2. **Livrer de la lecture avant de l'écriture.** Un dashboard qui affiche
   correctement l'état existant a déjà de la valeur et permet de valider le modèle de
   données auprès des utilisateurs avant de leur donner des boutons qui déclenchent des
   déploiements.
3. **Les questions ouvertes de [02](02-blocages-et-decisions.md) doivent être
   tranchées avant le lot qui en dépend**, pas pendant.

## 9.2 Lots

### Lot 0 — Fondations

*Prérequis levés : fournisseur OIDC (Gitea) et liste des environnements sont tranchés.*

- Monorepo, `go.mod`, `apps/web`, Makefile, intégration continue Gitea Actions
  (lint, tests, build d'images, `govulncheck`).
- Schéma PostgreSQL initial et migrations, client MinIO.
- Authentification OIDC complète (BFF, sessions, révocation), modèle de rôles et
  middleware d'autorisation, `/me`.
- Manifests kustomize, `ServiceAccount` et `Role`, `ExternalSecret`, déploiement sur le
  cluster kind ([11](11-environnement-de-developpement.md)).
- Installation des CRD Flux et `PortalData` sur kind, sans les contrôleurs, pour que la
  validation au démarrage et les informers tournent sans branche conditionnelle propre
  au développement.
- Configuration MinIO : région **`NUO-Region`**, signature `UNSIGNED-PAYLOAD`, durées
  d'expiration alignées sur les timeouts de pipeline
  ([B14](02-blocages-et-decisions.md#b14), vérifié sur l'instance cible).
- Coquille React : mise en page, navigation, thème, composants de statut portés depuis
  la maquette.

**Livrable :** application déployée, authentifiée, affichant une page vide. C'est ce
qui rend tous les lots suivants indépendants de l'infrastructure.

### Lot 1 — Redmine et versions

*Prérequis levé : aucun plugin webhook n'est déployable sur Redmine
([B6](02-blocages-et-decisions.md#b6)) — l'interrogation périodique incrémentale est la
voie définitive, pas un repli.*

- Client Redmine, synchronisation incrémentale, table de correspondance des statuts.
- Projection `versions` / `issues`, calcul de la complétion.
- Écrans `/versions` et `/versions/:id`.
- Écran d'administration : état des synchronisations, statuts non mappés,
  resynchronisation forcée.

**Livrable :** consultation des versions et de leur avancement. Premier livrable
réellement utile.

### Lot 2 — Observation Tekton

- Informer sur `PipelineRun` et `TaskRun`, projection en base, réconciliation au
  démarrage.
- Collecteur de logs : capture, écriture MinIO par tranches, reprise sur redémarrage.
- SSE pour l'état des runs et les logs.
- Écrans `/runs` et `/runs/:name` avec visionneuse virtualisée.

**Livrable :** tous les runs Tekton — y compris ceux déclenchés par webhook ou CronJob —
sont visibles avec leurs logs, y compris après disparition des pods. Répond à un besoin
opérationnel immédiat, indépendamment du reste.

### Lot 3 — File d'exécution et lancement

*Prérequis : lot 2. Le catalogue de pipelines doit être validé avec l'équipe `mse-tekton`.*

- Catalogue déclaratif, validation au démarrage contre les `Pipeline` du cluster.
- Table `jobs`, ordonnanceur avec `SKIP LOCKED`, élection de leader par `Lease`.
- Classes d'exclusion, plafond global, priorités, et **sémantique de barrière**
  (`exclusive-cluster` : drainage, attente, exécution seule). À prévoir dès maintenant
  même si son seul usage arrive au lot 8 — la greffer après coup impose de reprendre la
  boucle d'ordonnancement.
- Création de `PipelineRun` étiquetés, annulation, réessai.
- Écran `/queue`, boutons de lancement soumis à permission, idempotence.
- Journal d'audit.

**Livrable :** lancement de pipelines depuis l'interface, sans risque de concurrence.
C'est le cœur du premier objectif énoncé par le client.

### Lot 4 — Résultats de tests

*Prérequis : lot 10 (P1–P4, P7) livré, sinon le lot dégénère en scraping de texte.
Le support des URLs pré-signées SigV4 sur `nuo-s3.nuonet.fr` est vérifié : ce lot n'a
plus de prérequis externe.*

- Émission des URLs pré-signées à la création du run, déclaration `uploads` du
  catalogue.
- Extraction des marqueurs `##MSE-META##` / `##MSE-SUMMARY##` par le collecteur de
  logs, résultat partiel affiché en cours d'exécution.
- Parseurs : JUnit XML, JSON PHPStan, JSON cs-fixer, mochawesome, SARIF, composer audit
  — par reprise de `tekton-mse-manager/pkg/security`, rendu importable par P7.
- Normalisation côté dashboard : dedup, empreintes, exceptions appliquées au calcul,
  diff cross-run en transaction PostgreSQL.
- Projection `test_results` / `test_cases`, copie des artefacts à conserver.
- Commande `reprocess` : rejeu de l'historique depuis le brut.
- Calcul du score de qualité, pondération de [B11](02-blocages-et-decisions.md#b11)
  placée en configuration.
- Écrans `/releases/:tag/tests/:family` et `/releases/:tag/security`, comparaison avec
  la release précédente.

**Livrable :** vue enrichie des tests et niveau de qualité d'une version — le second
objectif énoncé.

### Lot 5 — Releases et déploiements

*Prérequis : un compte de service en lecture sur le registre. Tout le reste est tranché — Flux est
déjà en service (promotion par métadonnée SemVer, séparateur `_`, `qualif` désigné
`testing`, pré-releases acceptées jusqu'en pré-production,
[B7](02-blocages-et-decisions.md#b7)) et la release finale se fait par **re-tag** de la RC
validée ([B17](02-blocages-et-decisions.md#b17)).*

- Cycle de vie des releases et table de transitions, invariant de version
  ([B17](02-blocages-et-decisions.md#b17)).
- Création de tag et de release Gitea via l'API — c'est le segment aujourd'hui manuel.
- **Release finale par re-tag** : `promote-image` avec `old-tag` = la RC validée,
  `image_version` et `source_release_id` renseignés, héritage des résultats de tests de
  la RC source, avertissement si ses tests ne sont pas au vert.
- Enchaînement vers `build-image`, avec le tag SemVer passé en paramètre et
  `version.txt` baké (prérequis [P10](10-prerequis-mse-tekton.md)).
- Inventaire des images et digests via le registre.
- **Promotion** : construction du tag suffixé depuis `promotion_suffix` et le gabarit
  configurable, déclenchement de `promote-image`, puis suivi de la chaîne Flux →
  rollout → migrations. Le dashboard demande et constate, il ne déploie pas
  ([B7](02-blocages-et-decisions.md#b7)).
- Vérification au démarrage : `promotion_suffix` et `accepts_prerelease` confrontés aux
  `filterTags` et aux plages SemVer des `ImagePolicy` du cluster. Divergence = démarrage
  refusé, plutôt que des promotions silencieusement sans effet.
- Refus en amont d'une promotion de pré-release vers un environnement qui n'en accepte
  pas, avec un message explicite — Flux se contenterait d'ignorer le tag.
- Lecture du CRD `PortalData` et des `Kustomization` Flux, recoupement avec les digests,
  résolution de la version par environnement. Rattachement d'un `PortalData` sur le tag
  **sans** métadonnée — une comparaison avec le tag promu échouerait systématiquement
  ([B17](02-blocages-et-decisions.md#b17)).
- Suivi des commits de `mse-kustom` : tag appliqué par environnement, daté et attribué —
  seule source lisible sans accès au cluster, et donc la couverture principale de la
  production. Croisement avec `PortalData` pour détecter un déploiement bloqué.
- Simulateur Flux (`hack/fluxsim`) permettant de provoquer chaque étape et chaque échec
  de la chaîne de déploiement ([11 §11.3](11-environnement-de-developpement.md)). Sans
  lui, la machine à états de `deployments` n'est testable que par des déploiements réels,
  donc jamais dans ses cas d'échec.
- Traitement explicite de la production partiellement observable : états limités à
  `tagged`/`reconciling`, avec mention explicite que l'aboutissement n'est pas
  constatable.
- Écran `/environments` alimenté par des données réelles.
- Génération de la note de release (demandes Redmine + PR Gitea via `#1234`), édition
  manuelle.

*Dépendance externe : la PR `PortalData` sur `mse-kustom` doit être fusionnée. Si elle
tarde, le lot se replie sur la résolution digest → tag, qui donne la version mais pas le
statut des migrations.*

**Livrable :** processus de release complet, de la création au déploiement.

### Lot 6 — Vue d'ensemble d'une version

- Écran `/releases/:tag` : agrégat complet.
- Actions manuelles : champ texte Markdown éditable, tracé (auteur, date).
- Anomalies ouvertes rattachées à une version.
- Historique par environnement, portage complet de l'écran de la maquette sur données
  réelles.

**Livrable :** la maquette, alimentée par le système réel.

### Lot 7 — Exports

- Rapport de tests (PDF) : synthèse, résultats par famille, échecs détaillés.
- Rapport de sécurité (PDF/JSON) : findings, verdict de politique, écarts.
- Bon de livraison (PDF) : version, périmètre livré, images et digests, résultats de
  tests, actions manuelles, anomalies connues, mentions de date et de signataire.
- Génération asynchrone (job), stockage MinIO, table `artifacts`, téléchargement.

**Livrable :** documents de livraison téléchargeables.

*Le format exact du bon de livraison doit être validé avec le client avant
développement : c'est un document contractuel, et sa mise en page n'est pas une décision
technique.*

### Lot 8 — Tests de charge

*Outil tranché : **k6**. Prérequis restant : où vit le runner hors cluster,
comment est-il joignable et authentifié. Ce lot comprend la création d'un pipeline
Tekton, à réaliser dans `mse-tekton`, et une décision d'infrastructure hors des deux
dépôts.*

- Pipeline `load-test` : Task fine pilotant le **runner k6 hors cluster** — appel, suivi
  d'exécution, récupération de la sortie. Le runner publie ses résultats directement sur
  MinIO via URL pré-signée, sans credential distribué hors du cluster
  ([B16](02-blocages-et-decisions.md#b16)).
- Barrière `exclusive-cluster` dans l'ordonnanceur : drainage, attente, exécution seule,
  avec motif `draining` visible dans l'interface.
- Étiquetage des conditions de mesure (`exclusive`, jobs concurrents constatés) et
  exclusion des mesures non exclusives de la comparaison à la baseline.
- Parseur de résultats k6, modèle de baseline, historique par scénario.
- `BenchPanel`, `SignalDetail` et `Sparkline` alimentés — la maquette prévoit déjà
  l'affichage, y compris la comparaison à une baseline et le verdict sur le volume
  total.

**Livrable :** signal de performance dans le score de qualité.

*Ce lot est isolé volontairement : il dépend d'une décision d'infrastructure encore à
l'étude côté client et n'empêche aucun autre lot d'avancer. La barrière
`exclusive-cluster` est en revanche à prévoir dès le lot 3 dans le modèle
d'ordonnancement, sous peine de devoir y revenir.*

### Lot 10 — Prérequis `mse-tekton`

*À démarrer en parallèle du lot 0.* Contenu détaillé en
[10 — Prérequis](10-prerequis-mse-tekton.md).

**Livrable :** artefacts de tests adressables par run et lisibles par une machine.

## 9.3 Dépendances

```
Lot 0 ──┬─▶ Lot 1 ──────────────────────┐
        │                               ├─▶ Lot 6 ──▶ Lot 7
        ├─▶ Lot 2 ──▶ Lot 3 ──┬─▶ Lot 5 ┘
        │                     │
Lot 10 ─┴─────────────────────┴─▶ Lot 4 ──▶ (score) ──▶ Lot 6
                                     ▲
                              Lot 8 ─┘  (indépendant, greffé quand prêt)
```

Chemin critique : **0 → 2 → 3 → 5 → 6 → 7**. Le lot 1 et le lot 10 sont parallélisables
dès le départ ; le lot 8 est greffable à tout moment après le lot 4.

## 9.4 Jalons de validation

| Jalon | Condition de sortie |
|---|---|
| J1 — après lots 0 et 1 | un utilisateur du client consulte les versions et leur avancement, s'authentifie avec son compte |
| J2 — après lots 2 et 3 | un opérateur lance une campagne de tests depuis l'interface et suit ses logs en direct ; deux campagnes E2E simultanées sont impossibles |
| J3 — après lots 4 et 5 | une release est créée, testée, son score calculé, déployée sur un environnement de test, et l'interface affiche la version réellement en place |
| J4 — après lots 6 et 7 | une livraison complète est déroulée de bout en bout, bon de livraison téléchargé |

Chaque jalon est validé sur données réelles, avec un utilisateur du client. Valider J1
en interne n'apprend rien sur la justesse de la projection des statuts Redmine — c'est
précisément le genre d'écart qui coûte cher s'il est découvert au jalon 4.

## 9.5 Risques de planification

| Risque | Effet | Réduction |
|---|---|---|
| Prérequis `mse-tekton` non livrés à temps | lot 4 bloqué, score indisponible, lot 6 dégradé | démarrer le lot 10 à J0 ; à défaut, dégrader vers le scraping de texte en acceptant sa fragilité |
| ~~SigV4 non supporté par `nuo-s3.nuonet.fr`~~ | — | **écarté** : vérifié le 13/08/2026, `PUT` pré-signé accepté depuis un poste et depuis un pod |
| Réponse tardive sur `mse-kustom` / GitOps | lot 5 bloqué, c'est le chemin critique | poser la question dès le cadrage ; le modèle de données supporte les deux mécanismes, ce qui permet de commencer le lot 5 sans la réponse |
| Groupes absents du jeton OIDC | autorisation non fonctionnelle | repli par table de correspondance prévu dès le lot 0 |
| Registre non interrogeable par digest | version déployée non résolue | l'interface affiche le digest et un état « non résolue » ; dégradation acceptable, pas un blocage |
| Format du bon de livraison contesté en fin de projet | reprise du lot 7 | faire valider une maquette du document pendant le lot 5, pas au lot 7 |
| Volume de logs sous-estimé | coût de stockage, lenteur de l'interface | mesurer sur les runs réels dès le lot 2, avant d'écrire la visionneuse |

## 9.5 Avancement — *14/08/2026*

| Lot | État | Ce qui est vérifié sur kind |
|---|---|---|
| 0 — Fondations | **fait**, sauf overlay de production | déploiement, OIDC, RBAC, CI, CRD Flux, coquille React |
| 2 — Observation Tekton | **fait** | 28/28 PipelineRun projetés, journaux captés et scellés, SSE, écrans `/runs` |
| 3 — File et lancement | **fait** | catalogue validé contre les Pipeline réels, exclusion et barrière, lancement et annulation de bout en bout |

Le chemin critique passe au **lot 5** (promotion et environnements), qui reste
suspendu à deux relevés sur le cluster de production : les `filterTags` réellement
configurés et la version d'API du groupe `image.toolkit.fluxcd.io`
([11 §11.3](11-environnement-de-developpement.md)).

Reste hors chemin critique : le lot 1 (Redmine), parallélisable dès qu'un accès à
une instance de recette est disponible, et la fin du lot 10 côté `mse-tekton`.
