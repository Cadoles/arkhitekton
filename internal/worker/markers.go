package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Wiring the marker channel.
 *
 * The Tasks emit one descriptor per tool on their standard output —
 * `##META## {"tool":"phpstan","status":"ok"}` — and `internal/results` has long known how to
 * read it. Nobody called it: the function was written, tested, and had not a single caller.
 *
 * What that absence cost: **a crashed tool could not be told apart from a tool that found
 * nothing**. Both publish zero violations, one because the code is healthy, the other because
 * the scanner died before starting. Without the descriptor, the second displayed as
 * "compliant". That is exactly the property the channel existed to preserve, and it was lost.
 *
 * The descriptor is produced by the pipeline, **never deduced here**. It alone can tell the
 * difference, and a dashboard that guessed would be wrong at the first tool that exits with 1
 * to say "I found something".
 */

// maxMarkerScanBytes bounds how much of a task's log is read while looking for markers.
//
// A descriptor weighs about a hundred bytes and can be anywhere in the stream — each tool
// emits its own as soon as it has finished. The log must therefore be walked, and the bound
// stops a log gone wild bringing the worker to its knees. Crossing it is reported: a missed
// marker reads as an absent marker, and not saying so would make a truncated read look like a
// mute pipeline.
const maxMarkerScanBytes = 8 << 20

// LogSource is what the ingester needs from the log reader.
type LogSource interface {
	From(ctx context.Context, runName, taskName string, offset, length int64) (logs.Segment, error)
}

// MarkerStore is what the ingester needs from the database.
type MarkerStore interface {
	SaveTestResult(ctx context.Context, result store.TestResult, cases []store.TestCase) (int64, error)
	TestResultExists(ctx context.Context, runID int64, tool string) (bool, error)
}

// MarkerIngester records what a run's descriptors say about the tools that did not conclude.
type MarkerIngester struct {
	store   MarkerStore
	source  LogSource
	markers results.MarkerSet
	logger  *slog.Logger
}

func NewMarkerIngester(
	db MarkerStore, source LogSource, markers results.MarkerSet, logger *slog.Logger,
) *MarkerIngester {
	return &MarkerIngester{store: db, source: source, markers: markers, logger: logger}
}

// MarkerRun is the run to examine.
type MarkerRun struct {
	ID        int64
	Name      string
	Tasks     []string
	ReleaseID *int64
	// Family is the signal family the pipeline measures, as the catalogue declares it. Empty,
	// nothing is recorded: a result with no family attaches to no signal and would appear nowhere.
	Family string
}

// Ingest reads a finished run's logs and records the tools that did not conclude.
//
// **Only** those. A tool that did its work publishes an artifact, and that artifact carries
// the count: drawing a second result from the descriptor would give two rows for one run,
// with nothing saying which is authoritative.
//
// Called **before** the artifacts are ingested, and the order carries a decision: both are
// idempotent per tool, so the first to arrive wins. The descriptor must win — it is the only
// one that knows the tool failed, where a partial artifact would read as a complete result.
func (i *MarkerIngester) Ingest(ctx context.Context, run MarkerRun) error {
	if run.Family == "" || len(run.Tasks) == 0 {
		return nil
	}

	var failures []string
	for _, task := range run.Tasks {
		text, err := i.readTask(ctx, run.Name, task)
		if err != nil {
			// A task with no log is a normal case: it may have been skipped, or its collector sealed
			// nothing. That is no reason to lose the others' markers.
			i.logger.Debug("log unreadable for the marker reading",
				"run", run.Name, "task", task, "error", err)
			continue
		}

		// The timestamp is stripped before parsing: the stored chunks keep it, and a marker preceded
		// by a `2026-08-25T14:33:39Z ` is no longer at the start of the line. Without that the
		// channel recognises nothing, with not a single message — the logs read fine, they simply do
		// not parse.
		for tool, meta := range i.markers.ScanMarkers(logs.Plain(text)).Metas {
			if meta.Ran() {
				continue
			}
			if err := i.record(ctx, run, tool, meta); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", tool, err))
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("markers of run %q: %s", run.Name, strings.Join(failures, "; "))
	}
	return nil
}

// record records a tool that did not conclude.
func (i *MarkerIngester) record(
	ctx context.Context, run MarkerRun, tool string, meta results.Meta,
) error {
	exists, err := i.store.TestResultExists(ctx, run.ID, tool)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// `na` and not `fail`, and the nuance is the whole point of the channel: "failure" reads as a
	// verdict on the code, whereas here it is the tool that gave no verdict. The score takes this
	// signal out of the computation and redistributes its weight, instead of counting a zero that
	// would be wrong.
	result := store.TestResult{
		RunID:     run.ID,
		ReleaseID: run.ReleaseID,
		Family:    run.Family,
		Shape:     legacyShape(run.Family),
		Producer:  legacyProducer(tool),
		Tool:      tool,
		Status:    results.ReportNA,
		Summary:   markerSummary(meta),
	}

	if _, err := i.store.SaveTestResult(ctx, result, nil); err != nil {
		return err
	}
	i.logger.Warn("tool with no conclusion",
		"run", run.Name, "tool", tool, "status", meta.Status,
		"exit_code", meta.ExitCode, "detail", meta.Error)
	return nil
}

// markerSummary composes the line displayed for a tool that did not conclude.
//
// The pipeline's message when there is one, otherwise a sentence that at least says why the
// row exists. An empty cell would read as a measurement missing with no cause, which is half
// the information.
func markerSummary(meta results.Meta) string {
	if detail := strings.TrimSpace(meta.Error); detail != "" {
		return detail
	}
	if status := strings.TrimSpace(meta.Status); status != "" {
		return fmt.Sprintf("the tool did not conclude (status %q, code %d)", status, meta.ExitCode)
	}
	return fmt.Sprintf("the tool did not conclude (code %d)", meta.ExitCode)
}

// readTask reads a task's log, bounded.
//
// The log is read in chunks and not in one block: that is the reader's interface, which
// serves live following first. The loop stops at the end of the log or at the bound,
// whichever comes first.
func (i *MarkerIngester) readTask(ctx context.Context, runName, taskName string) (string, error) {
	var builder strings.Builder
	var offset int64

	for {
		segment, err := i.source.From(ctx, runName, taskName, offset, 0)
		if err != nil {
			return "", err
		}
		builder.Write(segment.Content)

		if segment.NextOffset >= segment.Size || segment.NextOffset <= offset {
			return builder.String(), nil
		}
		offset = segment.NextOffset

		if int64(builder.Len()) >= maxMarkerScanBytes {
			i.logger.Warn("log truncated for the marker reading",
				"run", runName, "task", taskName,
				"read", builder.Len(), "size", segment.Size)
			return builder.String(), nil
		}
	}
}

// markerRunFor composes what needs to be known about a run in order to look for markers in it.
//
// Returns `false` when the catalogue does not say which family the pipeline measures: without
// it, a tool with no artifact attaches to no signal.
func markerRunFor(
	pipelines *catalog.Catalog, kind string, run MarkerRun,
) (MarkerRun, bool) {
	if pipelines == nil {
		return MarkerRun{}, false
	}
	pipeline, found := pipelines.Find(kind)
	if !found {
		return MarkerRun{}, false
	}
	run.Family = pipeline.Family()
	return run, run.Family != ""
}
