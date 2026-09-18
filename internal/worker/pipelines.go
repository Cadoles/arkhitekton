package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
)

/*
 * Putting the reference libraries in place.
 *
 * Same shape as the deploy-key loop, and for the same reasons: the API declares, the worker
 * carries out under the lease, and a restart catches up what an outage interrupted.
 *
 * It serves **autonomy**: a fresh instance, with no pre-existing pipeline repository, receives
 * what it needs to build and test the stacks its projects declare. On an instance that already
 * has its own, the non-adoption rule makes the dashboard observe and withdraw.
 */

// libraryInterval spaces two passes.
//
// Long: the state changes only when a project declares a stack — a rare gesture — or when the
// binary changes version, which goes through a restart.
const libraryInterval = 10 * time.Minute

// StackStore returns the stacks to supply.
type StackStore interface {
	Stacks(ctx context.Context) ([]string, error)
}

// LibraryInstaller puts a stack's library in place.
type LibraryInstaller interface {
	Install(ctx context.Context, stack string) (pipelines.Outcome, error)
}

// LibraryReconciler puts in place the libraries the projects require.
type LibraryReconciler struct {
	store     StackStore
	installer LibraryInstaller
	logger    *slog.Logger
}

func NewLibraryReconciler(
	projects StackStore, installer LibraryInstaller, logger *slog.Logger,
) *LibraryReconciler {
	return &LibraryReconciler{store: projects, installer: installer, logger: logger}
}

// Run runs the loop until cancellation.
func (r *LibraryReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(libraryInterval)
	defer ticker.Stop()

	for {
		r.once(ctx)

		select {
		case <-ctx.Done():
			r.logger.Info("pipeline library setting stopped")
			return nil
		case <-ticker.C:
		}
	}
}

func (r *LibraryReconciler) once(ctx context.Context) {
	stacks, err := r.store.Stacks(ctx)
	if err != nil {
		r.logger.Error("declared stacks", "error", err)
		return
	}

	for _, stack := range stacks {
		outcome, err := r.installer.Install(ctx, stack)
		if err != nil {
			// A failed stack does not stop the others: a project whose library is missing will see it at
			// its first launch, whereas a stopped loop would deprive all the following ones.
			r.logger.Error("setting the library", "stack", stack, "error", err)
		}

		// Logged only when something moves: the loop comes round every ten minutes, and an identical
		// report at every pass would drown what matters.
		if len(outcome.Installed) > 0 || len(outcome.Updated) > 0 {
			r.logger.Info("pipeline library set",
				"stack", stack, "installed", outcome.Installed, "updated", outcome.Updated)
		}
		if len(outcome.Skipped) > 0 {
			// Not an error: the refusal to adopt. Saying so avoids someone looking for why a definition
			// from the dashboard does not appear.
			r.logger.Info("definitions left to their owner",
				"stack", stack, "resources", outcome.Skipped,
				"reason", "they already exist and were not created by the dashboard")
		}
	}
}
