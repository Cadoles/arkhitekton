package http

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The score, one tile per pipeline launched.
 *
 * What these tests hold: a tile appears when its pipeline is first launched and stays one when it
 * is launched again; it weighs what was set for it, otherwise its nature, otherwise nothing.
 */

type tileStore struct {
	jobs    []store.ReleaseJob
	audits  map[string]store.SecurityAudit
	weights map[string]float64
}

func (s *tileStore) ListJobsIn(context.Context, store.Scope) ([]store.ReleaseJob, error) {
	return s.jobs, nil
}

func (s *tileStore) LatestFindingsOf(_ context.Context, _ store.Scope, capability string) (store.SecurityAudit, error) {
	if audit, found := s.audits[capability]; found {
		return audit, nil
	}
	return store.SecurityAudit{}, store.ErrAuditNotFound
}

func (s *tileStore) ListLoadResults(context.Context, store.Scope) ([]store.LoadResult, error) {
	return nil, nil
}

func (s *tileStore) ListResultMeasures(context.Context, int64) ([]store.BenchMeasure, error) {
	return nil, nil
}

func (s *tileStore) Baselines(context.Context, string) (map[string]store.Baseline, error) {
	return nil, nil
}

func (s *tileStore) ScoreWeights(context.Context) (map[string]float64, error) { return s.weights, nil }

func tileCatalogue() *catalog.Catalog {
	pipeline := func(kind, label, group string) catalog.Pipeline {
		return catalog.Pipeline{Kind: kind, Label: label, PipelineRef: kind,
			Produces: []catalog.Product{catalog.ProductResults},
			Uploads:  map[string]catalog.Upload{"results": {Group: group}}}
	}
	return &catalog.Catalog{Pipelines: []catalog.Pipeline{
		pipeline("php/unit-tests", "Tests unitaires", "unit"),
		pipeline("mstore/e2e-tests", "Tests E2E", "e2e"),
		pipeline("docker/scan", "Scan d'image", "security"),
		pipeline("mstore/security-audit", "Audit de sécurité", "security"),
		pipeline("mstore/rgaa-audit", "Audit RGAA", "accessibility"),
		pipeline("review/pull-request", "Revue IA", "review"),
		{Kind: "docker/build", PipelineRef: "build", Produces: []catalog.Product{catalog.ProductImage}},
	}}
}

var origin = time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

func job(kind string, minute int, state string) store.ReleaseJob {
	return store.ReleaseJob{Kind: kind, State: state, RequestedAt: origin.Add(time.Duration(minute) * time.Minute)}
}

func result(kind, group, tool string, minute, total, failed int) store.TestResult {
	return store.TestResult{Capability: kind, Group: group, Tool: tool, Shape: "cases",
		Total: total, Passed: total - failed, Failed: failed, CreatedAt: origin.Add(time.Duration(minute) * time.Minute)}
}

func score(t *testing.T, source *tileStore, reports []store.TestResult) (keys []string, labels []string, value int, measured bool) {
	t.Helper()
	breakdown, err := computeScore(context.Background(), source, store.ReleaseScope(1), reports, tileCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range breakdown.Signals {
		keys = append(keys, signal.Key)
		labels = append(labels, signal.Label)
	}
	return keys, labels, breakdown.Score, breakdown.Measured
}

// Nothing launched, nothing shown: no tile, no score.
func TestNothingLaunchedHasNoTile(t *testing.T) {
	keys, _, _, measured := score(t, &tileStore{}, nil)
	if len(keys) != 0 || measured {
		t.Errorf("tiles = %v, measured = %v", keys, measured)
	}
}

// Each pipeline launched adds its tile in launch order; a build, a review and a pipeline launched
// again add none.
func TestEachPipelineLaunchedAddsOneTile(t *testing.T) {
	source := &tileStore{jobs: []store.ReleaseJob{
		job("mstore/rgaa-audit", 9, "running"),
		job("php/unit-tests", 5, "succeeded"),
		job("review/pull-request", 3, "succeeded"),
		job("php/unit-tests", 1, "failed"),
		job("docker/build", 0, "succeeded"),
	}}
	reports := []store.TestResult{
		// Newest first: the second run of the unit tests stands for the tile.
		result("php/unit-tests", "unit", "phpunit", 6, 10, 0),
		result("php/unit-tests", "unit", "phpunit", 2, 10, 5),
	}
	keys, labels, value, measured := score(t, source, reports)
	if len(keys) != 2 || keys[0] != "pipeline:php/unit-tests" || keys[1] != "pipeline:mstore/rgaa-audit" {
		t.Fatalf("tiles = %v", keys)
	}
	if labels[1] != "Audit RGAA" {
		t.Errorf("labels = %v", labels)
	}
	// The latest unit run passes entirely; the RGAA audit, running, is not measured yet.
	if value != 100 || !measured {
		t.Errorf("score = %d, measured = %v", value, measured)
	}
}

// A nature the score knows weighs its default; one it does not is out of the score, shown all the
// same; a weight set wins over both.
func TestATileWeighsWhatWasSetForItsPipeline(t *testing.T) {
	reports := []store.TestResult{
		result("mstore/rgaa-audit", "accessibility", "pa11y", 3, 10, 10),
		result("php/unit-tests", "unit", "phpunit", 2, 10, 0),
	}
	source := &tileStore{}
	breakdown, err := computeScore(context.Background(), source, store.ReleaseScope(1), reports, tileCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.Score != 100 || len(breakdown.Signals) != 2 {
		t.Fatalf("out of the score, the audit still counted: %+v", breakdown)
	}
	if rgaa := breakdown.Signals[1]; !rgaa.Measured || rgaa.Value != 0 || rgaa.Declared != 0 {
		t.Errorf("an out-of-score tile keeps its value: %+v", rgaa)
	}

	source.weights = map[string]float64{"mstore/rgaa-audit": 25}
	if breakdown, _ = computeScore(context.Background(), source, store.ReleaseScope(1), reports, tileCatalogue()); breakdown.Score != 50 {
		t.Errorf("weighted alike, a failed audit halves the score: %d", breakdown.Score)
	}

	source.weights = map[string]float64{"php/unit-tests": 0}
	if breakdown, _ = computeScore(context.Background(), source, store.ReleaseScope(1), reports, tileCatalogue()); breakdown.Measured {
		t.Errorf("nothing weighs anything, yet a score: %+v", breakdown)
	}
}

// Two pipelines auditing into one group are each graded on their own audit.
func TestTwoSecurityPipelinesReadTheirOwnAudit(t *testing.T) {
	reports := []store.TestResult{
		{Capability: "mstore/security-audit", Group: "security", Shape: "findings", Tool: "security-audit", CreatedAt: origin.Add(2 * time.Minute)},
		{Capability: "docker/scan", Group: "security", Shape: "findings", Tool: "trivy", CreatedAt: origin},
	}
	source := &tileStore{audits: map[string]store.SecurityAudit{
		"docker/scan":        {Critical: 0, High: 0, Total: 1},
		"mstore/security-audit": {Critical: 2, Total: 2},
	}}
	breakdown, err := computeScore(context.Background(), source, store.ReleaseScope(1), reports, tileCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	if len(breakdown.Signals) != 2 || breakdown.Signals[0].Value != 100 || breakdown.Signals[1].Value != 0 {
		t.Errorf("signals = %+v", breakdown.Signals)
	}
}

// A result recorded before results named their pipeline keeps a tile, by its group.
func TestAResultNamingNoPipelineKeepsATileByGroup(t *testing.T) {
	reports := []store.TestResult{{Group: "unit", Tool: "phpunit", Total: 4, Passed: 4, CreatedAt: origin}}
	_, labels, value, _ := score(t, &tileStore{}, reports)
	if len(labels) != 1 || labels[0] != "Tests unitaires" || value != 100 {
		t.Errorf("labels = %v, score = %d", labels, value)
	}
}
