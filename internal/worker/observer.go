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
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/images"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
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
	// markers lit les descripteurs d'exécution laissés dans les journaux. Nil
	// quand le lecteur de journaux n'est pas branché : le reste continue, et un
	// outil planté redevient alors indiscernable d'un outil propre.
	markers *MarkerIngester
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
	// pipelines dit si une famille du catalogue s'applique à un projet. Nil : tout
	// s'applique, ce qui est le comportement d'avant la table `project_pipelines`.
	//
	// Extrait en interface pour la même raison que `verification` : décider quoi
	// enchaîner est de la logique, et elle s'éprouve sans base.
	pipelines ProjectPipelines
	// releases porte l'avancement d'état. Extrait comme `verification` : c'est
	// l'écriture conditionnelle qui rend l'enchaînement idempotent, et elle
	// mérite d'être éprouvée sans base.
	releases ReleaseAdvancer
	// instance identifie la base. Un run qui porte celle d'une autre n'est pas
	// rattaché à un job : les identifiants de jobs repartent à 1 sur une base
	// neuve, et un run resté dans le cluster conclurait son homonyme.
	instance string
	// events enregistre les faits constatés. Nil : l'observation continue et le
	// fil reste vide — personne n'y réagit encore (doc 14).
	events EventRecorder
	// engine fait avancer les exécutions de plan. Nil : rien ne s'enchaîne, et le
	// journal le dit — un enchaînement muet serait pire qu'une release en attente.
	engine *Engine
	// dispatcher déclenche les réactions déclarées d'un fait neuf. Nil : les faits
	// sont constatés et personne n'y réagit.
	dispatcher *Dispatcher
	// now date les faits. Remplaçable pour que l'émission s'éprouve sans horloge :
	// un fait porte l'instant où il s'est produit, et c'est une valeur qu'un test
	// doit pouvoir fixer.
	now    func() time.Time
	logger *slog.Logger
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
	return &runSink{
		db: db, verification: db, releases: db, pipelines: db,
		now: time.Now, logger: slog.Default(),
	}
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

// WithMarkerIngester branche la lecture des descripteurs d'exécution.
//
// Optionnel comme les autres ingestions : sans lui, l'observation continue, mais
// un outil qui a planté redevient indiscernable d'un outil qui n'a rien trouvé.
func (s *runSink) WithMarkerIngester(ingester *MarkerIngester) *runSink {
	s.markers = ingester
	return s
}

// WithSecurityIngester branche l'ingestion des audits de sécurité.
func (s *runSink) WithSecurityIngester(ingester *SecurityIngester) *runSink {
	s.security = ingester
	return s
}

func (s *runSink) SaveRun(ctx context.Context, run tekton.Run) error {
	run = s.disown(run)

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
	// Les marqueurs **avant** les artefacts, et l'ordre porte une décision : les
	// deux ingestions sont idempotentes par outil, donc la première l'emporte. Le
	// descripteur doit gagner — il est le seul à savoir que l'outil a échoué, là
	// où un artefact partiel se lirait comme un résultat complet.
	if err := s.ingestMarkers(ctx, id, run, release); err != nil {
		return err
	}
	if err := s.ingestTests(ctx, id, run, release); err != nil {
		return err
	}
	if err := s.ingestSecurity(ctx, id, run, release); err != nil {
		return err
	}

	// Le contrôle du contrat vient **après** les trois ingestions : c'est leur
	// résultat cumulé qu'il constate, et le faire avant reviendrait à conclure au
	// silence sur des rapports pas encore lus.
	s.checkResultsContract(ctx, id, run)

	// Le fait générique, émis en dernier : il annonce un run **terminé et
	// dépouillé**, et l'émettre avant les ingestions ferait réagir une future
	// réaction sur des résultats pas encore lus.
	s.emitPipelineFinished(ctx, id, run, release)

	// La conclusion du job vient **après** l'enregistrement du run, et l'ordre
	// porte une décision.
	//
	// Elle le précédait, sans raison écrite. Depuis que la conclusion d'un geste
	// relève les artefacts du run — ses résultats Tekton —, cet ordre les rendait
	// introuvables : le relevé relisait une ligne que l'enregistrement n'avait pas
	// encore écrite. Constaté sur le cluster, trois constructions réussies et
	// aucun artefact retenu.
	//
	// La règle générale est celle des ingestions ci-dessus : **la conséquence suit
	// le constat**. Conclure d'abord faisait réagir sur ce qui n'était pas encore
	// écrit.
	return s.finishJob(ctx, run)
}

// checkResultsContract signale un pipeline qui annonce des résultats et n'en
// laisse aucun.
//
// C'est le mode de défaillance que doc 12 §12.6 nommait sans pouvoir y remédier :
// « un pipeline qui ne l'honore pas produit un écran vide, et rien ne dira
// pourquoi ». Le catalogue déclarant désormais ce que chaque pipeline produit,
// l'écart entre l'annonce et le constat devient nommable.
//
// **Un signalement, pas un échec.** Le run a tourné et son statut est vrai ; c'est
// la remontée qui manque. Le faire échouer rendrait la release inexploitable pour
// un défaut de publication, alors que la bonne réaction est de corriger le
// pipeline — ou sa déclaration.
//
// Son affichage viendra avec les faits : `pipeline.finished` portera ce constat,
// et l'écran d'un run pourra le dire au lieu de rester muet (doc 14). D'ici là, il
// vit dans le journal, où il désigne au moins la cause.
func (s *runSink) checkResultsContract(ctx context.Context, runID int64, run tekton.Run) {
	if s.catalog == nil || run.JobID == 0 || !run.Status.Terminal() {
		return
	}
	// Un run annulé n'a rien promis : il n'a pas eu le temps de publier, et le
	// signaler enverrait chercher un défaut de pipeline là où quelqu'un a
	// simplement interrompu.
	if run.Status != tekton.StatusSucceeded {
		return
	}

	job, err := s.db.GetJob(ctx, run.JobID)
	if err != nil {
		s.logger.Error("famille d'un run terminé", "run", run.Name, "job", run.JobID, "error", err)
		return
	}
	if !s.catalog.Yields(job.Kind, catalog.ProductResults) {
		return
	}

	found, err := s.db.RunHasResults(ctx, runID)
	if err != nil {
		s.logger.Error("résultats d'un run terminé", "run", run.Name, "error", err)
		return
	}
	if found {
		return
	}

	s.logger.Warn("contrat de pipeline non honoré",
		"run", run.Name, "kind", job.Kind,
		"attendu", "des résultats de tests, annoncés par `produces: [results]`",
		"constate", "aucun résultat enregistré pour ce run",
		"consequence", "l'écran de cette release restera vide sur cette famille",
		"piste", "artefacts non publiés, chemins hors du contrat doc 13 §13.4, "+
			"ou déclaration `produces` à corriger")
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

// ingestMarkers enregistre ce que les descripteurs disent des outils qui n'ont
// pas conclu.
//
// L'échec n'est pas rendu à l'observateur, comme pour les autres ingestions : il
// ferait retenter la projection du run entier alors que le run est correctement
// enregistré, et la prochaine visite rejouera ce qui manque.
//
// La famille vient du catalogue et non du run : un outil qui n'a rien publié n'a
// pas d'artefact d'où la déduire, et c'est précisément le cas qu'on cherche à
// rendre visible.
func (s *runSink) ingestMarkers(ctx context.Context, runID int64, run tekton.Run, release *int64) error {
	if s.markers == nil || !run.Status.Terminal() || run.JobID == 0 {
		return nil
	}

	job, err := s.db.GetJob(ctx, run.JobID)
	if err != nil || job.Kind == "" {
		return nil
	}

	tasks := make([]string, 0, len(run.DeclaredTasks))
	for _, task := range run.DeclaredTasks {
		tasks = append(tasks, task.Name)
	}

	marked, ok := markerRunFor(s.catalog, job.Kind, MarkerRun{
		ID: runID, Name: run.Name, Tasks: tasks, ReleaseID: release,
	})
	if !ok {
		return nil
	}

	if err := s.markers.Ingest(ctx, marked); err != nil {
		s.logger.Error("lecture des marqueurs", "run", run.Name, "error", err)
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
	// Le geste que ce job mettait en œuvre, s'il venait d'un plan. Avant la
	// conclusion d'état : c'est l'avancement du plan qui fera partir l'étape
	// suivante, et le retarder ferait attendre un tour à la chaîne.
	//
	// Sans effet pour la plupart des jobs, qui ne viennent d'aucun plan.
	if s.engine != nil {
		s.engine.Advance(ctx, jobID, planStateOf(jobState))
	}

	// Construire et vérifier ne passent plus par ici.
	//
	// Les deux sont devenues des **étapes du plan** de la release, et c'est le
	// règlement de l'étape qui avance l'état (doc 15 §15.3). Ce que cela retire
	// vaut d'être nommé : la construction ne se compte plus job par job — l'étape
	// *est* le décompte — et les vérifications ne se concluent plus par un
	// second décompte de familles, tenu ailleurs et qui devait rester d'accord
	// avec le premier.
	//
	// Reste ce qu'aucun plan ne cause : une promotion. Elle ne rend pas une
	// release « prête » — elle la déploie quelque part, et les tests qui suivront
	// ne concluent rien tant que personne ne les a lus. Elle constitue en
	// revanche un **fait**, et c'est toute la différence entre faire avancer un
	// état et constater qu'il s'est passé quelque chose.
	if linked.Kind != buildJobKind && !s.isVerificationKind(linked.Kind) {
		s.emitReleasePromoted(ctx, linked, jobState)
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

func (s *runSink) WithProjectPipelines(pipelines ProjectPipelines) *runSink {
	s.pipelines = pipelines
	return s
}

// pipelineApplies dit si une famille s'enchaîne pour ce projet.
//
// Une lecture en échec **laisse passer** : la construction a réussi, et priver
// une release de ses vérifications parce que la base a hoqueté une seconde serait
// pire que d'en lancer une que le projet aurait désactivée. Le journal le dit.
func (s *runSink) pipelineApplies(ctx context.Context, projectID int64, kind string) bool {
	if s.pipelines == nil || projectID == 0 {
		return true
	}

	enabled, err := s.pipelines.PipelineEnabled(ctx, projectID, kind)
	if err != nil {
		s.logger.Error("familles déclarées du projet",
			"projet", projectID, "kind", kind, "error", err)
		return true
	}
	return enabled
}

func (s *runSink) isVerificationKind(kind string) bool {
	return s.catalog != nil && s.catalog.IsVerification(kind)
}
