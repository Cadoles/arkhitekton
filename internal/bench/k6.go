// Package bench reads load-test results and makes them comparable.
//
// Two responsibilities, and the second is the less obvious.
//
// Reading a k6 summary is mechanical. **Deciding whether a measurement is comparable** is not:
// the cluster hosting Tekton is also that of the load-test environment. A build, a Trivy audit
// or an E2E campaign running during the measurement skews it as much as the generator would have
// — it is a problem of neighbourhood, not of generation, and moving the runner out of the
// cluster only half solves it.
//
// The consequence is direct: a trend curve over six points whose conditions vary means nothing.
// This package therefore carries the rule that makes the comparison honest — only the
// measurements taken under exclusivity enter the comparison with the baseline; the others are
// kept, displayed, and marked as not comparable.
//
// Everything here is pure: no database, no cluster, no network.
package bench

import (
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
)

// Metric names a k6 metric and how to read it.
type Metric struct {
	// Name is the k6 metric's name (`http_req_duration`).
	Name string
	// Stat is the statistic kept (`p(95)`, `rate`, `count`, `avg`).
	Stat string
	// Label is what is displayed.
	Label string
	Unit  string
	// LowerBetter inverts the direction of the comparison. A latency goes down, a throughput goes
	// up: confusing the two displays "conforming" on a regression.
	LowerBetter bool
	// Volume marks the total-volume metric, which may degrade another's verdict without ever
	// improving it.
	Volume bool
}

// DefaultMetrics is the set kept by default.
//
// Four measurements and no more: the mock-up displays a panel, not an observability dashboard.
// Each answers a question one asks before delivering — is it slow, is it slow for the unluckiest,
// how much does it take, and does it break.
var DefaultMetrics = []Metric{
	{Name: "http_req_duration", Stat: "p(95)", Label: "Latence p95", Unit: "ms", LowerBetter: true},
	{Name: "http_req_duration", Stat: "med", Label: "Latence médiane", Unit: "ms", LowerBetter: true},
	{Name: "http_reqs", Stat: "rate", Label: "Débit", Unit: "req/s"},
	// The total volume: a conforming instantaneous throughput obtained by handling fewer requests is
	// not a success.
	{Name: "http_reqs", Stat: "count", Label: "Requêtes", Unit: "req", Volume: true},
	{Name: "http_req_failed", Stat: "rate", Label: "Taux d'échec", Unit: "%", LowerBetter: true},
}

// Measure is a readable measurement, ready to be compared.
type Measure struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`
	Value  float64
	// LowerBetter is copied from the metric: the recorded measurement must be re-readable and
	// comparable without rebuilding the metrics catalogue, which may have changed in the meantime.
	LowerBetter bool
	Volume      bool
	// ThresholdOK takes the verdict k6 itself gives on its declared thresholds. Distinct from the
	// comparison with the baseline: k6 says "the contract is met", the baseline says "it is as good
	// as before". The two can contradict each other, and ignoring it would lose the most direct
	// piece of information.
	ThresholdOK *bool
}

// Summary is a campaign's result.
type Summary struct {
	Scenario string
	Measures []Measure
	// VUs is the number of virtual users at peak. Reading context: comparing two measurements taken
	// at different loads makes no sense.
	VUs int
	// ThresholdsFailed lists the k6 thresholds not met, in reading order.
	ThresholdsFailed []string
}

// ErrNoMetrics reports a summary with no usable metric.
type ErrNoMetrics struct{ Scenario string }

func (e ErrNoMetrics) Error() string {
	return fmt.Sprintf("k6 summary of scenario %q: no usable metric", e.Scenario)
}

// ParseSummary reads a k6 summary.
//
// An expected but absent metric is not an error: a scenario may produce no HTTP request. It is
// simply omitted, and the panel will display "not measured" — which is accurate, where a zero
// would be wrong.
func ParseSummary(scenario string, raw []byte, metrics []Metric) (Summary, error) {
	if len(metrics) == 0 {
		metrics = DefaultMetrics
	}

	summary, err := parse.ParseK6(raw)
	if err != nil {
		return Summary{}, fmt.Errorf("k6 summary of scenario %q unreadable: %w", scenario, err)
	}

	result := Summary{Scenario: scenario}
	failed := map[string]bool{}

	for _, metric := range metrics {
		decoded, present := summary.Metrics[metric.Name]
		if !present {
			continue
		}
		if decoded.Err != nil {
			return Summary{}, fmt.Errorf("metric %q of scenario %q: %w", metric.Name, scenario, decoded.Err)
		}

		value, measured := decoded.Values[metric.Stat]
		if !measured {
			continue
		}

		measure := Measure{
			Metric: metric.Name + ":" + metric.Stat,
			Label:  metric.Label, Unit: metric.Unit,
			Value: value, LowerBetter: metric.LowerBetter, Volume: metric.Volume,
		}

		// A k6 rate is 0..1; displaying it as a percentage avoids a "0.02%" that reads as two hundredths
		// of a percent.
		if metric.Unit == "%" {
			measure.Value = value * 100
		}

		for name, ok := range decoded.Thresholds {
			measure.ThresholdOK = &ok
			if !ok {
				failed[metric.Name+": "+name] = true
			}
		}

		result.Measures = append(result.Measures, measure)
	}

	if vus, present := summary.Metrics["vus_max"]; present && vus.Err == nil {
		// `value` on a gauge, `max` on a series: both forms exist depending on the k6 version.
		if peak, found := vus.Values["value"]; found {
			result.VUs = int(peak)
		} else if peak, found := vus.Values["max"]; found {
			result.VUs = int(peak)
		}
	}

	for name := range failed {
		result.ThresholdsFailed = append(result.ThresholdsFailed, name)
	}
	// A stable order: an identical set of thresholds must produce the same document twice in a row,
	// otherwise any comparison of outputs is skewed by a map's iteration order.
	sort.Strings(result.ThresholdsFailed)

	if len(result.Measures) == 0 {
		return result, ErrNoMetrics{Scenario: scenario}
	}
	return result, nil
}

// ScenarioFromKey derives a scenario name from an object key.
//
// The runner publishes under a key the dashboard computed; the file name carries the scenario.
// Failing anything better, the object's name serves as the scenario name — better than an empty
// scenario, which would prevent any history.
func ScenarioFromKey(key string) string {
	name := key
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	name = strings.TrimSuffix(name, ".json")
	name = strings.TrimSuffix(name, "-summary")
	if name == "" {
		return "default"
	}
	return name
}
