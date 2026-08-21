# 08 — Frontend

## 8.1 Portage de la maquette

La maquette est une application Vue 3 complète et cohérente ; la demande impose React.
Le portage est mécanique — détail de l'arbitrage en
[B9](02-blocages-et-decisions.md#b9).

| Élément Vue | Équivalent React |
|---|---|
| `vue-router` | React Router |
| store Pinia `dashboard` | TanStack Query + état d'URL |
| `computed` | `useMemo` / sélecteurs de requête |
| `defineAsyncComponent` + import dynamique | `React.lazy` + `Suspense` |
| classes Tailwind | identiques, sans modification |
| `Icon` (map nom → chemins SVG) | même map, en composant React |
| `useStatus()` (tables de libellés) | même objet, en module TypeScript |

Ce qui se recopie tel quel, ce sont les tables de libellés (santé, pipeline, bench,
sign-off) et leurs classes de couleur associées, qui portent tout le vocabulaire
français de l'interface. Elles vivent dans `lib/status.ts`.

**Les seuils, en revanche, ne se recopient pas.** Ce document prévoyait initialement de
porter `healthFromScore` et le verdict de bench en TypeScript, la maquette les calculant
dans le navigateur faute de serveur. Le dashboard a un serveur, et il est le seul à
disposer de ce qu'il faut pour trancher : les conditions de mesure d'un bench
([B16](02-blocages-et-decisions.md#b16)) et les signaux non mesurés dont le poids est
redistribué ([B11](02-blocages-et-decisions.md#b11)). Les verdicts sont donc calculés là
et transmis dans les réponses :

| Verdict | Implémentation |
|---|---|
| Santé depuis le score (85 / 70) | `internal/results/score.go` |
| Verdict de bench, dégradation par le volume total | `internal/bench/compare.go` |

Une seconde implémentation côté navigateur a existé et n'a jamais été appelée. Elle a été
retirée : deux copies d'un même seuil divergent au premier changement, et celle qui reste
morte est celle qu'on oublie de mettre à jour.

**La grammaire des sévérités a divergé de la maquette**, pour une raison de fond. La
maquette parlait `high | moderate | low`, celle de `npm audit`. Les scanners réellement
branchés — Trivy, govulncheck — parlent `CRITICAL | HIGH | MEDIUM | LOW | INFO`. La table
de la maquette ne savait pas exprimer `CRITICAL`, soit la sévérité la plus grave. Le
vocabulaire de sécurité vit donc dans `features/releases/tests.ts`, au contact du format
qu'il décrit, et non dans `lib/status.ts`.

## 8.2 Ce que le portage doit changer

La maquette charge tout d'un bloc (`load()` unique, promesse simulée de 150 ms) et
suppose les données complètes et figées. Le dashboard réel a trois différences
structurantes, à intégrer dès le départ et non après coup :

1. **Les données arrivent séparément.** L'état d'un environnement, les résultats de
   tests d'une release et la file d'exécution ont des fréquences de rafraîchissement
   sans rapport. Une requête par ressource, avec ses propres clés de cache.
2. **Certaines données sont en flux.** Logs, progression d'un run, file d'attente
   arrivent en SSE. Ce ne sont pas des requêtes.
3. **Les données peuvent être périmées ou indisponibles.** Redmine peut ne pas répondre.
   Chaque bloc affiche l'âge de ses données quand il dépasse un seuil, et un état
   dégradé explicite plutôt qu'une valeur silencieusement fausse.

## 8.3 Arborescence des écrans

```
/                                   → redirection vers /environments
/environments                       → flux de promotion, vue d'ensemble
/environments/:env                  → vue détaillée d'un environnement (écran de la maquette)
/environments/:env/history          → historique des déploiements
/versions                           → liste des versions Redmine + complétion
/versions/:id                       → périmètre, demandes, anomalies, releases associées
/releases                           → liste des releases
/releases/:tag                      → agrégat complet d'une release
/releases/:tag/tests/:family        → détail des tests d'une famille
/releases/:tag/security             → findings, filtres sévérité/catégorie/statut
/releases/:tag/notes                → note de release, éditable
/releases/:tag/actions              → actions manuelles
/runs                               → tous les runs, filtrables
/runs/:name                         → détail d'un run, logs en direct
/queue                              → file d'exécution
/admin                              → configuration, synchronisations, journal d'audit
```

Les écrans `/environments/:env`, `/environments/:env/history` et le flux de promotion
existent déjà dans la maquette et sont portés à l'identique. Les autres sont à
concevoir dans la même grammaire visuelle.

## 8.4 Écrans nouveaux

### `/versions/:id` — périmètre d'une version

Absent de la maquette, c'est pourtant le point d'entrée du processus décrit par le
client (« identifier les versions créées sur Redmine », « afficher un résumé des
demandes »).

- En-tête : numéro, échéance, statut Redmine, jauge de complétion.
- Trois colonnes par bucket (à faire / en cours / terminé), demandes en cartes avec
  tracker, assignation, lien Redmine.
- Bandeau des anomalies ouvertes, compté à part — une version peut être complète et
  porter des anomalies, ce sont deux signaux distincts.
- Liste des releases produites pour cette version, avec leur état et leur score.
- Action `Créer une release` (pré-release / finale / patch), soumise à permission.

### `/runs/:name` — détail d'un run

- Graphe des tasks (ordre `runAfter`), statut et durée par étape.
- Sélecteur de task et d'étape, visionneuse de logs avec suivi automatique, recherche,
  ancrage sur une ligne (URL partageable).
- Bandeau d'état en direct via SSE, avec reprise sur `Last-Event-ID`.
- Résultats Tekton du `PipelineRun` quand ils existent.

La visionneuse de logs est le composant le plus exigeant de l'interface : elle doit
tenir des dizaines de milliers de lignes. Rendu virtualisé obligatoire, chargement de la
fin par défaut avec remontée à la demande.

### `/releases/:tag/tests/:family` — détail des tests

- Agrégats : total, réussis, échoués, ignorés, instables, durée.
- Table des cas de test, filtrable, triable, avec message d'échec et trace repliés.
- Comparaison avec la release précédente : nouveaux échecs, échecs résolus, tests
  ajoutés. C'est la vue qui répond à la question réellement posée en recette — « qu'est-ce
  qui a changé ? » — et non « combien de tests passent ».
- Téléchargement du rapport brut.

### `/runs` — exécutions, files et paramètres

Trois sous-onglets sous une coquille commune. Ils répondaient à la même question depuis
deux entrées de menu — ce qui tourne, ce qui attend, ce qui gouverne l'attente — et les
réglages de file n'avaient nulle part où vivre.

**Exécutions** : l'historique des runs, inchangé.

**File d'attente** : une carte par file, y compris vides. Ne montrer que les files
occupées ferait disparaître la moitié du tableau au repos, et c'est justement quand rien
ne tourne qu'on vient vérifier ce qu'une file autorise.

- Occupation avant plafond — « 1 / 5 en parallèle » —, dans cet ordre : c'est le rapport
  des deux qui dit s'il reste de la place, et le lire ainsi évite de soustraire.
- Jobs en attente dans l'ordre réel de traitement, avec la raison : « la file Tests E2E
  est pleine — 1 en parallèle au maximum », ou « le plafond global est atteint, toutes
  files confondues ». Une file qui n'explique pas pourquoi elle attend engendre
  systématiquement des relances inutiles.
- Annulation, remise en file, changement de priorité selon permission.

**Paramètres** : le plafond de chaque file, modifiable à chaud par l'administration. Le
défaut du catalogue reste affiché à côté et le retour à cette valeur reste possible — un
réglage qu'on ne peut pas défaire est un réglage qu'on n'ose pas toucher.

### `/releases/:tag` — agrégat

C'est l'écran `/environments/:env` de la maquette, mais indexé par release plutôt que
par environnement, augmenté de :

- la liste des images et de leurs digests ;
- les actions manuelles : **un champ texte libre en Markdown**, éditable en place,
  renseigné au fil de la production du livrable, avec la date et l'auteur de la dernière
  modification ;
- les boutons d'export (rapport de tests, bon de livraison) ;
- les boutons de déploiement par environnement, soumis à permission ;
- le `SignoffPanel`, qui réunit les deux moitiés du sign-off décrites en
  [doc 01 §1.2](01-analyse-existant.md) : les contrôles automatiques — résumés depuis
  les résultats de tests, le détail vivant déjà dans le panneau des tests — et la
  validation humaine, saisissable avec `release:edit`.

  L'ordre n'est pas indifférent : l'automatique d'abord, parce qu'un client qui valide
  le fait au vu des tests. Sur `/environments/:env`, le panneau est restreint à cet
  environnement — une release validée en qualif ne l'est pas en préproduction.

## 8.5 Conventions

**États de chargement.** Squelettes plutôt que spinners, aux dimensions du contenu
attendu, pour éviter les sauts de mise en page. Chaque bloc charge indépendamment : la
note de release s'affiche sans attendre les résultats de tests.

**États vides.** Formulés en français, en indiquant l'action possible. La maquette donne
le ton (« Aucun historique pour cet environnement. », « Aucune issue connue. 🎉 ») ; il
est conservé.

**États d'erreur.** Distinguer trois cas, qui n'appellent pas la même réaction : source
indisponible (afficher le cache et son âge), données absentes (pipeline jamais exécuté),
échec réel (message et action de réessai).

**Actions destructives ou sortantes.** Toute action déclenchant une exécution ou un
déploiement passe par une confirmation rappelant explicitement l'environnement, la
version et l'action. Le déploiement en production exige de ressaisir le numéro de
version — le coût de la friction est négligeable devant celui d'une erreur.

**Accessibilité.** La maquette est déjà correcte (`aria-label`, `aria-pressed`,
`aria-checked`, `role="switch"`, `role="group"`, icônes en `aria-hidden`). Le niveau est
maintenu : navigation au clavier complète, statut jamais porté par la seule couleur —
chaque statut a une icône dédiée, ce que la maquette fait déjà.

**Thème sombre.** Repris tel quel, y compris le script anti-flash en `<head>` qui
applique le thème avant le premier rendu.

## 8.6 Performance

| Contrainte | Mesure |
|---|---|
| Logs volumineux | rendu virtualisé, chargement par plage d'octets |
| Tables de cas de test | pagination serveur au-delà de 500 lignes |
| SSE | un flux par écran au maximum, fermé au démontage |
| Rafraîchissement | `refetchInterval` uniquement sur les données réellement changeantes (file, run en cours), jamais sur les données historiques |
| Bundle | découpage par route, `React.lazy` sur les écrans lourds (visionneuse de logs, détail de tests) |
