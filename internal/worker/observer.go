// Package worker porte les boucles de fond : observation Tekton, collecte de
// logs, ordonnanceur de file, synchronisations externes.
//
// Elles ont un point commun qui dicte l'architecture : elles doivent tourner en
// **un seul exemplaire**. Un ordonnanceur dupliqué lance deux fois le même
// pipeline, deux collecteurs écrivent sur le même objet MinIO. D'où l'élection de
// leader, et non un simple `replicas: 1` — pendant un rolling update, deux pods
// coexistent brièvement, et c'est exactement l'instant qui produirait le doublon.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/images"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
)

// runSink adapte le magasin PostgreSQL à ce que l'observateur attend.
//
// L'adaptateur existe pour que `store` ignore Tekton et que `tekton` ignore
// PostgreSQL. La traduction est mécanique, et elle se paie une fois ici plutôt
// qu'en dépendance croisée entre deux paquets qui n'ont pas à se connaître.
type runSink struct {
	db *store.DB
	// load ingère les campagnes de charge d'un run terminé. Nil quand le stockage
	// n'est pas disponible : le reste de l'observation continue sans lui.
	load *LoadIngester
	// tests ingère les rapports de tests. Même règle.
	tests *TestIngester
	// security ingère les audits normalisés. Même règle.
	security *SecurityIngester
	// registryProject préfixe les images publiées, pour les nommer à l'audit.
	// Vide, les familles qui les réclament ne sont pas lançables et le journal le
	// dit — mieux qu'un run qui échouerait sur une référence tronquée.
	registryProject string
	// catalog sert à enchaîner les vérifications d'une construction réussie. Nil
	// quand il n'est pas branché : l'observation continue, sans enchaînement.
	catalog *catalog.Catalog
	// verification met en file les tests enchaînés. Extrait en interface pour que
	// l'enchaînement s'éprouve sans base : c'est de la logique de décision — quoi
	// lancer, avec quelle révision — et elle mérite d'être testée seule.
	verification Enqueuer
	// releases porte l'avancement d'état. Extrait comme `verification` : c'est
	// l'écriture conditionnelle qui rend l'enchaînement idempotent, et elle
	// mérite d'être éprouvée sans base.
	releases ReleaseAdvancer
	// instance identifie la base. Un run qui porte celle d'une autre n'est pas
	// rattaché à un job : les identifiants de jobs repartent à 1 sur une base
	// neuve, et un run resté dans le cluster conclurait son homonyme.
	instance string
	logger   *slog.Logger
}

// WithInstance déclare l'identité de la base.
func (s *runSink) WithInstance(id string) *runSink {
	s.instance = id
	return s
}

// disown coupe le rattachement d'un run lancé par une autre base.
//
// Le label `job-id` seul ne suffit pas à identifier un job : les identifiants
// repartent à 1 sur une base neuve, alors que les PipelineRun survivent dans le
// cluster. Constaté — `mse-static-tests-7`, échoué la veille sous une base
// précédente, a conclu en échec le job 7 d'une base créée depuis, qui n'avait
// même pas encore été lancé. Le run reste projeté, il n'est simplement plus
// tenu pour le run de ce job.
func (s *runSink) disown(run tekton.Run) tekton.Run {
	if run.JobID == 0 || run.Instance == s.instance {
		return run
	}

	s.logger.Debug("run d'une autre base, non rattaché",
		"run", run.Name, "job", run.JobID,
		"instance_run", run.Instance, "instance", s.instance)
	run.JobID = 0
	run.Observed = true
	return run
}

// ReleaseAdvancer est ce que la conclusion attend de la base.
//
// `JobRelease` y figure parce que deux chemins en dépendent — la conclusion
// d'état et le rattachement des rapports ingérés — et que les éprouver sans base
// est ce qui a manqué : les rapports s'enregistraient orphelins de leur release
// sans qu'aucun test ne le voie.
type ReleaseAdvancer interface {
	AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error)
	JobRelease(ctx context.Context, jobID int64) (store.JobReleaseContext, error)
	// CountBuilds dit où en sont les constructions d'une release. Une release en
	// construit plusieurs — une par image — et n'avance que lorsque toutes ont
	// abouti.
	CountBuilds(ctx context.Context, releaseID int64, buildKind string) (store.BuildTally, error)
}

// Enqueuer est ce que l'enchaînement attend de la file.
type Enqueuer interface {
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
}

// WithCatalog branche l'enchaînement des vérifications.
func (s *runSink) WithCatalog(pipelines *catalog.Catalog, registryProject string) *runSink {
	s.catalog = pipelines
	s.registryProject = registryProject
	return s
}

// imageRef compose la référence d'une image publiée par la construction.
//
// Le tag de l'image est celui de la release : c'est ce que `build-image` reçoit
// en `version` et inscrit dans `version.txt` (B17, P10).
func (s *runSink) imageRef(name, tag string) string {
	return images.Reference(s.registryProject, name, tag)
}

// buildJobKind est la nature de job qui construit les images.
//
// Doit correspondre au `kind` de l'entrée du catalogue : c'est sur cette valeur
// que la conclusion d'un run décide de faire avancer une release.
const buildJobKind = "build"

// NewRunSink rend l'adaptateur consommé par l'observateur Tekton.
func NewRunSink(db *store.DB) *runSink {
	return &runSink{db: db, verification: db, releases: db, logger: slog.Default()}
}

// WithLoadIngester branche l'ingestion des campagnes de charge.
//
// Déclenchée par la fin du run et non par une notification du runner : celui-ci
// vit hors du cluster, publie sur MinIO avec une URL pré-signée et n'a aucun
// chemin de retour vers le dashboard (B16). Le seul signal fiable est celui que
// le dashboard possède déjà.
func (s *runSink) WithLoadIngester(ingester *LoadIngester) *runSink {
	s.load = ingester
	return s
}

// WithTestIngester branche l'ingestion des rapports de tests.
//
// Déclenchée par la fin du run, comme la charge : les artefacts sont publiés au
// fil de l'exécution mais ne sont complets qu'à la fin, et les lire plus tôt
// donnerait des rapports partiels que rien ne distinguerait de rapports
// tronqués.
func (s *runSink) WithTestIngester(ingester *TestIngester) *runSink {
	s.tests = ingester
	return s
}

// WithSecurityIngester branche l'ingestion des audits de sécurité.
func (s *runSink) WithSecurityIngester(ingester *SecurityIngester) *runSink {
	s.security = ingester
	return s
}

func (s *runSink) SaveRun(ctx context.Context, run tekton.Run) error {
	run = s.disown(run)

	if err := s.finishJob(ctx, run); err != nil {
		return err
	}

	id, err := s.db.UpsertRun(ctx, store.RunRecord{
		Name:        run.Name,
		Namespace:   run.Namespace,
		Pipeline:    run.Pipeline,
		Status:      string(run.Status),
		Reason:      run.Reason,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
		Results:     run.Results,
		JobID:       run.JobID,
		Observed:    run.Observed,
		// Traduction plutôt que passage direct : `store` ignore Tekton, et
		// `tekton` ignore PostgreSQL. C'est ce que cet adaptateur existe pour
		// tenir.
		DeclaredTasks: declaredTasks(run.DeclaredTasks),
	})
	if err != nil {
		return err
	}

	// La release est résolue une fois, ici, et non dans chacune des trois
	// ingestions : elles la portent toutes, et trois lectures pour un même run
	// diraient trois fois la même chose.
	release := s.releaseOf(ctx, run)

	if err := s.ingestLoad(ctx, id, run, release); err != nil {
		return err
	}
	if err := s.ingestTests(ctx, id, run, release); err != nil {
		return err
	}
	return s.ingestSecurity(ctx, id, run, release)
}

// releaseOf rend la release que ce run vérifie, s'il en vérifie une.
//
// Sans elle, les rapports étaient enregistrés **orphelins** : l'écran d'une
// release cherche ses résultats par `release_id`, et n'en trouvait aucun alors
// que la table en portait neuf. Le score restait donc « non évalué » sur une
// release dont les tests avaient tourné et réussi.
//
// Nil est un cas normal, pas une anomalie : un run déclenché par webhook ou par
// CronJob ne vérifie aucune release, et ses rapports restent consultables depuis
// le run lui-même.
func (s *runSink) releaseOf(ctx context.Context, run tekton.Run) *int64 {
	if run.JobID == 0 {
		return nil
	}

	linked, err := s.releases.JobRelease(ctx, run.JobID)
	if err != nil {
		if !errors.Is(err, store.ErrJobWithoutRelease) {
			s.logger.Error("release d'un run", "run", run.Name, "job", run.JobID, "error", err)
		}
		return nil
	}
	return &linked.ReleaseID
}

// declaredTasks traduit la déclaration d'un pipeline vers le modèle du magasin.
func declaredTasks(tasks []tekton.DeclaredTask) []store.DeclaredTask {
	if len(tasks) == 0 {
		return nil
	}
	translated := make([]store.DeclaredTask, 0, len(tasks))
	for _, task := range tasks {
		translated = append(translated, store.DeclaredTask{
			Name: task.Name, DisplayName: task.DisplayName, Finally: task.Finally,
		})
	}
	return translated
}

// ingestSecurity enregistre l'audit normalisé d'un run terminé.
//
// L'échec n'est pas rendu à l'observateur, pour la même raison que les deux
// autres ingestions : il ferait retenter la projection du run entier alors que
// le run est correctement enregistré.
func (s *runSink) ingestSecurity(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.security == nil || !run.Status.Terminal() {
		return nil
	}

	if err := s.security.Ingest(ctx, LoadRun{ID: runID, Name: run.Name, JobID: run.JobID, ReleaseID: release}); err != nil {
		s.security.logger.Error("ingestion de l'audit de sécurité", "run", run.Name, "error", err)
	}
	return nil
}

// ingestTests enregistre les rapports d'un run terminé.
//
// L'échec n'est pas rendu à l'observateur, pour la même raison que pour la
// charge : il ferait retenter la projection du run entier alors que le run est
// correctement enregistré. L'ingestion étant idempotente, la prochaine visite
// rejouera ce qui manque.
func (s *runSink) ingestTests(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.tests == nil || !run.Status.Terminal() {
		return nil
	}

	if err := s.tests.Ingest(ctx, LoadRun{ID: runID, Name: run.Name, JobID: run.JobID, ReleaseID: release}); err != nil {
		s.tests.logger.Error("ingestion des rapports de tests", "run", run.Name, "error", err)
	}
	return nil
}

// ingestLoad enregistre les campagnes de charge d'un run de charge terminé.
//
// L'échec de l'ingestion n'est pas rendu à l'observateur : il ferait retenter la
// projection du run entier, alors que le run est correctement enregistré. Un
// résumé illisible est un problème de résultat, pas d'observation, et la
// prochaine visite du run le rejouera de toute façon — l'ingestion est
// idempotente.
func (s *runSink) ingestLoad(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.load == nil || !run.Status.Terminal() || !isLoadRun(run) {
		return nil
	}

	var duration int64
	if run.StartedAt != nil && run.CompletedAt != nil {
		duration = run.CompletedAt.Sub(*run.StartedAt).Milliseconds()
	}

	err := s.load.Ingest(ctx, LoadRun{
		ID: runID, Name: run.Name, JobID: run.JobID, DurationMS: duration,
		ReleaseID: release,
	})
	if err != nil {
		s.load.logger.Error("ingestion des campagnes de charge", "run", run.Name, "error", err)
	}
	return nil
}

// isLoadRun reconnaît un run de charge à son pipeline.
//
// Sur le nom du pipeline et non sur celui du job : un run déclenché hors
// dashboard n'a pas de job, et ses résultats valent autant que les autres.
func isLoadRun(run tekton.Run) bool {
	return strings.Contains(run.Pipeline, "load-test")
}

// finishJob conclut le job d'un run terminé.
//
// La file n'apprend la fin d'un run que par l'observateur : Tekton ne prévient
// personne, et sans cette conclusion la classe d'exclusion resterait occupée
// indéfiniment — le job suivant n'obtiendrait jamais son tour.
//
// La conclusion précède l'enregistrement du run : si elle échouait après, la
// transaction suivante verrait un run terminé et un job en cours, état qu'aucune
// boucle ne rattrape.
func (s *runSink) finishJob(ctx context.Context, run tekton.Run) error {
	if run.JobID == 0 || !run.Status.Terminal() {
		return nil
	}

	state := store.JobFailed
	switch run.Status {
	case tekton.StatusSucceeded:
		state = store.JobSucceeded
	case tekton.StatusCancelled:
		state = store.JobCancelled
	}

	if err := s.db.FinishJob(ctx, run.JobID, state, run.Reason); err != nil {
		return err
	}
	return s.advanceReleaseForJob(ctx, run.JobID, state)
}

// advanceRelease fait suivre à une release la conclusion de son pipeline.
//
// Seule la construction déplace l'état : une promotion réussie ne rend pas une
// release « prête », elle la déploie quelque part, et les tests qui suivront ne
// concluent rien tant que personne ne les a lus. Élargir cette règle ferait
// avancer une release sur la foi d'un pipeline qui ne dit rien de sa qualité.
//
// L'état de départ est relu ici plutôt que supposé : entre la mise en file et la
// fin du run, une reprise manuelle ou une annulation a pu déplacer la release, et
// écrire sans condition effacerait cette décision. L'écriture conditionnelle rend
// `false` dans ce cas, ce qui est consigné et non traité comme une erreur — un
// pipeline arrivé après coup n'a pas le dernier mot.
func (s *runSink) advanceReleaseForJob(ctx context.Context, jobID int64, jobState string) error {
	linked, err := s.releases.JobRelease(ctx, jobID)
	if err != nil {
		if errors.Is(err, store.ErrJobWithoutRelease) {
			// Cas courant : un lancement manuel de tests depuis la file ne
			// désigne aucune release.
			return nil
		}
		return err
	}
	// Deux natures font avancer une release, et pas de la même façon.
	switch {
	case linked.Kind == buildJobKind:
		return s.advanceRelease(ctx, linked, jobState)
	case isVerificationKind(linked.Kind):
		return s.concludeVerification(ctx, linked)
	default:
		// Une promotion réussie ne rend pas une release « prête » : elle la
		// déploie quelque part, et les tests qui suivront ne concluent rien tant
		// que personne ne les a lus.
		return nil
	}
}

// advanceRelease fait suivre à une release la conclusion de sa construction.
//
// Séparé de la recherche en base pour s'éprouver seul : c'est ici que vit
// l'idempotence, et elle se vérifie en rappelant la fonction, pas en montant une
// base.
func (s *runSink) advanceRelease(
	ctx context.Context, linked store.JobReleaseContext, jobState string,
) error {
	from := release.State(linked.State)

	// **Le contrôle avant l'effet.** L'observateur redélivre les runs terminés —
	// resynchronisation de l'informer, redémarrage du worker — et cette fonction
	// est donc appelée plusieurs fois pour la même construction. Lancer les
	// vérifications avant de vérifier la transition les relançait à chaque
	// passage : constaté à cinq paires de tests pour une seule construction.
	//
	// Seule une release encore en `building` déclenche ses vérifications.
	//
	// Ce contrôle est **explicite**, et non déduit de la table des transitions.
	// Il l'a été, et c'était faux : la table autorise `failed -> verifying` pour
	// qu'un opérateur puisse reprendre une release après un échec, et une
	// redélivrance de la construction empruntait ce même chemin. Constaté en
	// production locale — l'audit d'une release échoue, la release passe en
	// `failed`, le worker redémarre, la conclusion de construction est
	// redélivrée, `failed -> verifying` est accepté, et les vérifications
	// repartent une seconde fois.
	if from != release.StateBuilding {
		s.logger.Info("conclusion de construction hors construction, ignorée",
			"release", linked.Tag, "etat", linked.State,
			"raison", "seule une release en construction est conclue par son build")
		return nil
	}

	// Une release construit plusieurs images, et le pipeline n'en traite qu'une
	// par exécution : cette fonction est donc appelée une fois par image.
	//
	// Un échec conclut tout de suite. Attendre les autres ferait tourner deux
	// constructions dont on sait déjà que le résultat ne servira pas, et
	// retarderait d'autant le moment où l'écran dit ce qui ne va pas.
	//
	// Une réussite, elle, ne conclut que la dernière. Le décompte est relu en base
	// plutôt que tenu en mémoire : c'est ce qui reste juste après un redémarrage
	// du worker, pendant lequel les runs continuent.
	target := release.StateFailed
	if jobState == store.JobSucceeded {
		tally, err := s.releases.CountBuilds(ctx, linked.ReleaseID, buildJobKind)
		if err != nil {
			return err
		}
		if !tally.Done() {
			s.logger.Info("construction partielle, release maintenue en construction",
				"release", linked.Tag, "restantes", tally.Pending,
				"reussies", tally.Succeeded, "echouees", tally.Failed)
			return nil
		}
		target = release.StateVerifying
	}
	if !release.CanTransition(from, target) {
		s.logger.Info("conclusion de construction sans effet sur la release",
			"release", linked.Tag, "etat", linked.State, "vise", string(target),
			"raison", "transition non autorisée depuis cet état")
		return nil
	}

	// L'écriture d'état **précède** le lancement des vérifications, et c'est ce
	// qui rend l'ensemble idempotent : elle est conditionnée à l'état lu, donc
	// une seule livraison la gagne. Lancer d'abord laisserait deux livraisons
	// simultanées enfiler deux jeux de tests avant qu'aucune n'ait écrit.
	advanced, err := s.releases.AdvanceReleaseState(ctx, linked.ReleaseID, linked.State, string(target))
	if err != nil {
		return err
	}
	if !advanced {
		s.logger.Info("release déplacée entre-temps, conclusion ignorée",
			"release", linked.Tag, "etat_lu", linked.State, "vise", string(target))
		return nil
	}

	s.logger.Info("release avancée par sa construction",
		"release", linked.Tag, "de", linked.State, "vers", string(target))

	if target != release.StateVerifying {
		return nil
	}

	// Si aucun test ne part — catalogue incomplet, file indisponible — la release
	// rejoint `ready` : mieux vaut une décision prise sans tests, et dite comme
	// telle, qu'une release bloquée en `verifying` que rien ne fera avancer.
	if launched := s.launchVerification(ctx, linked); launched == 0 {
		if _, err := s.releases.AdvanceReleaseState(ctx, linked.ReleaseID,
			string(release.StateVerifying), string(release.StateReady)); err != nil {
			return err
		}
		s.logger.Info("aucune vérification lancée, release en attente de décision",
			"release", linked.Tag)
	}
	return nil
}

func (s *runSink) SaveTaskRun(ctx context.Context, taskRun tekton.TaskRunProjection) error {
	return s.db.UpsertTaskRun(ctx, store.TaskRunRecord{
		RunName:     taskRun.RunName,
		Name:        taskRun.Name,
		DisplayName: taskRun.DisplayName,
		Status:      string(taskRun.Status),
		StartedAt:   taskRun.StartedAt,
		CompletedAt: taskRun.CompletedAt,
		PodName:     taskRun.PodName,
	})
}

// UnfinishedRunNames rend les runs que la base croit en cours, indexés par nom de
// PipelineRun — la clé que le cache de l'informer sait interroger.
func (s *runSink) UnfinishedRunNames(ctx context.Context, namespace string) (map[string]int64, error) {
	runs, err := s.db.UnfinishedRuns(ctx, namespace)
	if err != nil {
		return nil, err
	}

	names := make(map[string]int64, len(runs))
	for _, run := range runs {
		names[run.Name] = run.ID
	}
	return names, nil
}

func (s *runSink) ConcludeVanishedRun(ctx context.Context, id int64, reason string) error {
	return s.db.MarkRunVanished(ctx, id, reason)
}

// logAdapter traduit les notifications de l'observateur vers le collecteur.
//
// Même motif que runSink : ni `tekton` ni `logs` ne dépend de l'autre, et la
// traduction se paie une fois ici.
type logAdapter struct{ collector *logs.Collector }

// NewLogAdapter branche le collecteur sur l'observateur Tekton.
func NewLogAdapter(collector *logs.Collector) *logAdapter {
	return &logAdapter{collector: collector}
}

func (a *logAdapter) StartTaskLogs(
	ctx context.Context, runName, taskName, podName string, steps []tekton.Step,
) {
	a.collector.Start(ctx, logs.Target{
		RunName:  runName,
		TaskName: taskName,
		PodName:  podName,
		Steps:    toLogSteps(steps),
	})
}

func (a *logAdapter) FinishTaskLogs(
	ctx context.Context, runName, taskName string, steps []tekton.Step,
) error {
	return a.collector.Finish(ctx, logs.Target{
		RunName:  runName,
		TaskName: taskName,
		Steps:    toLogSteps(steps),
	})
}

// toLogSteps conserve l'ordre déclaré : c'est lui qui rétablit la chronologie au
// scellement, les étapes d'un TaskRun s'exécutant en série.
func toLogSteps(steps []tekton.Step) []logs.Step {
	converted := make([]logs.Step, 0, len(steps))
	for _, step := range steps {
		converted = append(converted, logs.Step{Name: step.Name, Container: step.Container})
	}
	return converted
}

// verificationKinds sont les familles lancées après une construction réussie.
//
// `integration-tests` n'y figure pas, et c'est un choix : il déploie
// l'application dans `mse-pipeline` et exige `mse-kustom-revision`, que le
// dashboard ne connaît pas — la révision du dépôt de déploiement est une donnée
// d'exploitation, pas une propriété de la release. Le lancer d'office ferait
// échouer toute vérification sur une cause étrangère au code testé.
// verificationKinds énumère ce qu'une construction réussie enchaîne.
//
// `e2e-tests` n'y figure pas : le pipeline vise un environnement déployé, et
// l'enchaîner sur une construction le ferait porter sur ce qui s'y trouve, pas
// sur la release qui vient d'être construite.
var verificationKinds = []string{
	"static-tests", "unit-tests", "security-audit", "integration-tests",
}

// launchVerification met en file les tests d'une release construite, et rend le
// nombre effectivement enfilé.
//
// Les échecs de mise en file sont consignés et non propagés : la construction a
// réussi, et perdre ce fait parce qu'un test n'a pas pu être enfilé serait pire
// que de le signaler. Le nombre rendu décide de l'état — zéro test enfilé ne doit
// pas laisser la release en attente d'une vérification qui ne viendra pas.
func (s *runSink) launchVerification(ctx context.Context, linked store.JobReleaseContext) int {
	// Les tests portent sur ce qui a été construit, donc sur le commit — jamais
	// sur la branche d'où il vient, qui a pu bouger depuis (B17).
	revision := linked.GitCommit
	if revision == "" {
		revision = linked.GitRef
	}
	if revision == "" {
		s.logger.Warn("vérification non lancée",
			"release", linked.Tag, "raison", "aucune référence Git sur la release")
		return 0
	}

	if s.catalog == nil {
		s.logger.Warn("vérification non lancée",
			"release", linked.Tag, "raison", "catalogue non branché sur l'observateur")
		return 0
	}

	launched := 0
	for _, kind := range verificationKinds {
		pipeline, found := s.catalog.Find(kind)
		if !found {
			s.logger.Warn("famille de vérification absente du catalogue",
				"release", linked.Tag, "kind", kind)
			continue
		}

		// Les valeurs sortent de ce que le catalogue déclare vouloir : `revision`,
		// `app-revision` et `mse-revision` désignent la même chose sous trois noms
		// hérités de pipelines écrits séparément, et c'est leur `suggest: git-ref`
		// commun qui le dit. Tous valent le commit construit — c'est lui qu'on
		// vérifie, pas la branche dont il vient.
		params, missing := pipeline.VerificationInputs(revision, linked.Tag, s.registryProject)
		if missing != "" {
			// Un paramètre requis qu'on ne sait pas renseigner ferait échouer le
			// run à l'ordonnancement : ne pas l'enfiler du tout est plus honnête,
			// et le journal dit lequel manque.
			s.logger.Warn("famille de vérification non lançable",
				"release", linked.Tag, "kind", kind, "parametre_manquant", missing)
			continue
		}

		id, err := s.verification.Enqueue(ctx, store.JobRequest{
			Kind:        pipeline.Kind,
			Params:      params,
			RequestedBy: verificationRequester,
			ReleaseID:   &linked.ReleaseID,
		})
		if err != nil {
			s.logger.Error("mise en file d'une vérification",
				"release", linked.Tag, "kind", kind, "error", err)
			continue
		}

		launched++
		s.logger.Info("vérification lancée",
			"release", linked.Tag, "kind", kind, "job", id, "revision", revision)
	}

	return launched
}

// verificationRequester est l'auteur inscrit sur les jobs enchaînés.
//
// Une valeur explicite plutôt que le sujet de celui qui a créé la release : ces
// runs ne sont demandés par personne, ils découlent d'une construction réussie,
// et les attribuer à un humain brouillerait le journal d'audit.
const verificationRequester = "dashboard:verification"

func isVerificationKind(kind string) bool {
	for _, known := range verificationKinds {
		if known == kind {
			return true
		}
	}
	return false
}

// concludeVerification fait sortir une release de `verifying` quand toutes ses
// vérifications ont rendu leur verdict.
//
// Le décompte est relu en base à chaque conclusion plutôt que retenu en mémoire :
// les runs continuent pendant qu'un worker redémarre, et un compteur en mémoire
// laisserait la release bloquée en `verifying` pour toujours — ce qui vient
// précisément d'arriver, faute de cette conclusion.
//
// Un échec de vérification mène à `failed` et non à `ready` : la décision humaine
// se prend sur des tests qui ont conclu, pas sur des tests qui ont planté. La
// release reste relançable, la table de transitions autorisant `failed →
// verifying`.
func (s *runSink) concludeVerification(ctx context.Context, linked store.JobReleaseContext) error {
	// `verifying` est le cas courant ; `failed` est celui d'une relance.
	//
	// Une release tombée en échec sur une vérification devait pouvoir s'en
	// relever : c'est tout l'objet du bouton « Rejouer ». Sans ce second état
	// admis, un essai réussi ne concluait rien et la release restait en échec
	// pour un verdict que le dernier essai avait remplacé.
	//
	// `ready` et `released` ne sont pas repris : la première a déjà été jugée
	// bonne, la seconde est livrée, et un verdict tardif ne doit pas défaire une
	// décision prise.
	switch release.State(linked.State) {
	case release.StateVerifying, release.StateFailed:
	default:
		return nil
	}

	tally, err := s.db.CountVerifications(ctx, linked.ReleaseID, verificationKinds, buildJobKind)
	if err != nil {
		return err
	}
	if !tally.Done() {
		return nil
	}

	target := release.StateReady
	if tally.Failed > 0 {
		target = release.StateFailed
	}

	// Rien de neuf : la release est déjà dans l'état visé. Écrire quand même
	// ferait un tour de journal à chaque redélivrance de l'informer.
	if release.State(linked.State) == target {
		return nil
	}

	advanced, err := s.releases.AdvanceReleaseState(ctx, linked.ReleaseID, linked.State, string(target))
	if err != nil {
		return err
	}
	if !advanced {
		s.logger.Info("release déplacée entre-temps, conclusion de vérification ignorée",
			"release", linked.Tag, "etat_lu", linked.State)
		return nil
	}

	s.logger.Info("vérification conclue",
		"release", linked.Tag, "vers", string(target),
		"reussies", tally.Succeeded, "echouees", tally.Failed)
	return nil
}
