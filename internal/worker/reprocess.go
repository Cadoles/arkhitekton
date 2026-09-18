package worker

import (
	"context"
	"fmt"
	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Replaying the results from the raw artifacts.
 *
 * A parser gets fixed. A metric is added. A test family changes format. Each time, the history
 * already projected becomes wrong — and rebuilding it by re-running the pipelines is
 * impossible: the runs are past, their target has moved, and a forty-minute E2E campaign is not
 * replayed to fix a regular expression.
 *
 * The replay therefore starts from the **raw data kept**. Each report records the key of the
 * object it came from, and that key is what makes the history rebuildable without re-running
 * anything.
 *
 * Three properties follow.
 *
 * **Erase then rebuild, never stack.** A replay that added would duplicate every case and skew
 * any comparison between releases — precisely what the replay serves to fix.
 *
 * **One run at a time, and one's failure does not carry off the others.** Replaying fifty runs
 * and losing everything because the third has a purged artifact would be a regression worse
 * than the original defect.
 *
 * **The dry-run mode is the default mode of doubt.** Replaying erases before rebuilding: being
 * able to see what would disappear, destroying nothing, is what makes the operation defensible.
 */

// ReprocessStore is what the replay needs from the database.
type ReprocessStore interface {
	ListReprocessTargets(ctx context.Context, releaseID int64) ([]store.ReprocessTarget, error)
	DeleteTestResults(ctx context.Context, runID int64) (int64, error)
}

// Reprocessor replays the results of one or more runs.
type Reprocessor struct {
	store    ReprocessStore
	tests    *TestIngester
	security *SecurityIngester
	load     *LoadIngester
	logger   *slog.Logger
}

func NewReprocessor(
	targets ReprocessStore, tests *TestIngester, security *SecurityIngester,
	load *LoadIngester, logger *slog.Logger,
) *Reprocessor {
	return &Reprocessor{store: targets, tests: tests, security: security, load: load, logger: logger}
}

// ReprocessOptions bounds what the replay touches.
type ReprocessOptions struct {
	// ReleaseID limits the replay to one release. Zero replays the whole history.
	ReleaseID int64
	// DryRun neither erases nor rebuilds: it lists what would be replayed. This mode exists because
	// the replay destroys before rebuilding, and a destructive operation must be inspectable before
	// being launched.
	DryRun bool
}

// ReprocessReport accounts for what was done.
type ReprocessReport struct {
	Examined int
	Replayed int
	Deleted  int64
	// Failed names the runs whose replay failed. Named and not counted: knowing which ones is what
	// allows deciding whether to worry.
	Failed []string
}

// Run replays the results.
func (r *Reprocessor) Run(ctx context.Context, options ReprocessOptions) (ReprocessReport, error) {
	targets, err := r.store.ListReprocessTargets(ctx, options.ReleaseID)
	if err != nil {
		return ReprocessReport{}, err
	}

	report := ReprocessReport{Examined: len(targets)}

	for _, target := range targets {
		if options.DryRun {
			r.logger.Info("dry run",
				"run", target.RunName, "outils actuels", target.Tools)
			continue
		}

		deleted, err := r.replay(ctx, target)
		if err != nil {
			// One run's failure does not carry off the others: a purged artifact must not deprive the whole
			// history of its rebuild.
			r.logger.Error("rejeu", "run", target.RunName, "error", err)
			report.Failed = append(report.Failed, target.RunName)
			continue
		}

		report.Replayed++
		report.Deleted += deleted
	}

	return report, nil
}

// replay erases a run's results then rebuilds them from the raw data.
//
// The erasure deliberately precedes the rebuild: the ingesters are idempotent and would refuse
// to write over what exists, which would make the replay useless — the exact opposite of what
// is asked.
//
// The price is real: between the erasure and the rebuild, the run has no results. An
// interruption at that moment leaves a hole. It is accepted because the raw data stays — a
// second replay repairs it, whereas a duplicated projection would need a manual clean-up.
func (r *Reprocessor) replay(ctx context.Context, target store.ReprocessTarget) (int64, error) {
	deleted, err := r.store.DeleteTestResults(ctx, target.RunID)
	if err != nil {
		return 0, err
	}

	run := LoadRun{ID: target.RunID, Name: target.RunName, ReleaseID: target.ReleaseID}

	// All three ingesters are replayed: a run may carry test reports, an audit and a load campaign.
	// Replaying only one would rebuild part of what has just been erased.
	var problems []error
	if r.tests != nil {
		if err := r.tests.Ingest(ctx, run); err != nil {
			problems = append(problems, err)
		}
	}
	if r.security != nil {
		if err := r.security.Ingest(ctx, run); err != nil {
			problems = append(problems, err)
		}
	}
	if r.load != nil {
		if err := r.load.Ingest(ctx, run); err != nil {
			problems = append(problems, err)
		}
	}

	if len(problems) > 0 {
		return deleted, fmt.Errorf("run %q: %w", target.RunName, problems[0])
	}

	r.logger.Info("run replayed",
		"run", target.RunName, "reports erased", deleted, "previous tools", target.Tools)
	return deleted, nil
}
