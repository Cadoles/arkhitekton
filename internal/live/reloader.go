package live

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Reloader builds the capabilities in force again when what they are built from changes: the
// database's counter of installations and bindings, watched at an interval.
type Reloader struct {
	holder   *Holder
	version  func(ctx context.Context) (int64, error)
	build    func(ctx context.Context, version int64) (Snapshot, error)
	interval time.Duration
	logger   *slog.Logger

	mu       sync.Mutex
	failed   int64
	replaced []func(Snapshot)
}

// NewReloader wires a holder to what it is built from.
func NewReloader(
	holder *Holder, version func(ctx context.Context) (int64, error),
	build func(ctx context.Context, version int64) (Snapshot, error),
	interval time.Duration, logger *slog.Logger,
) *Reloader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reloader{holder: holder, version: version, build: build, interval: interval, logger: logger, failed: -1}
}

// OnReplace calls a function with every snapshot put in force.
func (r *Reloader) OnReplace(replaced func(Snapshot)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replaced = append(r.replaced, replaced)
}

// Reload builds the capabilities again if the counter moved. A build that fails leaves the
// snapshot in force and is tried again at the next reload: what failed may be passing, object
// storage briefly out of reach. It is logged once per version.
func (r *Reloader) Reload(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	version, err := r.version(ctx)
	if err != nil {
		return err
	}
	if version == r.holder.Current().Version {
		return nil
	}
	next, err := r.build(ctx, version)
	if err != nil {
		if r.failed != version {
			r.failed = version
			r.logger.Error("capabilities not rebuilt, the previous ones stay in force", "version", version, "error", err)
		}
		return err
	}
	next.Version = version
	r.holder.Replace(next)
	r.logger.Info("capabilities rebuilt", "version", version, "kinds", next.Catalog.Kinds())
	for _, replaced := range r.replaced {
		replaced(next)
	}
	return nil
}

// Run reloads at every interval until the context ends.
func (r *Reloader) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.Reload(ctx); err != nil {
				r.logger.Debug("reloading capabilities", "error", err)
			}
		}
	}
}
