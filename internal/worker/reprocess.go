package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
)

/*
 * Replaying the results from the raw artifacts.
 *
 * A parser gets fixed. A metric is added. A test family changes format. Each time, the history
 * already projected becomes wrong — and rebuilding it by re-running the pipelines is
 * impossible: the runs are past, their target has moved, and a forty-minute E2E campaign is not
 * replayed to fix a regular expression.
 *
 * The replay therefore starts from the **raw data kept**, read again by the interpreter of the
 * capability that launched the run, exactly as a finished run is read.
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
	ResetInterpretation(ctx context.Context, runID int64) (int64, []store.PinnedBaseline, error)
	RestoreBaselines(ctx context.Context, runID int64, baselines []store.PinnedBaseline) (int, error)
}

// RunReader reads a finished run.
type RunReader interface {
	Ingest(ctx context.Context, runID int64) error
}

// Reprocessor replays the results of one or more runs.
type Reprocessor struct {
	store  ReprocessStore
	reader RunReader
	// versions gives, by plugin, the version installed now.
	versions map[string]string
	logger   *slog.Logger
}

func NewReprocessor(
	targets ReprocessStore, reader RunReader, versions map[string]string, logger *slog.Logger,
) *Reprocessor {
	return &Reprocessor{store: targets, reader: reader, versions: versions, logger: logger}
}

// ReprocessOptions bounds what the replay touches.
type ReprocessOptions struct {
	// ReleaseID limits the replay to one release. Zero replays the whole history.
	ReleaseID int64
	// DryRun neither erases nor rebuilds: it lists what would be replayed. This mode exists because
	// the replay destroys before rebuilding, and a destructive operation must be inspectable before
	// being launched.
	DryRun bool
	// AcceptDrift replays runs that another plugin version read. Without it they are refused: a
	// replay through another reading changes the history, and that must be asked for.
	AcceptDrift bool
}

// ReprocessReport accounts for what was done.
type ReprocessReport struct {
	Examined int
	Replayed int
	Deleted  int64
	// Failed names the runs whose replay failed. Named and not counted: knowing which ones is what
	// allows deciding whether to worry.
	Failed []string
	// Refused names the runs another plugin version read, left as they are.
	Refused []string
}

// Run replays the results.
func (r *Reprocessor) Run(ctx context.Context, options ReprocessOptions) (ReprocessReport, error) {
	targets, err := r.store.ListReprocessTargets(ctx, options.ReleaseID)
	if err != nil {
		return ReprocessReport{}, err
	}

	report := ReprocessReport{Examined: len(targets)}

	for _, target := range targets {
		if drift := r.drift(target.Producers); drift != "" && !options.AcceptDrift {
			r.logger.Warn("replay refused", "run", target.RunName, "reason", drift)
			report.Refused = append(report.Refused, target.RunName)
			continue
		}
		if options.DryRun {
			r.logger.Info("dry run", "run", target.RunName, "tools", target.Tools, "producers", target.Producers)
			continue
		}

		deleted, err := r.replay(ctx, target)
		if err != nil {
			// One run's failure does not carry off the others: a purged artifact must not deprive the whole
			// history of its rebuild.
			r.logger.Error("replay", "run", target.RunName, "error", err)
			report.Failed = append(report.Failed, target.RunName)
			continue
		}

		report.Replayed++
		report.Deleted += deleted
	}

	return report, nil
}

// drift says why a run's readers differ from the plugins installed, if they do. Results from
// before the plugins read their runs carry a `-legacy` version: replaying them is what moves them
// onto the plugins' reading, and is never a drift.
func (r *Reprocessor) drift(producers []string) string {
	for _, raw := range producers {
		producer, err := facts.ParseProducer(raw)
		if err != nil || strings.HasSuffix(producer.Version, "-legacy") {
			continue
		}
		if installed, known := r.versions[producer.Plugin]; known && installed != producer.Version {
			return fmt.Sprintf("read by %s, %s installed", producer, installed)
		}
	}
	return ""
}

// replay erases a run's results then reads the run again.
//
// The erasure precedes the reading: a run is read once, and its mark must be cleared for it to be
// read again. Between the two the run has no results; an interruption at that moment leaves a hole
// a second replay repairs, whereas a duplicated projection would need a manual clean-up.
func (r *Reprocessor) replay(ctx context.Context, target store.ReprocessTarget) (int64, error) {
	deleted, baselines, err := r.store.ResetInterpretation(ctx, target.RunID)
	if err != nil {
		return 0, err
	}
	if err := r.reader.Ingest(ctx, target.RunID); err != nil {
		return deleted, fmt.Errorf("run %q: %w", target.RunName, err)
	}
	restored, err := r.store.RestoreBaselines(ctx, target.RunID, baselines)
	if err != nil {
		return deleted, err
	}
	if restored < len(baselines) {
		r.logger.Warn("baselines not restored", "run", target.RunName,
			"pinned", len(baselines), "restored", restored,
			"reason", "the replay did not produce their measures again")
	}

	r.logger.Info("run replayed",
		"run", target.RunName, "reports erased", deleted, "previous tools", target.Tools)
	return deleted, nil
}
