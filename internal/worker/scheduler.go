package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
)

// tickInterval borne l'attente entre deux tours de boucle.
//
// La doc prévoit un réveil par `LISTEN/NOTIFY` avec un tick de sécurité. Seul le
// tick est implémenté pour l'instant : deux secondes de latence au lancement sont
// sans conséquence pour un pipeline qui dure des minutes, alors qu'une notification
// perdue laisserait la file immobile jusqu'au tick suivant de toute façon. La
// notification reste un raffinement, pas un prérequis.
const tickInterval = 2 * time.Second

// Queue est ce que l'ordonnanceur attend de la base.
type Queue interface {
	ClaimNext(ctx context.Context, maxConcurrent int, runName func(kind string, id int64) string) (store.Job, error)
	Requeue(ctx context.Context, id int64, reason string) error
	ExplainQueue(ctx context.Context, maxConcurrent int) error
	PendingCancellations(ctx context.Context) ([]store.RunningJob, error)
}

// Scheduler transforme les jobs en file en PipelineRun.
type Scheduler struct {
	queue   Queue
	cluster *tekton.Client
	catalog *catalog.Catalog
	config  map[string]string
	logger  *slog.Logger
}

// NewSchedulerFromCatalog assemble l'ordonnanceur. `config` fournit les valeurs
// des paramètres de source `config`.
func NewSchedulerFromCatalog(
	queue Queue, cluster *tekton.Client, pipelines *catalog.Catalog,
	config map[string]string, logger *slog.Logger,
) *Scheduler {
	return &Scheduler{queue: queue, cluster: cluster, catalog: pipelines, config: config, logger: logger}
}

// Run fait tourner la boucle jusqu'à annulation.
//
// Une seule instance doit l'exécuter : c'est ce que garantit le bail. Le
// `skip locked` de la réservation couvre le reste — un double lancement de
// déploiement en production n'est pas rattrapable.
func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Info("ordonnanceur démarré",
		"plafond", s.catalog.MaxConcurrentRuns, "kinds", s.catalog.Kinds())

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		// Chaque tour vide la file autant que les règles le permettent : servir
		// un seul job par tick ferait démarrer quatre jobs éligibles en huit
		// secondes, sans raison.
		for s.dispatchOne(ctx) {
			if ctx.Err() != nil {
				return nil
			}
		}

		s.applyCancellations(ctx)

		if err := s.queue.ExplainQueue(ctx, s.catalog.MaxConcurrentRuns); err != nil {
			s.logger.Error("explication de la file", "error", err)
		}

		select {
		case <-ctx.Done():
			s.logger.Info("arrêt de l'ordonnanceur")
			return nil
		case <-ticker.C:
		}
	}
}

// dispatchOne réserve et lance un job. Rend false quand il n'y a plus rien à
// faire pour ce tour.
func (s *Scheduler) dispatchOne(ctx context.Context) bool {
	job, err := s.queue.ClaimNext(ctx, s.catalog.MaxConcurrentRuns, RunName)
	if err != nil {
		if !errors.Is(err, store.ErrJobNotFound) {
			s.logger.Error("réservation d'un job", "error", err)
		}
		return false
	}

	pipeline, found := s.catalog.Find(job.Kind)
	if !found {
		// Le catalogue a changé entre la mise en file et le lancement. Remettre
		// le job en file le ferait boucler : mieux vaut le conclure en échec avec
		// un motif qui nomme la cause.
		s.logger.Error("kind absent du catalogue", "job", job.ID, "kind", job.Kind)
		if err := s.queue.Requeue(ctx, job.ID, fmt.Sprintf("kind %q absent du catalogue", job.Kind)); err != nil {
			s.logger.Error("remise en file", "job", job.ID, "error", err)
		}
		return false
	}

	params, err := s.resolveParams(pipeline, job.Params)
	if err != nil {
		s.logger.Error("paramètres du job", "job", job.ID, "error", err)
		if err := s.queue.Requeue(ctx, job.ID, err.Error()); err != nil {
			s.logger.Error("remise en file", "job", job.ID, "error", err)
		}
		return false
	}

	// La création vient **après** la transition validée en base. Si elle échoue,
	// le job repasse `queued` avec un essai de plus ; l'ordre inverse produirait
	// un run que le dashboard ne saurait pas rattacher.
	if _, err := s.cluster.Create(ctx, tekton.Request{
		Name:     job.RunName,
		JobID:    job.ID,
		Pipeline: pipeline,
		Params:   params,
	}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Le run existe déjà sous ce nom : une tentative précédente a abouti
			// dans le cluster sans que le worker l'apprenne — mort entre la
			// création et sa prise en compte. Le remettre en file en créerait un
			// second, ou boucherait la file indéfiniment. L'état visé est
			// atteint : on continue.
			s.logger.Warn("run déjà présent, tentative précédente aboutie",
				"job", job.ID, "run", job.RunName)
			return true
		}

		s.logger.Error("création du PipelineRun", "job", job.ID, "run", job.RunName, "error", err)
		if err := s.queue.Requeue(ctx, job.ID, err.Error()); err != nil {
			s.logger.Error("remise en file", "job", job.ID, "error", err)
		}
		return false
	}

	s.logger.Info("run lancé",
		"job", job.ID, "kind", job.Kind, "run", job.RunName, "classe", job.QueueClass)
	return true
}

// applyCancellations exécute les demandes d'arrêt enregistrées par l'api.
//
// Le patch Tekton est idempotent : le rejouer sur un run déjà annulé ne coûte
// qu'un appel. C'est ce qui permet de ne pas tenir d'état supplémentaire pour
// savoir si la demande a été transmise — l'informer finira par constater la fin,
// et la demande disparaîtra avec le job.
func (s *Scheduler) applyCancellations(ctx context.Context) {
	pending, err := s.queue.PendingCancellations(ctx)
	if err != nil {
		s.logger.Error("lecture des annulations demandées", "error", err)
		return
	}

	for _, job := range pending {
		if err := s.cluster.Cancel(ctx, job.RunName); err != nil {
			s.logger.Error("annulation dans le cluster",
				"job", job.ID, "run", job.RunName, "error", err)
			continue
		}
		s.logger.Info("annulation transmise au cluster", "job", job.ID, "run", job.RunName)
	}
}

// resolveParams compose les paramètres du run à partir du catalogue et de la
// demande.
//
// Les trois sources ne se mélangent pas : `input` vient de l'utilisateur,
// `config` de la configuration du dashboard, `literal` du catalogue. Laisser une
// demande fournir un paramètre de source `config` permettrait de pointer un
// pipeline vers un autre dépôt que celui prévu.
func (s *Scheduler) resolveParams(
	pipeline catalog.Pipeline, provided map[string]string,
) (map[string]string, error) {
	params := make(map[string]string, len(pipeline.Params))

	for name, declared := range pipeline.Params {
		switch declared.Source {
		case catalog.SourceLiteral:
			params[name] = declared.Value

		case catalog.SourceConfig:
			value, present := s.config[declared.Key]
			if !present {
				return nil, fmt.Errorf("paramètre %q: clé de configuration %q absente", name, declared.Key)
			}
			params[name] = value

		case catalog.SourceInput:
			value, present := provided[name]
			if !present || value == "" {
				if declared.Required {
					return nil, fmt.Errorf("paramètre %q requis et non fourni", name)
				}
				continue
			}
			params[name] = value
		}
	}

	return params, nil
}

// RunName compose le nom du PipelineRun d'un job.
//
// Il porte l'identifiant du job : le lien est lisible dans `kubectl` sans
// consulter la base, et deux essais du même job donnent le même nom. C'est ce qui
// rend la reprise sûre — une seconde création se heurte à un conflit, que
// l'ordonnanceur lit comme « c'était déjà fait » plutôt que de créer un doublon.
func RunName(kind string, id int64) string {
	return fmt.Sprintf("mse-%s-%d", kind, id)
}
