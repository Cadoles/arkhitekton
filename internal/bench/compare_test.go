package bench

import "testing"

func summaryOf(measures ...Measure) Summary {
	return Summary{Scenario: "panier", Measures: measures}
}

func latency(value float64) Measure {
	return Measure{Metric: "http_req_duration:p(95)", Label: "Latence p95", Unit: "ms",
		Value: value, LowerBetter: true}
}

func throughput(value float64) Measure {
	return Measure{Metric: "http_reqs:rate", Label: "Débit", Unit: "req/s", Value: value}
}

func volume(value float64) Measure {
	return Measure{Metric: "http_reqs:count", Label: "Requêtes", Unit: "req",
		Value: value, Volume: true}
}

var exclusive = Conditions{Exclusive: true, ConcurrentJobs: 0}

// Le sens de la comparaison dépend de la métrique : une latence baisse, un débit
// monte. Les confondre ferait afficher « conforme » sur une régression.
func TestVerdictFollowsTheDirectionOfTheMetric(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300, "http_reqs:rate": 400}

	comparisons := Compare(summaryOf(latency(250), throughput(500)), baselines, exclusive)
	for _, comparison := range comparisons {
		if comparison.Verdict != VerdictPass {
			t.Errorf("%s = %s, attendu pass", comparison.Measure.Metric, comparison.Verdict)
		}
	}

	comparisons = Compare(summaryOf(latency(400), throughput(300)), baselines, exclusive)
	for _, comparison := range comparisons {
		if comparison.Verdict != VerdictFail {
			t.Errorf("%s = %s, attendu fail", comparison.Measure.Metric, comparison.Verdict)
		}
	}
}

func TestTenPercentTolerance(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300}

	// 315 ms contre 300 : ratio 0,952, dans la tolérance.
	if verdict := Compare(summaryOf(latency(315)), baselines, exclusive)[0].Verdict; verdict != VerdictWarn {
		t.Errorf("verdict = %s, attendu warn", verdict)
	}
	// 340 ms : ratio 0,882, au-delà.
	if verdict := Compare(summaryOf(latency(340)), baselines, exclusive)[0].Verdict; verdict != VerdictFail {
		t.Errorf("verdict = %s, attendu fail", verdict)
	}
}

// Le cœur de B16. Une mesure prise pendant un build ne se compare pas : la
// juger imputerait à la release une lenteur causée par un voisin.
func TestNonExclusiveMeasuresAreNotJudged(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300}
	conditions := Conditions{Exclusive: false, ConcurrentJobs: 3}

	comparisons := Compare(summaryOf(latency(900)), baselines, conditions)
	if len(comparisons) != 1 {
		t.Fatalf("%d comparaison(s)", len(comparisons))
	}
	comparison := comparisons[0]

	if comparison.Verdict != VerdictNA {
		t.Errorf("verdict = %s: une mesure non comparable ne doit pas être jugée", comparison.Verdict)
	}
	// Mais elle reste affichée : la cacher ferait croire que la campagne n'a pas
	// eu lieu.
	if comparison.Measure.Value != 900 {
		t.Error("la mesure doit rester disponible")
	}
	if comparison.Comparable {
		t.Error("la mesure est marquée comparable à tort")
	}
	// Et le motif est dit : sans lui, l'absence de verdict se lit comme une panne.
	if comparison.Reason == "" {
		t.Error("le motif du refus de comparaison doit être explicite")
	}
	// La baseline reste rendue : le lecteur doit pouvoir constater l'écart, même
	// si le dashboard refuse d'en tirer un verdict.
	if comparison.Baseline == nil || *comparison.Baseline != 300 {
		t.Errorf("baseline = %v", comparison.Baseline)
	}
}

// La barrière draine avant de lancer : exclusivité et concurrence nulle
// devraient coïncider. Si elles divergent, c'est le constat qui fait foi.
func TestObservedConcurrencyOverridesTheIntent(t *testing.T) {
	conditions := Conditions{Exclusive: true, ConcurrentJobs: 2}
	if conditions.Comparable() {
		t.Error("des runs constatés au démarrage rendent la mesure non comparable")
	}

	comparisons := Compare(summaryOf(latency(250)),
		map[string]float64{"http_req_duration:p(95)": 300}, conditions)
	if comparisons[0].Verdict != VerdictNA {
		t.Errorf("verdict = %s", comparisons[0].Verdict)
	}
}

// Un débit instantané conforme obtenu en traitant moins de requêtes n'est pas un
// succès.
func TestVolumeDegradesTheOtherVerdicts(t *testing.T) {
	baselines := map[string]float64{"http_reqs:rate": 400, "http_reqs:count": 24000}

	// Débit à parité, mais moitié moins de requêtes traitées.
	comparisons := Compare(summaryOf(throughput(400), volume(12000)), baselines, exclusive)

	for _, comparison := range comparisons {
		if comparison.Measure.Metric == "http_reqs:rate" && comparison.Verdict != VerdictFail {
			t.Errorf("débit = %s, attendu dégradé par le volume", comparison.Verdict)
		}
	}
}

// Dans l'autre sens : un volume excellent ne rachète pas un débit effondré.
func TestVolumeNeverImprovesAVerdict(t *testing.T) {
	baselines := map[string]float64{"http_reqs:rate": 400, "http_reqs:count": 24000}

	comparisons := Compare(summaryOf(throughput(100), volume(48000)), baselines, exclusive)
	for _, comparison := range comparisons {
		if comparison.Measure.Metric == "http_reqs:rate" && comparison.Verdict != VerdictFail {
			t.Errorf("débit = %s, attendu fail", comparison.Verdict)
		}
	}
}

// Une première campagne n'a pas de baseline. Ce n'est pas un échec : c'est une
// mesure de référence en puissance.
func TestAMeasureWithoutBaselineIsNotJudged(t *testing.T) {
	comparisons := Compare(summaryOf(latency(250)), nil, exclusive)
	if comparisons[0].Verdict != VerdictNA {
		t.Errorf("verdict = %s, attendu na", comparisons[0].Verdict)
	}
	if comparisons[0].Baseline != nil {
		t.Error("aucune baseline ne doit être inventée")
	}
}

// Le signal de performance vaut le point le plus faible : une moyenne noierait
// une régression dans la conformité des autres mesures.
func TestWorstDrivesTheSignal(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300, "http_reqs:rate": 400}

	comparisons := Compare(summaryOf(latency(500), throughput(500)), baselines, exclusive)
	if worst := Worst(comparisons); worst != VerdictFail {
		t.Errorf("verdict d'ensemble = %s, attendu fail", worst)
	}
}

func TestZeroBaselineDoesNotProduceInfinity(t *testing.T) {
	comparisons := Compare(summaryOf(latency(0)),
		map[string]float64{"http_req_duration:p(95)": 0}, exclusive)
	if comparisons[0].Verdict != VerdictPass {
		t.Errorf("verdict = %s", comparisons[0].Verdict)
	}
}
