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
	"strings"

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
}

// NewRunSink rend l'adaptateur consommé par l'observateur Tekton.
func NewRunSink(db *store.DB) *runSink { return &runSink{db: db} }

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

func (s *runSink) SaveRun(ctx context.Context, run tekton.Run) error {
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
	})
	if err != nil {
		return err
	}

	return s.ingestLoad(ctx, id, run)
}

// ingestLoad enregistre les campagnes de charge d'un run de charge terminé.
//
// L'échec de l'ingestion n'est pas rendu à l'observateur : il ferait retenter la
// projection du run entier, alors que le run est correctement enregistré. Un
// résumé illisible est un problème de résultat, pas d'observation, et la
// prochaine visite du run le rejouera de toute façon — l'ingestion est
// idempotente.
func (s *runSink) ingestLoad(ctx context.Context, runID int64, run tekton.Run) error {
	if s.load == nil || !run.Status.Terminal() || !isLoadRun(run) {
		return nil
	}

	var duration int64
	if run.StartedAt != nil && run.CompletedAt != nil {
		duration = run.CompletedAt.Sub(*run.StartedAt).Milliseconds()
	}

	err := s.load.Ingest(ctx, LoadRun{
		ID: runID, Name: run.Name, JobID: run.JobID, DurationMS: duration,
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

	return s.db.FinishJob(ctx, run.JobID, state, run.Reason)
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
