package parse

import (
	"encoding/json"
	"fmt"
)

// K6SummaryJSON parses a k6 summary into a [K6].
const K6SummaryJSON ID = "k6-summary-json"

// K6 is a k6 summary: every metric, decoded.
type K6 struct {
	Metrics map[string]K6Metric
}

// K6Metric is one metric's statistics and thresholds.
type K6Metric struct {
	// Values maps a statistic (`p(95)`, `rate`, `count`...) to its value.
	Values map[string]float64
	// Thresholds maps a declared threshold to whether k6 says it is met.
	Thresholds map[string]bool
	// Err is set when the metric's object has neither of k6's forms. Kept per metric: one odd
	// metric must not cost the others.
	Err error
}

// k6Metric covers both forms of summary k6 produces.
//
// `--summary-export` writes the statistics flat into the metric's object; `handleSummary` files
// them under `values` and adds `type` and `thresholds`. Both exist in the wild.
type k6Metric struct {
	Values     map[string]float64 `json:"values"`
	Thresholds map[string]struct {
		OK bool `json:"ok"`
	} `json:"thresholds"`
}

type k6Summary struct {
	Metrics map[string]json.RawMessage `json:"metrics"`
}

// ParseK6 reads a k6 summary. The error is the decoder's, unwrapped.
func ParseK6(raw []byte) (K6, error) {
	var summary k6Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return K6{}, err
	}

	report := K6{Metrics: make(map[string]K6Metric, len(summary.Metrics))}
	for name, raw := range summary.Metrics {
		report.Metrics[name] = decodeK6Metric(raw)
	}
	return report, nil
}

func decodeK6Metric(raw []byte) K6Metric {
	var structured k6Metric
	if err := json.Unmarshal(raw, &structured); err == nil && structured.Values != nil {
		metric := K6Metric{Values: structured.Values}
		if structured.Thresholds != nil {
			metric.Thresholds = make(map[string]bool, len(structured.Thresholds))
			for name, threshold := range structured.Thresholds {
				metric.Thresholds[name] = threshold.OK
			}
		}
		return metric
	}

	// Flat form: the metric's object is the table of statistics.
	var flat map[string]float64
	if err := json.Unmarshal(raw, &flat); err != nil {
		return K6Metric{Err: fmt.Errorf("unrecognised metric form: %w", err)}
	}
	return K6Metric{Values: flat}
}
