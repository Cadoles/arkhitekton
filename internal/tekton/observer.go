package tekton

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	informers "github.com/tektoncd/pipeline/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// Sink reçoit les projections. L'interface existe pour que l'observateur ne
// dépende pas de la base : elle rend le câblage testable avec un enregistreur en
// mémoire, sans PostgreSQL.
type Sink interface {
	SaveRun(ctx context.Context, run Run) error
	SaveTaskRun(ctx context.Context, taskRun TaskRunProjection) error
}

// Store décrit ce que l'observateur attend de la base pour réconcilier.
type Store interface {
	UnfinishedRunNames(ctx context.Context, namespace string) (map[string]int64, error)
	ConcludeVanishedRun(ctx context.Context, id int64, reason string) error
}

// ReasonVanished est le motif inscrit sur un run dont le PipelineRun a disparu.
//
// Distinct d'un échec ordinaire à dessein : « disparu » dit qu'il n'y a ni log ni
// résultat à chercher, alors que « échoué » envoie l'utilisateur en quête d'une
// trace qui n'existe pas. C'est aussi la trace d'un nettoyage — les CronJobs de
// `mse-tekton` suppriment les vieux PipelineRun.
const ReasonVanished = "PipelineRunDisparu"

// resyncPeriod force un rejeu périodique de tous les objets du cache.
//
// Ce n'est pas une redondance avec les watches : un watch peut être interrompu
// sans que le client s'en aperçoive, et le rejeu est le filet. Il est aussi la
// raison d'être de la garde sur les états terminaux — il livre les objets dans un
// ordre quelconque.
const resyncPeriod = 10 * time.Minute

// Observer projette les PipelineRun et TaskRun d'un namespace.
type Observer struct {
	client  *Client
	sink    Sink
	store   Store
	logger  *slog.Logger
	factory informers.SharedInformerFactory
}

// NewObserver assemble les informers.
func NewObserver(client *Client, sink Sink, store Store, logger *slog.Logger) *Observer {
	factory := informers.NewSharedInformerFactoryWithOptions(
		client.Tekton, resyncPeriod,
		// Borné au namespace : le compte de service n'a qu'une Role, et un
		// informer à l'échelle du cluster échouerait en boucle sur un
		// « forbidden » que rien ne rattacherait à la RBAC.
		informers.WithNamespace(client.Namespace),
	)

	return &Observer{client: client, sink: sink, store: store, logger: logger, factory: factory}
}

// Run réconcilie, puis suit les changements jusqu'à annulation du contexte.
func (o *Observer) Run(ctx context.Context) error {
	runs := o.factory.Tekton().V1().PipelineRuns().Informer()
	tasks := o.factory.Tekton().V1().TaskRuns().Informer()

	if _, err := runs.AddEventHandler(o.runHandler()); err != nil {
		return fmt.Errorf("abonnement aux PipelineRun: %w", err)
	}
	if _, err := tasks.AddEventHandler(o.taskHandler()); err != nil {
		return fmt.Errorf("abonnement aux TaskRun: %w", err)
	}

	o.factory.Start(ctx.Done())

	// L'attente de synchronisation est indispensable avant de réconcilier :
	// comparer la base à un cache encore vide conclurait que tous les runs ont
	// disparu, et les marquerait en échec en bloc.
	o.logger.Info("synchronisation du cache Tekton", "namespace", o.client.Namespace)
	if !cache.WaitForCacheSync(ctx.Done(), runs.HasSynced, tasks.HasSynced) {
		return fmt.Errorf("synchronisation du cache interrompue")
	}

	if err := o.reconcile(ctx); err != nil {
		// Échouer ici ne doit pas empêcher l'observation de démarrer : la
		// réconciliation rattrape le passé, les watches suivent le présent.
		o.logger.Error("réconciliation au démarrage", "error", err)
	}

	<-ctx.Done()
	o.logger.Info("arrêt de l'observation Tekton")
	return nil
}

func (o *Observer) runHandler() cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { o.projectRun(obj) },
		// L'ancien objet est ignoré : la projection est idempotente et la base
		// porte les gardes. Comparer les versions ici dupliquerait cette logique
		// à un endroit où elle ne protégerait pas des écritures concurrentes.
		UpdateFunc: func(_, obj any) { o.projectRun(obj) },
		// Pas de DeleteFunc. La suppression d'un PipelineRun ne dit rien de son
		// issue — les CronJobs de nettoyage en effacent des dizaines — et
		// conclure sur cet événement effacerait l'historique que le dashboard
		// existe pour conserver. Les runs restés en cours sont traités par la
		// réconciliation, qui sait distinguer « disparu » de « en cours ».
	}
}

func (o *Observer) taskHandler() cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { o.projectTaskRun(obj) },
		UpdateFunc: func(_, obj any) { o.projectTaskRun(obj) },
	}
}

func (o *Observer) projectRun(obj any) {
	pr, ok := obj.(*tektonv1.PipelineRun)
	if !ok {
		o.logger.Warn("objet inattendu sur le flux des PipelineRun", "type", fmt.Sprintf("%T", obj))
		return
	}

	// Contexte propre : celui de l'informer n'est pas transmis aux handlers, et
	// une écriture sans borne de temps bloquerait la file de l'informer.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	run := ProjectRun(pr)
	if err := o.sink.SaveRun(ctx, run); err != nil {
		o.logger.Error("projection d'un PipelineRun", "run", run.Name, "error", err)
	}
}

func (o *Observer) projectTaskRun(obj any) {
	tr, ok := obj.(*tektonv1.TaskRun)
	if !ok {
		o.logger.Warn("objet inattendu sur le flux des TaskRun", "type", fmt.Sprintf("%T", obj))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	projection := ProjectTaskRun(tr)
	if projection.RunName == "" {
		// Un TaskRun autonome, sans PipelineRun parent : hors périmètre du
		// dashboard, qui suit des pipelines.
		return
	}
	if err := o.sink.SaveTaskRun(ctx, projection); err != nil {
		o.logger.Error("projection d'un TaskRun", "taskrun", projection.Name, "error", err)
	}
}

// reconcile confronte ce que la base croit en cours à ce que le cluster détient.
//
// Sans cette étape, un redémarrage laisse des runs bloqués en `running` qui gèlent
// leur classe d'exclusion — le mode de panne le plus probable de tout le
// dispositif (doc 06 §6.4). Deux cas se présentent :
//
//   - le PipelineRun existe : les événements d'ajout du cache viennent de le
//     projeter, il n'y a rien à faire ;
//   - il a disparu : le run est conclu avec un motif qui dit sa cause.
func (o *Observer) reconcile(ctx context.Context) error {
	unfinished, err := o.store.UnfinishedRunNames(ctx, o.client.Namespace)
	if err != nil {
		return err
	}
	if len(unfinished) == 0 {
		o.logger.Info("réconciliation: aucun run en cours en base")
		return nil
	}

	lister := o.factory.Tekton().V1().PipelineRuns().Lister()

	var vanished int
	for name, id := range unfinished {
		if _, err := lister.PipelineRuns(o.client.Namespace).Get(name); err == nil {
			continue
		}

		// Le cache dit « absent ». Une vérification directe auprès de l'API
		// tranche : un cache incomplet ferait conclure à tort, et la conclusion
		// est irréversible pour l'utilisateur.
		if _, err := o.client.Tekton.TektonV1().PipelineRuns(o.client.Namespace).
			Get(ctx, name, metav1.GetOptions{}); err == nil {
			continue
		}

		if err := o.store.ConcludeVanishedRun(ctx, id, ReasonVanished); err != nil {
			o.logger.Error("conclusion d'un run disparu", "run", name, "error", err)
			continue
		}
		o.logger.Info("run conclu: PipelineRun absent du cluster", "run", name)
		vanished++
	}

	o.logger.Info("réconciliation terminée", "en_cours", len(unfinished), "conclus", vanished)
	return nil
}
