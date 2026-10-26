package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// BenchStore is what the load handlers need from the database.
type BenchStore interface {
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	// The two ways of naming a project: by the request's path, or failing that by the one being
	// built. See internal/http/project.go.
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	ListScenarioMeasures(ctx context.Context, scenario, metric string, limit int) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
	PinBaseline(ctx context.Context, measureID int64, pinnedBy string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// BenchHandler exposes a release's load campaigns.
type BenchHandler struct{ store BenchStore }

func NewBenchHandler(benches BenchStore) *BenchHandler { return &BenchHandler{store: benches} }

// historyPoints bounds the trend. Six points: beyond that the curve stops being readable
// within a card's width.
const historyPoints = 6

type campaignPayload struct {
	ID       int64  `json:"id"`
	Scenario string `json:"scenario"`
	// Exclusive and ConcurrentJobs are the observed conditions. Returned even when all is well:
	// that is what lets the interface say a measure is comparable, rather than leaving it to be
	// guessed.
	Exclusive      bool      `json:"exclusive"`
	ConcurrentJobs int       `json:"concurrent_jobs"`
	Comparable     bool      `json:"comparable"`
	Reason         string    `json:"reason,omitempty"`
	DurationMS     int64     `json:"duration_ms"`
	Summary        string    `json:"summary,omitempty"`
	MeasuredAt     time.Time `json:"measured_at"`
	// Verdict is the campaign's weakest point: an average would drown a regression in the other
	// measures' compliance.
	Verdict  string               `json:"verdict"`
	Measures []measurePayload     `json:"measures"`
	History  map[string][]float64 `json:"history,omitempty"`
}

type measurePayload struct {
	ID          int64    `json:"id"`
	Metric      string   `json:"metric"`
	Label       string   `json:"label"`
	Unit        string   `json:"unit"`
	Value       float64  `json:"value"`
	LowerBetter bool     `json:"lower_better"`
	Volume      bool     `json:"volume"`
	Baseline    *float64 `json:"baseline,omitempty"`
	// BaselineAt dates the reference: a baseline ages, and knowing when it dates from is part of
	// the verdict.
	BaselineAt *time.Time `json:"baseline_at,omitempty"`
	Verdict    string     `json:"verdict"`
	Ratio      float64    `json:"ratio,omitempty"`
	// ThresholdOK is the verdict the load tool returns on its own thresholds. It says "the
	// contract is met" where the baseline says "it is as good as before": the two can
	// contradict each other, and the interface must be able to show both.
	ThresholdOK *bool `json:"threshold_ok,omitempty"`
}

// Bench returns a release's load campaigns, compared with their baselines.
func (h *BenchHandler) Bench(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	results, err := h.store.ListReleaseLoadResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the campaigns", "")
		return
	}

	payload := make([]campaignPayload, 0, len(results))
	for _, result := range results {
		campaign, err := h.buildCampaign(r.Context(), result)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the measurements", "")
			return
		}
		payload = append(payload, campaign)
	}

	writeJSON(w, http.StatusOK, map[string]any{"campaigns": payload})
}

func (h *BenchHandler) buildCampaign(ctx context.Context, result store.LoadResult) (campaignPayload, error) {
	measures, err := h.store.ListResultMeasures(ctx, result.ID)
	if err != nil {
		return campaignPayload{}, err
	}

	baselines, err := h.store.Baselines(ctx, result.Scenario)
	if err != nil {
		return campaignPayload{}, err
	}

	// The comparison is made by internal/bench, a pure package: the rule that sets aside
	// non-exclusive measures is a decision, not presentation, and it must be testable without
	// standing up a database.
	summary := bench.Summary{Scenario: result.Scenario}
	values := map[string]float64{}
	for _, measure := range measures {
		summary.Measures = append(summary.Measures, bench.Measure{
			Metric: measure.Metric, Label: measure.Label, Unit: measure.Unit,
			Value: measure.Value, LowerBetter: measure.LowerBetter,
			Volume: measure.Volume, ThresholdOK: measure.ThresholdOK,
		})
	}
	for metric, baseline := range baselines {
		values[metric] = baseline.Value
	}

	conditions := bench.Conditions{Exclusive: result.Exclusive, ConcurrentJobs: result.ConcurrentJobs}
	comparisons := bench.Compare(summary, values, conditions)

	campaign := campaignPayload{
		ID: result.ID, Scenario: result.Scenario,
		Exclusive: result.Exclusive, ConcurrentJobs: result.ConcurrentJobs,
		Comparable: conditions.Comparable(), Reason: conditions.Why(),
		DurationMS: result.DurationMS, Summary: result.Summary,
		MeasuredAt: result.CreatedAt,
		Verdict:    string(bench.Worst(comparisons)),
		History:    map[string][]float64{},
	}

	for index, comparison := range comparisons {
		entry := measurePayload{
			ID:     measures[index].ID,
			Metric: comparison.Measure.Metric, Label: comparison.Measure.Label,
			Unit: comparison.Measure.Unit, Value: comparison.Measure.Value,
			LowerBetter: comparison.Measure.LowerBetter, Volume: comparison.Measure.Volume,
			Baseline: comparison.Baseline, Verdict: string(comparison.Verdict),
			Ratio: comparison.Ratio, ThresholdOK: comparison.Measure.ThresholdOK,
		}
		if baseline, known := baselines[comparison.Measure.Metric]; known {
			at := baseline.MeasuredAt
			entry.BaselineAt = &at
		}
		campaign.Measures = append(campaign.Measures, entry)

		history, err := h.store.ListScenarioMeasures(ctx,
			result.Scenario, comparison.Measure.Metric, historyPoints)
		if err != nil {
			return campaignPayload{}, err
		}
		// Only comparable measures enter the trend. A curve mixing different conditions is an
		// invented trend, and the sparkline would present it as a fact.
		var points []float64
		for index := len(history) - 1; index >= 0; index-- {
			point := history[index]
			if !(bench.Conditions{Exclusive: point.Exclusive, ConcurrentJobs: point.ConcurrentAt}).Comparable() {
				continue
			}
			points = append(points, point.Value)
		}
		if len(points) > 1 {
			campaign.History[comparison.Measure.Metric] = points
		}
	}

	return campaign, nil
}

// PinBaseline designates a measure as the reference.
//
// The gesture moves the reference of every later verdict: it requires the role that commits
// delivery, and it is recorded.
func (h *BenchHandler) PinBaseline(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("measure"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
		return
	}

	if err := h.store.PinBaseline(r.Context(), id, identity.Subject); err != nil {
		if errors.Is(err, store.ErrBaselineNotFound) {
			writeProblem(w, http.StatusNotFound, "Measure not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot pin", "")
		return
	}

	if err := h.store.Audit(r.Context(), identity.Subject, store.AuditBaselinePin,
		r.PathValue("tag"), map[string]any{"measure_id": id}); err != nil {
		// A record and not a transaction: the baseline is pinned.
		writeJSON(w, http.StatusOK, map[string]any{"measure_id": id})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"measure_id": id})
}
