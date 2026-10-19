package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// LoadStore is what the ingester needs from the database.
type LoadStore interface {
	SaveLoadResult(ctx context.Context, result store.LoadResult, measures []store.BenchMeasure) (int64, error)
	StartConditions(ctx context.Context, jobID int64) (bool, int, error)
	LoadResultExists(ctx context.Context, runID int64, scenario string) (bool, error)
}

// Objects is what the ingester needs from storage.
type Objects interface {
	List(ctx context.Context, prefix string) ([]ObjectRef, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ObjectRef is the bare minimum needed to find a summary again.
type ObjectRef struct {
	Key  string
	Size int64
}

// LoadIngester records the load campaigns a run produced.
//
// Ingestion is triggered by the **end of the run**, and not by a notification from the
// runner: the runner lives outside the cluster, publishes to MinIO with a presigned URL the
// dashboard supplies, and has no credential and no return path to the dashboard. The only
// reliable signal is therefore the one the dashboard already has — the Tekton observer sees
// the end of the PipelineRun that drove the runner.
type LoadIngester struct {
	store   LoadStore
	objects Objects
	metrics []bench.Metric
	logger  *slog.Logger
}

// loadDirectories lists the directories where a load summary may live.
//
// `load` is the kind's name, the one the contract publishes; `k6` is the tool's name, the one
// the MSE Tasks use. Both are read: removing the second would break existing runs, refusing
// the first would contradict the contract.
var loadDirectories = []string{"load", "k6"}

func NewLoadIngester(releases LoadStore, objects Objects, logger *slog.Logger) *LoadIngester {
	return &LoadIngester{store: releases, objects: objects, metrics: bench.DefaultMetrics, logger: logger}
}

// Ingest reads a finished run's k6 summaries and records them.
//
// Idempotent: the observer may see a finished run again — at restart, or because an informer
// resynchronises its cache. Recording again would produce duplicate history points, that is,
// a false trend.
func (i *LoadIngester) Ingest(ctx context.Context, run LoadRun) error {
	// Two directories, and it is the published contract that requires it. The MSE Tasks write
	// under `k6/`, from the tool's name; the contract announces that the directory names the
	// **kind**, hence `load/`. A project following the documented convention was not read at all,
	// and the load screen stayed empty with nothing saying why.
	var objects []ObjectRef
	for _, directory := range loadDirectories {
		prefix := "raw/" + run.Name + "/" + directory + "/"
		found, err := i.objects.List(ctx, prefix)
		if err != nil {
			return fmt.Errorf("summaries of run %q: %w", run.Name, err)
		}
		objects = append(objects, found...)
	}
	if len(objects) == 0 {
		// A load run with no summary is not an anomaly of the ingester: the runner may have failed
		// before publishing, and the run already carries its failure.
		i.logger.Info("no load summary", "run", run.Name)
		return nil
	}

	exclusive, concurrent := false, 0
	if run.JobID != 0 {
		conditions, count, err := i.store.StartConditions(ctx, run.JobID)
		if err != nil && !errors.Is(err, store.ErrJobNotFound) {
			return err
		}
		exclusive, concurrent = conditions, count
	}

	// A scenario is recorded only once per pass. The idempotence guard is `LoadResultExists`, but
	// it queries the database: relying on it **inside** the loop would make correctness depend on
	// a read-after-write, and two objects naming the same scenario — the same summary published
	// under `k6/` and under `load/` — would produce two history points, that is, a false trend.
	seen := make(map[string]bool, len(objects))

	var failures []string
	for _, object := range objects {
		if !strings.HasSuffix(object.Key, ".json") {
			continue
		}
		scenario := bench.ScenarioFromKey(object.Key)
		if seen[scenario] {
			continue
		}
		seen[scenario] = true
		if err := i.ingestOne(ctx, run, object, exclusive, concurrent); err != nil {
			// An unreadable summary does not prevent recording the others: a four-scenario campaign must
			// not be lost whole because one of them produced a truncated file.
			i.logger.Error("load summary", "run", run.Name, "objet", object.Key, "error", err)
			failures = append(failures, object.Key)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("run %q: %d summary(ies) not recorded: %s",
			run.Name, len(failures), strings.Join(failures, ", "))
	}
	return nil
}

func (i *LoadIngester) ingestOne(
	ctx context.Context, run LoadRun, object ObjectRef, exclusive bool, concurrent int,
) error {
	scenario := bench.ScenarioFromKey(object.Key)

	known, err := i.store.LoadResultExists(ctx, run.ID, scenario)
	if err != nil {
		return err
	}
	if known {
		return nil
	}

	content, err := i.objects.Get(ctx, object.Key)
	if err != nil {
		return err
	}
	defer content.Close()

	raw, err := io.ReadAll(content)
	if err != nil {
		return err
	}

	summary, err := bench.ParseSummary(scenario, raw, i.metrics)
	if err != nil {
		return err
	}

	measures := make([]store.BenchMeasure, 0, len(summary.Measures))
	for _, measure := range summary.Measures {
		measures = append(measures, store.BenchMeasure{
			Metric: measure.Metric, Label: measure.Label, Unit: measure.Unit,
			Value: measure.Value, LowerBetter: measure.LowerBetter,
			Volume: measure.Volume, ThresholdOK: measure.ThresholdOK,
		})
	}

	_, err = i.store.SaveLoadResult(ctx, store.LoadResult{
		RunID: run.ID, ReleaseID: run.ReleaseID, Scenario: scenario,
		Group: "load", Tool: "k6", Producer: legacyProducer("k6"),
		// The conditions **observed**, collected when the job was reserved. Not those asked for: the
		// barrier drains before launching, but it is the observation that decides whether the
		// measurement enters the comparison.
		Exclusive: exclusive, ConcurrentJobs: concurrent,
		DurationMS: run.DurationMS,
		Summary:    summarise(summary),
		RawObject:  object.Key,
	}, measures)
	if err != nil {
		return err
	}

	i.logger.Info("load campaign recorded",
		"run", run.Name, "scenario", scenario, "mesures", len(measures),
		"exclusive", exclusive, "concurrents", concurrent)
	return nil
}

// LoadRun is the run whose results are being ingested.
type LoadRun struct {
	ID         int64
	Name       string
	JobID      int64
	ReleaseID  *int64
	DurationMS int64
}

// summarise returns a one-line summary, displayed without having to open the measurements.
func summarise(summary bench.Summary) string {
	parts := []string{fmt.Sprintf("%d mesure(s)", len(summary.Measures))}
	if summary.VUs > 0 {
		parts = append(parts, fmt.Sprintf("%d VUs at peak", summary.VUs))
	}
	if len(summary.ThresholdsFailed) > 0 {
		// Named and not counted: knowing *which* threshold gave way is what decides what follows.
		parts = append(parts, "seuils non tenus : "+strings.Join(summary.ThresholdsFailed, ", "))
	}
	return strings.Join(parts, ", ")
}

// objectStore adapts the MinIO client to what the ingester needs.
//
// The adapter exists so that the ingester stays testable without MinIO: what it needs fits in
// two methods, and requiring them on the whole client would force twelve to be simulated.
type objectStore struct {
	client interface {
		List(ctx context.Context, prefix string) ([]blob.ObjectInfo, error)
		Get(ctx context.Context, key string) (io.ReadCloser, error)
	}
}

// NewObjectStore returns the adapter the ingester consumes.
func NewObjectStore(client *blob.Client) Objects { return objectStore{client: client} }

func (o objectStore) List(ctx context.Context, prefix string) ([]ObjectRef, error) {
	objects, err := o.client.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	refs := make([]ObjectRef, 0, len(objects))
	for _, object := range objects {
		refs = append(refs, ObjectRef{Key: object.Key, Size: object.Size})
	}
	return refs, nil
}

func (o objectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return o.client.Get(ctx, key)
}
