package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeLoadStore struct {
	saved      []store.LoadResult
	measures   [][]store.BenchMeasure
	exclusive  bool
	concurrent int
	known      map[string]bool
}

func (f *fakeLoadStore) SaveLoadResult(
	_ context.Context, result store.LoadResult, measures []store.BenchMeasure,
) (int64, error) {
	f.saved = append(f.saved, result)
	f.measures = append(f.measures, measures)
	// The fake must model the database: after recording, the scenario exists. Without that,
	// `TestIngestIsIdempotent` passed for a bad reason — the guard was never actually crossed.
	if f.known == nil {
		f.known = map[string]bool{}
	}
	f.known[result.Scenario] = true
	return int64(len(f.saved)), nil
}

func (f *fakeLoadStore) StartConditions(context.Context, int64) (bool, int, error) {
	return f.exclusive, f.concurrent, nil
}

func (f *fakeLoadStore) LoadResultExists(_ context.Context, _ int64, scenario string) (bool, error) {
	return f.known[scenario], nil
}

type fakeObjects map[string]string

func (f fakeObjects) List(_ context.Context, prefix string) ([]ObjectRef, error) {
	var refs []ObjectRef
	for key := range f {
		if strings.HasPrefix(key, prefix) {
			refs = append(refs, ObjectRef{Key: key, Size: int64(len(f[key]))})
		}
	}
	return refs, nil
}

func (f fakeObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	content, found := f[key]
	if !found {
		return nil, errors.New("object missing")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

const summaryJSON = `{"metrics":{
  "http_req_duration":{"values":{"med":98,"p(95)":310.2}},
  "http_reqs":{"values":{"count":24000,"rate":400}},
  "vus_max":{"values":{"value":200}}
}}`

func newIngester(objects fakeObjects) (*fakeLoadStore, *LoadIngester) {
	releases := &fakeLoadStore{exclusive: true, concurrent: 0, known: map[string]bool{}}
	return releases, NewLoadIngester(releases, objects, slog.New(slog.DiscardHandler))
}

func TestIngestSavesOneCampaignPerScenario(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-1/k6/panier-summary.json":    summaryJSON,
		"raw/load-test-1/k6/recherche-summary.json": summaryJSON,
		// Neither a summary nor JSON: ignored without failing the rest.
		"raw/load-test-1/k6/console.log": "bruit",
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1", JobID: 9}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	if len(releases.saved) != 2 {
		t.Fatalf("%d campaign(s), expected 2", len(releases.saved))
	}
	scenarios := map[string]bool{}
	for _, result := range releases.saved {
		scenarios[result.Scenario] = true
		// The observed conditions travel with the campaign: without them, the measurement is only a
		// number.
		if !result.Exclusive || result.ConcurrentJobs != 0 {
			t.Errorf("conditions = %+v", result)
		}
	}
	if !scenarios["panier"] || !scenarios["recherche"] {
		t.Errorf("scenarios = %v", scenarios)
	}
}

// The observer sees a finished run again at restart. Recording again would produce duplicate
// history points, that is, a false trend.
func TestIngestIsIdempotent(t *testing.T) {
	objects := fakeObjects{"raw/load-test-1/k6/panier-summary.json": summaryJSON}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Fatalf("first ingestion: %v", err)
	}
	releases.known["panier"] = true

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Fatalf("seconde ingestion: %v", err)
	}
	if len(releases.saved) != 1 {
		t.Errorf("%d campaign(s) recorded, expected 1", len(releases.saved))
	}
}

// A four-scenario campaign must not be lost whole because one of them produced a truncated
// file.
func TestOneBadSummaryDoesNotLoseTheOthers(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-1/k6/panier-summary.json":  summaryJSON,
		"raw/load-test-1/k6/tronque-summary.json": `{"metr`,
	}
	releases, ingester := newIngester(objects)

	err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"})
	if err == nil {
		t.Error("the partial failure must be reported")
	}
	if len(releases.saved) != 1 {
		t.Errorf("%d campaign(s) recorded, expected 1", len(releases.saved))
	}
	if !strings.Contains(err.Error(), "tronque") {
		t.Errorf("error = %v, expected the name of the offending summary", err)
	}
}

// The runner may have failed before publishing: the run already carries its failure.
func TestIngestWithoutSummariesIsNotAnError(t *testing.T) {
	releases, ingester := newIngester(fakeObjects{})

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Errorf("error = %v, expected none", err)
	}
	if len(releases.saved) != 0 {
		t.Error("nothing must be recorded")
	}
}

// A run triggered outside the dashboard has no job: its results are worth as much, but nothing
// can be asserted about its conditions.
func TestIngestWithoutAJobAssumesNonExclusive(t *testing.T) {
	objects := fakeObjects{"raw/load-test-1/k6/panier-summary.json": summaryJSON}
	releases, ingester := newIngester(objects)
	releases.exclusive = true

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1", JobID: 0}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if releases.saved[0].Exclusive {
		t.Error("with no job, no exclusivity can be asserted")
	}
}

func TestSummariseNamesTheFailedThresholds(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-1/k6/panier-summary.json": `{"metrics":{
		  "http_req_duration":{"values":{"p(95)":900},"thresholds":{"p(95)<500":{"ok":false}}},
		  "http_reqs":{"values":{"count":100,"rate":10}}
		}}`,
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	// Knowing *which* threshold gave way is what decides what follows.
	if !strings.Contains(releases.saved[0].Summary, "p(95)<500") {
		t.Errorf("summary = %q", releases.saved[0].Summary)
	}
}

// The contract publishes `load` as the kind's name; the MSE Tasks write under `k6`, from the
// tool's name. Both must be read.
//
// Without that, a project following the documented convention sees nothing: the load screen
// stays empty and nothing says the directory was never opened.
func TestThePublishedFamilyDirectoryIsReadForLoad(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-2/load/panier-summary.json": summaryJSON,
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-2", JobID: 9}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(releases.saved) != 1 {
		t.Fatalf("%d campaign(s) — the directory published by the contract is not read",
			len(releases.saved))
	}
}

// The two directories coexist without doubling up: a run publishing under both names would
// produce the same campaign twice, therefore a false trend.
func TestTheTwoLoadDirectoriesDoNotDoubleUp(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-3/k6/panier-summary.json":   summaryJSON,
		"raw/load-test-3/load/panier-summary.json": summaryJSON,
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-3", JobID: 9}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(releases.saved) != 1 {
		t.Errorf("%d campaign(s) for one scenario — the history would be wrong",
			len(releases.saved))
	}
}
