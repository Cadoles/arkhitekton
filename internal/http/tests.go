package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/bench"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// TestStore est ce que les handlers de résultats attendent de la base.
type TestStore interface {
	GetRelease(ctx context.Context, tag string) (store.Release, error)
	ListReleaseTestResults(ctx context.Context, releaseID int64) ([]store.TestResult, error)
	ListTestCases(ctx context.Context, resultID int64, onlyFailures bool) ([]store.TestCase, error)
	DiffFamily(ctx context.Context, releaseID, previousID int64, family string) ([]store.CaseDiff, error)
	PreviousRelease(ctx context.Context, releaseID int64) (int64, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
}

// TestsHandler expose les résultats de tests et le score de qualité.
type TestsHandler struct {
	store TestStore
	// weights vient de la configuration. La pondération du score a été acceptée
	// par le client et reste ajustable : la coder ici obligerait à redéployer pour
	// la changer, ce qui reviendrait à la figer (B11).
	weights results.Weights
}

// NewTestsHandler assemble le handler. La pondération est reçue sous forme
// générique — elle vient d'un YAML — et convertie ici : c'est la frontière entre
// la configuration et la règle de calcul.
func NewTestsHandler(tests TestStore, weights map[string]float64) *TestsHandler {
	converted := results.Weights{}
	for signal, weight := range weights {
		converted[results.Signal(signal)] = weight
	}
	if len(converted) == 0 {
		converted = results.DefaultWeights()
	}
	return &TestsHandler{store: tests, weights: converted}
}

type testResultPayload struct {
	ID         int64     `json:"id"`
	Family     string    `json:"family"`
	Tool       string    `json:"tool"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Flaky      int       `json:"flaky"`
	DurationMS int64     `json:"duration_ms"`
	Summary    string    `json:"summary,omitempty"`
	MeasuredAt time.Time `json:"measured_at"`
}

type signalPayload struct {
	Signal string  `json:"signal"`
	Value  float64 `json:"value"`
	// Weight est le poids **effectif**, après redistribution des signaux non
	// mesurés. C'est lui qui explique la contribution réelle au score.
	Weight   float64 `json:"weight"`
	Measured bool    `json:"measured"`
	Detail   string  `json:"detail,omitempty"`
}

// Tests rend les rapports d'une release et le score qu'ils composent.
func (h *TestsHandler) Tests(w http.ResponseWriter, r *http.Request) {
	release, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	reports, err := h.store.ListReleaseTestResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des résultats impossible", "")
		return
	}

	payload := make([]testResultPayload, 0, len(reports))
	for _, report := range reports {
		payload = append(payload, testResultPayload{
			ID: report.ID, Family: report.Family, Tool: report.Tool, Status: report.Status,
			Total: report.Total, Passed: report.Passed, Failed: report.Failed,
			Skipped: report.Skipped, Flaky: report.Flaky, DurationMS: report.DurationMS,
			Summary: report.Summary, MeasuredAt: report.CreatedAt,
		})
	}

	breakdown, err := h.score(r.Context(), release.ID, reports)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Calcul du score impossible", "")
		return
	}

	signals := make([]signalPayload, 0, len(breakdown.Signals))
	for _, signal := range breakdown.Signals {
		signals = append(signals, signalPayload{
			Signal: string(signal.Signal), Value: signal.Value, Weight: signal.Weight,
			Measured: signal.Measured, Detail: signal.Detail,
		})
	}

	redistributed := make([]string, 0, len(breakdown.Redistributed))
	for _, signal := range breakdown.Redistributed {
		redistributed = append(redistributed, string(signal))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": payload,
		"score": map[string]any{
			"value":    breakdown.Score,
			"measured": breakdown.Measured,
			"health":   results.Health(breakdown),
			"signals":  signals,
			// Le lecteur doit savoir sur quoi porte le score qu'il lit : un 100
			// sur trois signaux n'est pas un 100 sur six.
			"redistributed": redistributed,
		},
	})
}

// score compose le score d'une release à partir de ce qui a été mesuré.
//
// Chaque signal est cherché explicitement plutôt que dérivé d'une boucle sur les
// rapports : un signal absent doit produire une mesure « non exécutée », et une
// boucle sur ce qui existe ne produirait rien du tout — donc un score calculé sur
// un sous-ensemble silencieux.
func (h *TestsHandler) score(
	ctx context.Context, releaseID int64, reports []store.TestResult,
) (results.Breakdown, error) {
	byFamily := map[string]results.Report{}
	seen := map[string]bool{}

	for _, report := range reports {
		// Le rapport le plus récent d'une famille fait foi : la liste est
		// antichronologique, donc le premier rencontré.
		if seen[report.Family] {
			continue
		}
		seen[report.Family] = true
		byFamily[report.Family] = results.Report{
			Total: report.Total, Passed: report.Passed, Failed: report.Failed,
			Skipped: report.Skipped, Flaky: report.Flaky, Status: report.Status,
		}
	}

	// Les violations d'analyse statique se comptent en cas, pas en tests réussis :
	// la règle de score n'est pas la même.
	staticViolations, staticRan := 0, seen[string(results.FamilyStatic)]
	if staticRan {
		staticViolations = byFamily[string(results.FamilyStatic)].Failed
	}

	measures := []results.Measure{
		results.FromReport(results.SignalUnit,
			byFamily[string(results.FamilyUnit)], seen[string(results.FamilyUnit)]),
		results.FromReport(results.SignalIntegration,
			byFamily[string(results.FamilyIntegration)], seen[string(results.FamilyIntegration)]),
		results.FromReport(results.SignalE2E,
			byFamily[string(results.FamilyE2E)], seen[string(results.FamilyE2E)]),
		results.FromStatic(staticViolations, staticRan),
		// La sécurité attend la normalisation des findings : la déclarer non
		// mesurée est exact, et son poids est redistribué.
		results.FromSecurity(0, 0, false),
	}

	performance, err := h.benchMeasure(ctx, releaseID)
	if err != nil {
		return results.Breakdown{}, err
	}
	measures = append(measures, performance)

	return results.Score(measures, h.weights), nil
}

// benchMeasure rend le signal de performance de la campagne la plus récente.
func (h *TestsHandler) benchMeasure(ctx context.Context, releaseID int64) (results.Measure, error) {
	campaigns, err := h.store.ListReleaseLoadResults(ctx, releaseID)
	if err != nil {
		return results.Measure{}, err
	}
	if len(campaigns) == 0 {
		return results.FromBench("", false), nil
	}

	campaign := campaigns[0]
	conditions := bench.Conditions{
		Exclusive: campaign.Exclusive, ConcurrentJobs: campaign.ConcurrentJobs,
	}
	if !conditions.Comparable() {
		return results.FromBench("", false), nil
	}

	measures, err := h.store.ListResultMeasures(ctx, campaign.ID)
	if err != nil {
		return results.Measure{}, err
	}
	baselines, err := h.store.Baselines(ctx, campaign.Scenario)
	if err != nil {
		return results.Measure{}, err
	}

	summary := bench.Summary{Scenario: campaign.Scenario}
	values := map[string]float64{}
	for _, measure := range measures {
		summary.Measures = append(summary.Measures, bench.Measure{
			Metric: measure.Metric, Value: measure.Value,
			LowerBetter: measure.LowerBetter, Volume: measure.Volume,
		})
	}
	for metric, baseline := range baselines {
		values[metric] = baseline.Value
	}

	verdict := bench.Worst(bench.Compare(summary, values, conditions))
	return results.FromBench(string(verdict), true), nil
}

type caseDiffPayload struct {
	Suite  string `json:"suite"`
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	Was    string `json:"was,omitempty"`
	Change string `json:"change"`
}

type casePayload struct {
	Suite      string `json:"suite"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Trace      string `json:"trace,omitempty"`
}

// Family rend les cas d'une famille et sa comparaison à la release précédente.
//
// La comparaison est la vue qui répond à la question réellement posée en recette
// — « qu'est-ce qui a changé ? » — et non « combien de tests passent ». Un
// compteur stable peut cacher un échec résolu et un échec apparu.
func (h *TestsHandler) Family(w http.ResponseWriter, r *http.Request) {
	release, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	family := r.PathValue("family")

	reports, err := h.store.ListReleaseTestResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des résultats impossible", "")
		return
	}

	var cases []casePayload
	for _, report := range reports {
		if report.Family != family {
			continue
		}
		stored, err := h.store.ListTestCases(r.Context(), report.ID, false)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lecture des cas impossible", "")
			return
		}
		for _, testCase := range stored {
			cases = append(cases, casePayload{
				Suite: testCase.Suite, Name: testCase.Name, Status: testCase.Status,
				DurationMS: testCase.DurationMS,
				Message:    testCase.FailureMessage, Trace: testCase.FailureTrace,
			})
		}
		// Le rapport le plus récent de la famille suffit : c'est lui qui décrit
		// l'état de la release.
		break
	}

	response := map[string]any{"family": family, "cases": cases}

	previous, err := h.store.PreviousRelease(r.Context(), release.ID)
	if err == nil {
		diffs, err := h.store.DiffFamily(r.Context(), release.ID, previous, family)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Comparaison impossible", "")
			return
		}

		changed := make([]caseDiffPayload, 0, len(diffs))
		for _, diff := range diffs {
			// `unchanged` est le gros du volume et n'apprend rien : la vue
			// existe pour ce qui a bougé.
			if diff.Change == store.ChangeUnchanged {
				continue
			}
			changed = append(changed, caseDiffPayload{
				Suite: diff.Suite, Name: diff.Name,
				Status: diff.Status, Was: diff.Was, Change: diff.Change,
			})
		}
		response["diff"] = changed
	}
	// Pas de release précédente : pas de comparaison, et ce n'est pas une erreur.
	// La première release d'un projet n'a rien à quoi se comparer.

	writeJSON(w, http.StatusOK, response)
}
