package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// LeaseName is the name of the Lease that arbitrates between workers.
const LeaseName = "arkhitekton-worker"

// LeaderConfig describes the election.
type LeaderConfig struct {
	// Namespace of the Lease: the dashboard's, never the pipelines'. The Role granted covers only
	// the first (docs/09-security.md).
	Namespace string
	// Identity must be unique per process. The pod's name guarantees it; in development, the host
	// name is enough.
	Identity string
}

// LeaderIdentity derives an identity from the execution context.
//
// POD_NAME is injected by the Deployment. Without it, the log would say a leader exists
// without saying which — precisely the useful piece of information when two processes are
// contending for the place.
func LeaderIdentity() string {
	if pod := os.Getenv("POD_NAME"); pod != "" {
		return pod
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host + "-local"
	}
	return "unknown"
}

// standAgainAfter is the pause between a leadership term that ended and the next candidacy: the
// loops of the term that ended see their context cancelled and stop in the meantime, and a loop
// that keeps failing restarts at this pace rather than in a tight cycle.
var standAgainAfter = 5 * time.Second

// RunAsLeader stands for the Lease, runs `work` while it holds it, and stands again when a term
// ends, until ctx is cancelled. The context passed to `work` is cancelled as soon as the lease is
// lost.
//
// A term ends in two ways. The lease is lost: the loops stop, the process stays a candidate, and a
// lease lost during a network switch costs a second of unavailability rather than a crash loop.
// `work` returns: a dead loop gives the lease up rather than let the others run half-way, and the
// loops restart at the next term, here or in another worker.
//
// The durations follow the values client-go recommends, with a deliberately wide ratio
// between the lease and the renewal period: a busy worker — log collection saturates its
// goroutines — must not lose the lease over a renewal two seconds late. Losing the lease
// wrongly means a switch-over, therefore an interruption of the collection, therefore lines
// lost.
func RunAsLeader(
	ctx context.Context,
	kube kubernetes.Interface,
	cfg LeaderConfig,
	logger *slog.Logger,
	work func(context.Context) error,
) error {
	for {
		if err := runTerm(ctx, kube, cfg, logger, work); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(standAgainAfter):
		}
	}
}

// runTerm stands once for the lease and returns when the term ends, once `work` has returned.
func runTerm(
	ctx context.Context,
	kube kubernetes.Interface,
	cfg LeaderConfig,
	logger *slog.Logger,
	work func(context.Context) error,
) error {
	termCtx, endTerm := context.WithCancel(ctx)
	defer endTerm()

	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: LeaseName, Namespace: cfg.Namespace},
		Client:     kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: cfg.Identity},
	}

	// Closed when `work` starts, and when it returns: standing again before would run two copies of
	// the loops.
	workStarted := make(chan struct{})
	workDone := make(chan struct{})

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   30 * time.Second,
		RenewDeadline:   20 * time.Second,
		RetryPeriod:     5 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadingCtx context.Context) {
				close(workStarted)
				defer close(workDone)
				logger.Info("lease acquired", "identity", cfg.Identity, "lease", LeaseName)
				if err := work(leadingCtx); err != nil && leadingCtx.Err() == nil {
					logger.Error("background loops interrupted, giving the lease up", "error", err)
				}
				// Ending the term releases the lease: ReleaseOnCancel.
				endTerm()
			},
			OnStoppedLeading: func() {
				logger.Warn("lease lost", "identity", cfg.Identity)
			},
			OnNewLeader: func(identity string) {
				if identity == cfg.Identity {
					return
				}
				logger.Info("another worker leads", "leader", identity)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("leader election: %w", err)
	}

	elector.Run(termCtx)
	// Run gives up a candidacy only when ctx ends: otherwise it returns after a term, whose work was
	// started and is stopping, its context cancelled.
	if ctx.Err() == nil {
		<-workDone
		return nil
	}
	select {
	case <-workStarted:
		<-workDone
	default:
	}
	return nil
}
