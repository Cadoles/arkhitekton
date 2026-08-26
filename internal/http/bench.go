package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/bench"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// BenchStore est ce que les handlers de charge attendent de la base.
type BenchStore interface {
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	// Les deux façons de nommer un projet : par le chemin de la requête, ou à
	// défaut par celui qui se construit. Voir internal/http/project.go.
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	ListScenarioMeasures(ctx context.Context, scenario, metric string, limit int) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
	PinBaseline(ctx context.Context, measureID int64, pinnedBy string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// BenchHandler expose les campagnes de charge d'une release.
type BenchHandler struct{ store BenchStore }

func NewBenchHandler(benches BenchStore) *BenchHandler { return &BenchHandler{store: benches} }

// historyPoints borne la tendance. Six points : c'est ce qu'affiche la maquette,
// et au-delà la courbe cesse d'être lisible dans la largeur d'une carte.
const historyPoints = 6

type campaignPayload struct {
	ID       int64  `json:"id"`
	Scenario string `json:"scenario"`
	// Exclusive et ConcurrentJobs sont les conditions constatées. Rendues même
	// quand tout va bien : c'est ce qui permet à l'interface de dire qu'une
	// mesure est comparable, plutôt que de le laisser deviner.
	Exclusive      bool      `json:"exclusive"`
	ConcurrentJobs int       `json:"concurrent_jobs"`
	Comparable     bool      `json:"comparable"`
	Reason         string    `json:"reason,omitempty"`
	DurationMS     int64     `json:"duration_ms"`
	Summary        string    `json:"summary,omitempty"`
	MeasuredAt     time.Time `json:"measured_at"`
	// Verdict est le point le plus faible de la campagne : une moyenne noierait
	// une régression dans la conformité des autres mesures.
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
	// BaselineAt date la référence : une baseline vieillit, et savoir de quand
	// elle date fait partie du verdict.
	BaselineAt *time.Time `json:"baseline_at,omitempty"`
	Verdict    string     `json:"verdict"`
	Ratio      float64    `json:"ratio,omitempty"`
	// ThresholdOK est le verdict que k6 rend sur ses propres seuils. Il dit « le
	// contrat est tenu » là où la baseline dit « c'est aussi bon qu'avant » : les
	// deux peuvent se contredire, et l'interface doit pouvoir montrer les deux.
	ThresholdOK *bool `json:"threshold_ok,omitempty"`
}

// Bench rend les campagnes de charge d'une release, comparées à leurs baselines.
func (h *BenchHandler) Bench(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	results, err := h.store.ListReleaseLoadResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des campagnes impossible", "")
		return
	}

	payload := make([]campaignPayload, 0, len(results))
	for _, result := range results {
		campaign, err := h.buildCampaign(r.Context(), result)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lecture des mesures impossible", "")
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

	// La comparaison est faite par `internal/bench`, paquet pur : la règle qui
	// écarte les mesures non exclusives est une décision, pas de la présentation,
	// et elle doit être éprouvable sans monter de base (B16).
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
		// Seules les mesures comparables entrent dans la tendance. Une courbe qui
		// mêle des conditions différentes est une tendance inventée, et le
		// Sparkline la présenterait comme un fait.
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

// PinBaseline désigne une mesure comme référence.
//
// Le geste déplace la référence de tous les verdicts suivants : il exige le rôle
// qui engage la livraison, et il est consigné.
func (h *BenchHandler) PinBaseline(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("measure"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
		return
	}

	if err := h.store.PinBaseline(r.Context(), id, identity.Subject); err != nil {
		if errors.Is(err, store.ErrBaselineNotFound) {
			writeProblem(w, http.StatusNotFound, "Mesure introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Épinglage impossible", "")
		return
	}

	if err := h.store.Audit(r.Context(), identity.Subject, store.AuditBaselinePin,
		r.PathValue("tag"), map[string]any{"measure_id": id}); err != nil {
		// Trace et non transaction : la baseline est épinglée.
		writeJSON(w, http.StatusOK, map[string]any{"measure_id": id})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"measure_id": id})
}
