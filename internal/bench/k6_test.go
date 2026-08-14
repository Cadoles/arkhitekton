package bench

import (
	"errors"
	"testing"
)

// Forme moderne : `handleSummary` range les statistiques sous `values` et ajoute
// les seuils.
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

// Forme plate : `--summary-export`, encore employé par les Tasks écrites il y a
// deux ans. La refuser ferait échouer la collecte sur un détail d'invocation.
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
				t.Errorf("VUs = %d, attendu 200", summary.VUs)
			}

			values := map[string]float64{}
			for _, measure := range summary.Measures {
				values[measure.Metric] = measure.Value
			}
			if values["http_req_duration:p(95)"] != 310.2 {
				t.Errorf("p95 = %v", values["http_req_duration:p(95)"])
			}
			if values["http_reqs:rate"] != 400 {
				t.Errorf("débit = %v", values["http_reqs:rate"])
			}
		})
	}
}

// Un taux k6 vaut 0..1. L'afficher tel quel donnerait « 0.012 % » là où le
// scénario échoue une fois sur cent.
func TestFailureRateIsRenderedAsAPercentage(t *testing.T) {
	summary, err := ParseSummary("panier", []byte(modernSummary), nil)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	for _, measure := range summary.Measures {
		if measure.Metric == "http_req_failed:rate" {
			if measure.Value != 1.2 {
				t.Errorf("taux d'échec = %v, attendu 1.2 %%", measure.Value)
			}
			return
		}
	}
	t.Error("taux d'échec absent")
}

// k6 rend son propre verdict sur les seuils déclarés. Il dit « le contrat est
// tenu » là où la baseline dit « c'est aussi bon qu'avant » : les deux peuvent se
// contredire, et perdre le premier perdrait l'information la plus directe.
func TestThresholdVerdictsAreKept(t *testing.T) {
	summary, err := ParseSummary("panier", []byte(modernSummary), nil)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(summary.ThresholdsFailed) != 1 || summary.ThresholdsFailed[0] != "http_req_failed: rate<0.01" {
		t.Errorf("seuils échoués = %v", summary.ThresholdsFailed)
	}

	for _, measure := range summary.Measures {
		if measure.Metric == "http_req_duration:p(95)" {
			if measure.ThresholdOK == nil || !*measure.ThresholdOK {
				t.Errorf("seuil de latence = %v, attendu tenu", measure.ThresholdOK)
			}
		}
	}
}

// Un scénario sans requêtes HTTP est légitime. Omettre la mesure est exact ; un
// zéro serait faux.
func TestAbsentMetricsAreOmittedNotZeroed(t *testing.T) {
	summary, err := ParseSummary("sonde", []byte(`{"metrics":{"http_reqs":{"values":{"count":10,"rate":1}}}}`), nil)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	for _, measure := range summary.Measures {
		if measure.Metric == "http_req_duration:p(95)" {
			t.Error("une métrique absente ne doit pas apparaître")
		}
	}
}

func TestParseSummaryRejectsUnusableInput(t *testing.T) {
	if _, err := ParseSummary("panier", []byte("pas du json"), nil); err == nil {
		t.Error("un résumé illisible doit être refusé")
	}

	var empty ErrNoMetrics
	_, err := ParseSummary("panier", []byte(`{"metrics":{}}`), nil)
	if !errors.As(err, &empty) {
		t.Errorf("erreur = %v, attendu ErrNoMetrics", err)
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
			t.Errorf("ScenarioFromKey(%q) = %q, attendu %q", key, got, expected)
		}
	}
}
