# Arkhitekton — Documentation de conception

Dashboard de pilotage des releases de l'application MSE. Il s'intercale entre Redmine
(gestion de projet), Gitea (code et revues), Tekton (pipelines CI/CD) et le registre
d'images, pour donner une vue unique de l'état d'une version et permettre de dérouler
le processus de release sans quitter l'interface.

## Sommaire

| Document | Contenu |
|---|---|
| [01 — Analyse de l'existant](01-analyse-existant.md) | Ce qui existe dans `mse-tekton`, ce que fait la maquette, ce qui manque |
| [02 — Blocages et décisions](02-blocages-et-decisions.md) | Points durs, arbitrages proposés, questions ouvertes pour le client |
| [03 — Architecture](03-architecture.md) | Découpage monorepo, services, déploiement Kubernetes |
| [04 — Modèle de données](04-modele-de-donnees.md) | Entités, schéma PostgreSQL, arborescence MinIO |
| [05 — API et intégrations](05-api-et-integrations.md) | API interne, intégration Redmine / Gitea / registre |
| [06 — Orchestration Tekton](06-orchestration-tekton.md) | File d'exécution, exclusion mutuelle, collecte des logs |
| [07 — Sécurité et permissions](07-securite-et-permissions.md) | OIDC Gitea, rôles, RBAC Kubernetes, secrets Vault |
| [08 — Frontend](08-frontend.md) | Portage React de la maquette, écrans, états |
| [09 — Roadmap](09-roadmap.md) | Découpage en lots, dépendances, jalons |
| [10 — Prérequis côté mse-tekton](10-prerequis-mse-tekton.md) | Modifications à apporter aux pipelines existants |
| [11 — Environnement de développement](11-environnement-de-developpement.md) | Cluster kind, simulation de Flux, registre de test, limites du local |
| [12 — Généralisation et plugins](12-generalisation-et-plugins.md) | Ce qui est propre au MSE, ce qui peut devenir configuration ou plugin, plan de découplage |
| [13 — Contrat des pipelines](13-contrat-des-pipelines.md) | Ce qu'un pipeline doit honorer pour que le dashboard sache l'exploiter |
| [14 — Déclencheurs et automatisations](14-declencheurs-et-automatisations.md) | Rendre déclarable ce qui s'enchaîne après un fait, et laisser les plugins déclarer leurs propres faits |
| [15 — Construire et tester par déclaration](15-construire-et-tester-par-declaration.md) | Sortir du code la décision de quoi construire et quoi tester, pour la poser dans l'onglet automatisation |
| [16 — Le plugin MSE](16-plugin-mse.md) | Rassembler environnements, Flux et `PortalData` dans un plugin, et garder une instance cohérente sans lui |

## Résumé exécutif

**Ce qui est déjà en place et directement réutilisable.** Le dépôt `mse-tekton` fournit
sept pipelines fonctionnels (tests statiques, tests d'intégration, E2E, build d'images,
promotion d'images, déploiement, audit de sécurité), une brique Go maison
(`tekton-mse-manager`) qui sait déjà déployer via kustomize et exécuter les tests
d'intégration dans un pod, et un ensemble de parseurs et de normalisation de findings
de sécurité (`pkg/security`) déjà écrit et testé — récupérable tel quel.

**Les trois blocages structurants.**

1. *Les résultats de tests ne sont pas adressables par run.* Sauf pour l'audit de
   sécurité, les artefacts sont écrasés à chaque exécution (voir
   [02](02-blocages-et-decisions.md#b1)). Sans correction côté `mse-tekton`, le
   dashboard ne peut pas afficher l'historique de qualité d'une version.
2. *Les tests n'émettent que du texte.* PHPUnit, PHPStan, php-cs-fixer et Cypress
   produisent des logs destinés à l'œil humain, pas des formats machine. La « vue
   enrichie » demandée suppose du JUnit XML / JSON en sortie.
3. *Il n'existe aucun pipeline de release ni de test de charge.* Le processus décrit
   (pré-release, release finale, patch, test de charge) n'a pas d'équivalent Tekton
   aujourd'hui. Une partie du travail est de la création de pipeline, pas seulement du
   pilotage.

**Le parti technique proposé.** Un binaire Go unique déployé en deux rôles (`api`
scalable horizontalement, `worker` en singleton avec élection de leader), PostgreSQL
comme base de vérité et file d'exécution, MinIO pour les logs et artefacts, un frontend
React servi statiquement. Le dashboard crée les `PipelineRun` directement via l'API
Kubernetes plutôt que par l'`EventListener`, ce qui est la seule façon de conserver la
corrélation entre une demande utilisateur et son exécution.

**Le partage des rôles avec les pipelines** ([B14](02-blocages-et-decisions.md#b14)) :
*le pipeline constate, le dashboard interprète.* Les Tasks exécutent l'outil, émettent
sa sortie native et un descripteur d'exécution, puis publient l'artefact via une **URL
pré-signée fournie par le dashboard** — donc sans credential MinIO et sans décider du
layout de stockage. Tout le reste — parsing, déduplication, exceptions, comparaison au
run précédent, score — se fait côté dashboard, dans PostgreSQL. C'est ce qui rend
l'historique rejouable : normaliser à l'écriture, comme le fait aujourd'hui
`sec-normalize`, fige le jeu de données et rend tout diff cross-run dépendant d'un
bucket sans transaction.

**Le dashboard demande, il ne déploie pas.** La chaîne réelle est
`retag-image` → registre → **FluxCD** → environnement
([B7](02-blocages-et-decisions.md#b7)). Le dashboard déclenche le re-tag et **constate**
la suite : rapprochement Flux, rollout, puis migrations rapportées par le CRD
`PortalData` ([B8](02-blocages-et-decisions.md#b8)). Il n'écrit jamais dans les clusters
d'environnement — Flux corrigerait la dérive au rapprochement suivant.

Tout cela repose sur un **invariant de version**
([B17](02-blocages-et-decisions.md#b17)) : une même chaîne SemVer dans la version
Redmine, le tag Git et le tag d'image, alignée sur la transition SemVer en cours. C'est
ce qui rend possible la jointure entre périmètre fonctionnel, artefact et exécution.

Une exception, assumée et enregistrée : la release finale **re-tague** l'image de la RC
validée, de sorte que ce qui part en production est bit pour bit ce qui a été validé en
pré-production. Le `version.txt` baké dans l'image reste donc celui de la RC. Le champ
`releases.image_version` porte cet écart explicitement, et les résultats de tests de la
RC sont hérités par la release finale — qui, n'étant pas construite, n'en a aucun à elle.

**Une limite, plus étroite qu'il n'y paraît.** La production tourne dans un cluster
indépendant : ni `PortalData`, ni `Deployment`, ni le statut Flux n'y sont lisibles.
Mais Flux réécrit dans `mse-kustom` le tag retenu pour chaque environnement, production
comprise — et `mse-kustom` est sur Gitea, donc lisible sans aucun accès à ce cluster. La
production affiche ainsi sa version et l'historique daté de ses déploiements ; seul
l'aboutissement — rollout terminé, migrations réussies — reste hors de portée.

**Volume.** Dix lots. Deux doivent démarrer en parallèle dès le début : le lot 0
(fondations) et le lot 10 (prérequis `mse-tekton`), ce dernier conditionnant entièrement
le lot 4.
