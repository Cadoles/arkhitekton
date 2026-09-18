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

// Sink receives the projections. The interface exists so the observer does not
// depend on the database: it makes the wiring testable with an in-memory recorder.
type Sink interface {
	SaveRun(ctx context.Context, run Run) error
	SaveTaskRun(ctx context.Context, taskRun TaskRunProjection) error
}

// LogCollector is told about a task's life cycle, so its logs can be captured before
// the pods disappear.
//
// The interface is described here with this package's types rather than the
// collector's: neither package has a reason to depend on the other, and the
// adaptation happens once, in the worker.
type LogCollector interface {
	StartTaskLogs(ctx context.Context, runName, taskName, podName string, steps []Step)
	FinishTaskLogs(ctx context.Context, runName, taskName string, steps []Step) error
}

// Store describes what the observer needs from the database to reconcile.
type Store interface {
	UnfinishedRunNames(ctx context.Context, namespace string) (map[string]int64, error)
	ConcludeVanishedRun(ctx context.Context, id int64, reason string) error
}

// ReasonVanished is the reason recorded on a run whose PipelineRun disappeared.
//
// Deliberately distinct from an ordinary failure: "vanished" says there is neither
// log nor result to look for, where "failed" sends someone hunting a trace that does
// not exist. It is also the mark of a clean-up, since cron jobs delete old
// PipelineRuns.
const ReasonVanished = "PipelineRunDisparu"

// resyncPeriod forces a periodic replay of every object in the cache.
//
// Not redundant with the watches: a watch can be interrupted without the client
// noticing, and the replay is the safety net. It is also the reason for the guard on
// terminal states — it delivers objects in arbitrary order.
const resyncPeriod = 10 * time.Minute

// Observer projects a namespace's PipelineRuns and TaskRuns.
type Observer struct {
	client  *Client
	sink    Sink
	store   Store
	logs    LogCollector
	logger  *slog.Logger
	factory informers.SharedInformerFactory
}

// NewObserver assembles the informers. `collector` may be nil: observation then
// works without log capture.
func NewObserver(client *Client, sink Sink, store Store, collector LogCollector, logger *slog.Logger) *Observer {
	factory := informers.NewSharedInformerFactoryWithOptions(
		client.Tekton, resyncPeriod,
		// Bounded to the namespace: the service account has only a Role, and a cluster-wide
		// informer would fail in a loop on a "forbidden" nothing would connect to the RBAC.
		informers.WithNamespace(client.Namespace),
	)

	return &Observer{client: client, sink: sink, store: store, logs: collector, logger: logger, factory: factory}
}

// Run reconciles, then follows changes until the context is cancelled.
func (o *Observer) Run(ctx context.Context) error {
	runs := o.factory.Tekton().V1().PipelineRuns().Informer()
	tasks := o.factory.Tekton().V1().TaskRuns().Informer()

	if _, err := runs.AddEventHandler(o.runHandler()); err != nil {
		return fmt.Errorf("subscribing to PipelineRuns: %w", err)
	}
	if _, err := tasks.AddEventHandler(o.taskHandler()); err != nil {
		return fmt.Errorf("subscribing to TaskRuns: %w", err)
	}

	o.factory.Start(ctx.Done())

	// Waiting for the cache to sync before reconciling is indispensable: comparing the
	// database with an empty cache would conclude that every run vanished, and mark them
	// all failed.
	o.logger.Info("synchronising the Tekton cache", "namespace", o.client.Namespace)
	if !cache.WaitForCacheSync(ctx.Done(), runs.HasSynced, tasks.HasSynced) {
		return fmt.Errorf("cache synchronisation interrupted")
	}

	if err := o.reconcile(ctx); err != nil {
		// Failing here must not stop observation from starting: reconciliation catches up on
		// the past, the watches follow the present.
		o.logger.Error("reconciliation at start-up", "error", err)
	}

	<-ctx.Done()
	o.logger.Info("stopping Tekton observation")
	return nil
}

func (o *Observer) runHandler() cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { o.projectRun(obj) },
		// The old object is ignored: the projection is idempotent and the database carries
		// the guards. Comparing versions here would duplicate that logic where it would not
		// protect against concurrent writes.
		UpdateFunc: func(_, obj any) { o.projectRun(obj) },
		// No DeleteFunc. Deleting a PipelineRun says nothing about its outcome — clean-up
		// cron jobs remove dozens — and concluding on that event would erase the history the
		// dashboard exists to keep. Runs left in flight are handled by reconciliation, which
		// can tell "vanished" from "running".
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
		o.logger.Warn("unexpected object on the PipelineRun stream", "type", fmt.Sprintf("%T", obj))
		return
	}

	// A fresh context: the informer's is not passed to handlers, and a write with no
	// deadline would block the informer's queue.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	run := ProjectRun(pr)
	if err := o.sink.SaveRun(ctx, run); err != nil {
		o.logger.Error("projecting a PipelineRun", "run", run.Name, "error", err)
	}
}

func (o *Observer) projectTaskRun(obj any) {
	tr, ok := obj.(*tektonv1.TaskRun)
	if !ok {
		o.logger.Warn("unexpected object on the TaskRun stream", "type", fmt.Sprintf("%T", obj))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	projection := ProjectTaskRun(tr)
	if projection.RunName == "" {
		// A standalone TaskRun, with no parent PipelineRun: outside the dashboard's scope,
		// which follows pipelines.
		return
	}
	if err := o.sink.SaveTaskRun(ctx, projection); err != nil {
		o.logger.Error("projecting a TaskRun", "taskrun", projection.Name, "error", err)
	}

	o.followLogs(ctx, projection)
}

// followLogs starts or concludes the capture of a task's log.
//
// The order of the two branches matters: a task that has become terminal must be
// concluded even if the same event still carries a pod. Concluding waits for the
// capture goroutines to finish, then seals.
func (o *Observer) followLogs(ctx context.Context, projection TaskRunProjection) {
	if o.logs == nil {
		return
	}

	steps := make([]Step, 0, len(projection.Steps))
	for _, step := range projection.Steps {
		if step.Container != "" {
			steps = append(steps, step)
		}
	}
	if len(steps) == 0 {
		// Tekton has not filled in the containers yet: nothing to read, and the next event
		// will bring them.
		return
	}

	if projection.Status.Terminal() {
		// A fresh and generous context: sealing re-reads every chunk and writes an object,
		// which can take several seconds on a log tens of megabytes long.
		sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()

		if err := o.logs.FinishTaskLogs(sealCtx, projection.RunName, projection.Name, steps); err != nil {
			o.logger.Error("sealing the log",
				"run", projection.RunName, "task", projection.Name, "error", err)
		}
		return
	}

	if projection.PodName == "" {
		return
	}
	// The background context, not the event's: the capture must outlive the handling of
	// the event that started it.
	o.logs.StartTaskLogs(context.WithoutCancel(ctx), projection.RunName, projection.Name, projection.PodName, steps)
}

// reconcile compares what the database believes is running with what the cluster
// holds.
//
// Without this step, a restart leaves runs stuck in `running` that freeze their
// exclusion class — the most likely failure mode of the whole arrangement. Two cases
// arise:
//
//   - the PipelineRun exists: the cache's add events have just projected it, and
//     there is nothing to do;
//   - it has vanished: the run is concluded with a reason that states its cause.
func (o *Observer) reconcile(ctx context.Context) error {
	unfinished, err := o.store.UnfinishedRunNames(ctx, o.client.Namespace)
	if err != nil {
		return err
	}
	if len(unfinished) == 0 {
		o.logger.Info("reconciliation: no run in flight in the database")
		return nil
	}

	lister := o.factory.Tekton().V1().PipelineRuns().Lister()

	var vanished int
	for name, id := range unfinished {
		if _, err := lister.PipelineRuns(o.client.Namespace).Get(name); err == nil {
			continue
		}

		// The cache says "absent". A direct check against the API settles it: an incomplete
		// cache would make the wrong conclusion, and the conclusion is irreversible for the
		// user.
		if _, err := o.client.Tekton.TektonV1().PipelineRuns(o.client.Namespace).
			Get(ctx, name, metav1.GetOptions{}); err == nil {
			continue
		}

		if err := o.store.ConcludeVanishedRun(ctx, id, ReasonVanished); err != nil {
			o.logger.Error("concluding a vanished run", "run", name, "error", err)
			continue
		}
		o.logger.Info("run concluded: PipelineRun missing from the cluster", "run", name)
		vanished++
	}

	o.logger.Info("reconciliation finished", "en_cours", len(unfinished), "conclus", vanished)
	return nil
}
