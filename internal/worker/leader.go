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

// RunAsLeader acquires the Lease then runs `work`. The context passed to `work` is cancelled
// as soon as the lease is lost.
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
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: LeaseName, Namespace: cfg.Namespace},
		Client:     kube.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: cfg.Identity},
	}

	// workErr is set by the work goroutine and read after the election ends:
	// `leaderelection.Run` returns no error.
	var workErr error

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   30 * time.Second,
		RenewDeadline:   20 * time.Second,
		RetryPeriod:     5 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadingCtx context.Context) {
				logger.Info("lease acquired", "identity", cfg.Identity, "lease", LeaseName)
				if err := work(leadingCtx); err != nil {
					workErr = err
					logger.Error("background loops interrupted", "error", err)
				}
			},
			OnStoppedLeading: func() {
				// Neither panic nor restart: the process stays a candidate. A lease lost during a network
				// switch is a normal incident, and exiting with an error would turn a second of unavailability
				// into a crash loop.
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

	elector.Run(ctx)
	return workErr
}
