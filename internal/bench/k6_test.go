package bench

import (
	"errors"
	"testing"
)

// The modern form: `handleSummary` files the statistics under `values` and adds the thresholds.
const modernSummary = `{
  "metrics": {
    "http_req_duration": {
      "type": "trend", "contains": "time",
      "values": {"avg": 120.5, "med": 98.0, "p(95)": 310.2, "max": 900.0},
      "thresholds": {"p(95)<500": {"ok": true}}
    },
    "http_reqs": {"type": "counter", "values": {"count": 24000, "rate": 400.0}},
    "http_req_failed": {
      "type": "rate", "values": {"rate": 0.012},
      "thresholds": {"rate<0.01": {"ok": false}}
    },
    "vus_max": {"type": "gauge", "values": {"value": 200}}
  }
}`

// The flat form: `--summary-export`, still used by Tasks written two years ago. Refusing it
// would fail the collection over a detail of invocation.
const legacySummary = `{
  "metrics": {
    "http_req_duration": {"avg": 120.5, "med": 98.0, "p(95)": 310.2},
    "http_reqs": {"count": 24000, "rate": 400.0},
    "vus_max": {"max": 200}
  }
}`

func TestParseSummaryReadsBothShapes(t *testing.T) {
	for name, raw := range map[string]string{"moderne": modernSummary, "plate": legacySummary} {
		t.Run(name, func(t *testing.T) {
			summary, err := ParseSummary("panier", []byte(raw), nil)
			if err != nil {
				t.Fatalf("lecture: %v", err)
			}
			if summary.VUs != 200 {
				t.Errorf("VUs = %d, expected 200", summary.VUs)
			}

			values := map[string]float64{}
			for _, measure := range summary.Measures {
				values[measure.Metric] = measure.Value
			}
			if values["http_req_duration:p(95)"] != 310.2 {
				t.Errorf("p95 = %v", values["http_req_duration:p(95)"])
			}
			if values["http_reqs:rate"] != 400 {
				t.Errorf("throughput = %v", values["http_reqs:rate"])
			}
		})
	}
}

// A k6 rate is 0..1. Displaying it as it is would give "0.012%" where the scenario fails once in
// a hundred.
func TestFailureRateIsRenderedAsAPercentage(t *testing.T) {
	summary, err := ParseSummary("panier", []byte(modernSummary), nil)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	for _, measure := range summary.Measures {
		if measure.Metric == "http_req_failed:rate" {
			if measure.Value != 1.2 {
				t.Errorf("failure rate = %v, expected 1.2 %%", measure.Value)
			}
			return
		}
	}
	t.Error("failure rate missing")
}

// k6 gives its own verdict on the declared thresholds. It says "the contract is met" where the
// baseline says "it is as good as before": the two can contradict each other, and losing the
// first would lose the most direct piece of information.
func TestThresholdVerdictsAreKept(t *testing.T) {
	summary, err := ParseSummary("panier", []byte(modernSummary), nil)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(summary.ThresholdsFailed) != 1 || summary.ThresholdsFailed[0] != "http_req_failed: rate<0.01" {
		t.Errorf("failed thresholds = %v", summary.ThresholdsFailed)
	}

	for _, measure := range summary.Measures {
		if measure.Metric == "http_req_duration:p(95)" {
			if measure.ThresholdOK == nil || !*measure.ThresholdOK {
				t.Errorf("latency threshold = %v, expected met", measure.ThresholdOK)
			}
		}
	}
}

// A scenario with no HTTP request is legitimate. Omitting the measurement is accurate; a zero
// would be wrong.
func TestAbsentMetricsAreOmittedNotZeroed(t *testing.T) {
	summary, err := ParseSummary("sonde", []byte(`{"metrics":{"http_reqs":{"values":{"count":10,"rate":1}}}}`), nil)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	for _, measure := range summary.Measures {
		if measure.Metric == "http_req_duration:p(95)" {
			t.Error("a missing metric must not appear")
		}
	}
}

func TestParseSummaryRejectsUnusableInput(t *testing.T) {
	if _, err := ParseSummary("panier", []byte("not json"), nil); err == nil {
		t.Error("an unreadable summary must be refused")
	}

	var empty ErrNoMetrics
	_, err := ParseSummary("panier", []byte(`{"metrics":{}}`), nil)
	if !errors.As(err, &empty) {
		t.Errorf("error = %v, expected ErrNoMetrics", err)
	}
}

func TestScenarioFromKey(t *testing.T) {
	cases := map[string]string{
		"raw/load-test-42/k6/panier-summary.json": "panier",
		"raw/load-test-42/k6/parcours.json":       "parcours",
		"":                                        "default",
	}
	for key, expected := range cases {
		if got := ScenarioFromKey(key); got != expected {
			t.Errorf("ScenarioFromKey(%q) = %q, expected %q", key, got, expected)
		}
	}
}
