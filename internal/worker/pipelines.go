package worker

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
)

/*
 * Putting the capabilities' pipelines in place, then checking what the cluster can run.
 *
 * Same shape as the deploy-key loop, and for the same reasons: the worker carries out under the
 * lease, and a restart catches up what an outage interrupted.
 *
 * **Install, then check.** The order is the point: checking first refused to start on a fresh
 * cluster, whose Pipelines the dashboard would have installed one step later.
 *
 * **A pipeline at odds with the cluster stops only itself.** Its runs are refused with the reason,
 * while the rest of the catalogue keeps running.
 *
 * On an instance that already has its own definitions, the non-adoption rule makes the dashboard
 * observe and withdraw.
 */

// capabilityInterval spaces two passes.
//
// Long: the state changes when the binary changes version, which goes through a restart, or when
// someone deploys a Pipeline by hand, which the next pass picks up.
const capabilityInterval = 10 * time.Minute

// ResourceInstaller puts Tekton definitions in place, and withdraws those of plugins no longer
// carried.
type ResourceInstaller interface {
	InstallResources(ctx context.Context, resources []pipelines.Resource) (pipelines.Outcome, error)
	Prune(ctx context.Context, carried func(plugin string) bool) ([]string, error)
}

// AvailabilitySink receives, by kind, why the pipelines that cannot run cannot.
type AvailabilitySink interface {
	SetAvailability(unavailable map[string]string)
}

// CapabilityReconciler installs the bound capabilities' definitions and checks the catalogue
// against the cluster.
type CapabilityReconciler struct {
	installer ResourceInstaller
	// resources are read at every pass: what is bound changes while the dashboard runs.
	resources func() []pipelines.Resource
	// carried lists the plugins whose definitions stay. Read from what is recorded rather than from
	// the capabilities in force: a plugin that failed to load is still installed, and its
	// definitions are not withdrawn for a passing failure.
	carried func(ctx context.Context) (map[string]bool, error)
	wake    chan struct{}
	catalog catalog.Source
	cluster catalog.Cluster
	sink    AvailabilitySink
	logger  *slog.Logger
	last    map[string]string
}

// NewCapabilityReconciler wires the loop. The installer may be nil: definitions are then only
// checked, as on an instance whose pipelines someone else deploys.
func NewCapabilityReconciler(
	installer ResourceInstaller, resources func() []pipelines.Resource,
	pipelines catalog.Source, cluster catalog.Cluster, sink AvailabilitySink, logger *slog.Logger,
) *CapabilityReconciler {
	return &CapabilityReconciler{
		installer: installer, resources: resources, catalog: pipelines,
		cluster: cluster, sink: sink, logger: logger, wake: make(chan struct{}, 1),
	}
}

// WithPruning withdraws, at every pass, the definitions of the plugins no longer carried.
func (r *CapabilityReconciler) WithPruning(carried func(ctx context.Context) (map[string]bool, error)) *CapabilityReconciler {
	r.carried = carried
	return r
}

// Wake asks for a pass now: the capabilities in force changed, and a newly bound one must not
// wait ten minutes for its definitions.
func (r *CapabilityReconciler) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run settles again at every interval, until cancellation. The first pass is the caller's, through
// Settle, before the scheduler starts.
func (r *CapabilityReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(capabilityInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("pipeline setting stopped")
			return nil
		case <-ticker.C:
			r.Settle(ctx)
		case <-r.wake:
			r.Settle(ctx)
		}
	}
}

// Settle installs, then checks and publishes what can run.
func (r *CapabilityReconciler) Settle(ctx context.Context) {
	r.install(ctx)

	unavailable, err := catalog.Of(r.catalog).Availability(ctx, r.cluster)
	if err != nil {
		// The previous answer stays published: a cluster briefly unreachable does not make every
		// pipeline unavailable.
		r.logger.Error("checking the catalogue against the cluster", "error", err)
		return
	}
	r.sink.SetAvailability(unavailable)

	for kind, reason := range unavailable {
		if r.last[kind] != reason {
			r.logger.Warn("pipeline unavailable", "kind", kind, "reason", reason,
				"consequence", "its runs are refused until the cluster provides what it needs")
		}
	}
	for kind := range r.last {
		if _, still := unavailable[kind]; !still {
			r.logger.Info("pipeline available again", "kind", kind)
		}
	}
	r.last = unavailable
}

func (r *CapabilityReconciler) install(ctx context.Context) {
	if r.installer == nil || r.resources == nil {
		return
	}
	r.prune(ctx)
	resources := r.resources()
	if len(resources) == 0 {
		return
	}
	outcome, err := r.installer.InstallResources(ctx, resources)
	if err != nil {
		// A failed definition does not stop the others: the pipeline that needs it will be reported
		// unavailable by the check that follows.
		r.logger.Error("setting the pipelines", "error", err)
	}
	if len(outcome.Installed) > 0 || len(outcome.Updated) > 0 {
		r.logger.Info("pipelines set", "installed", outcome.Installed, "updated", outcome.Updated)
	}
	if len(outcome.Skipped) > 0 {
		// Not an error: the refusal to adopt. Saying so avoids someone looking for why a definition
		// from the dashboard does not appear.
		r.logger.Info("definitions left to their owner", "resources", outcome.Skipped,
			"reason", "they already exist and were not created by the dashboard")
	}
}

func (r *CapabilityReconciler) prune(ctx context.Context) {
	if r.carried == nil {
		return
	}
	carried, err := r.carried(ctx)
	if err != nil {
		r.logger.Error("reading the plugins carried, definitions left in place", "error", err)
		return
	}
	withdrawn, err := r.installer.Prune(ctx, func(plugin string) bool { return carried[plugin] })
	if err != nil {
		r.logger.Error("withdrawing the definitions of uninstalled plugins", "error", err)
	}
	if len(withdrawn) > 0 {
		r.logger.Info("definitions withdrawn", "resources", withdrawn, "reason", "their plugin is uninstalled")
	}
}

// availability is the scheduler's copy of what the cluster cannot run, written by the reconciler
// and read at every dispatch.
type availability struct {
	mu          sync.RWMutex
	unavailable map[string]string
}

func (a *availability) set(unavailable map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.unavailable = maps.Clone(unavailable)
}

func (a *availability) reason(kind string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.unavailable[kind]
}
