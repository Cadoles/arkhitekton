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

// The direction of the comparison depends on the metric: a latency goes down, a throughput goes
// up. Confusing them would display "conforming" on a regression.
func TestVerdictFollowsTheDirectionOfTheMetric(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300, "http_reqs:rate": 400}

	comparisons := Compare(summaryOf(latency(250), throughput(500)), baselines, exclusive)
	for _, comparison := range comparisons {
		if comparison.Verdict != VerdictPass {
			t.Errorf("%s = %s, expected pass", comparison.Measure.Metric, comparison.Verdict)
		}
	}

	comparisons = Compare(summaryOf(latency(400), throughput(300)), baselines, exclusive)
	for _, comparison := range comparisons {
		if comparison.Verdict != VerdictFail {
			t.Errorf("%s = %s, expected fail", comparison.Measure.Metric, comparison.Verdict)
		}
	}
}

func TestTenPercentTolerance(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300}

	// 315 ms against 300: ratio 0.952, within the tolerance.
	if verdict := Compare(summaryOf(latency(315)), baselines, exclusive)[0].Verdict; verdict != VerdictWarn {
		t.Errorf("verdict = %s, expected warn", verdict)
	}
	// 340 ms: ratio 0.882, beyond it.
	if verdict := Compare(summaryOf(latency(340)), baselines, exclusive)[0].Verdict; verdict != VerdictFail {
		t.Errorf("verdict = %s, expected fail", verdict)
	}
}

// The heart of the matter. A measurement taken during a build does not compare: judging it would
// charge the release with a slowness caused by a neighbour.
func TestNonExclusiveMeasuresAreNotJudged(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300}
	conditions := Conditions{Exclusive: false, ConcurrentJobs: 3}

	comparisons := Compare(summaryOf(latency(900)), baselines, conditions)
	if len(comparisons) != 1 {
		t.Fatalf("%d comparaison(s)", len(comparisons))
	}
	comparison := comparisons[0]

	if comparison.Verdict != VerdictNA {
		t.Errorf("verdict = %s: a measurement that cannot be compared must not be judged", comparison.Verdict)
	}
	// But it stays displayed: hiding it would suggest the campaign did not happen.
	if comparison.Measure.Value != 900 {
		t.Error("the measurement must stay available")
	}
	if comparison.Comparable {
		t.Error("the measurement is wrongly marked comparable")
	}
	// And the reason is stated: without it, the absence of a verdict reads as a failure.
	if comparison.Reason == "" {
		t.Error("the ground for refusing the comparison must be explicit")
	}
	// The baseline is still returned: the reader must be able to see the gap, even if the dashboard
	// refuses to draw a verdict from it.
	if comparison.Baseline == nil || *comparison.Baseline != 300 {
		t.Errorf("baseline = %v", comparison.Baseline)
	}
}

// The barrier drains before launching: exclusivity and zero concurrency should coincide. If they
// diverge, it is the observation that is authoritative.
func TestObservedConcurrencyOverridesTheIntent(t *testing.T) {
	conditions := Conditions{Exclusive: true, ConcurrentJobs: 2}
	if conditions.Comparable() {
		t.Error("runs observed at start-up make the measurement not comparable")
	}

	comparisons := Compare(summaryOf(latency(250)),
		map[string]float64{"http_req_duration:p(95)": 300}, conditions)
	if comparisons[0].Verdict != VerdictNA {
		t.Errorf("verdict = %s", comparisons[0].Verdict)
	}
}

// A conforming instantaneous throughput obtained by handling fewer requests is not a success.
func TestVolumeDegradesTheOtherVerdicts(t *testing.T) {
	baselines := map[string]float64{"http_reqs:rate": 400, "http_reqs:count": 24000}

	// Throughput at parity, but half as many requests handled.
	comparisons := Compare(summaryOf(throughput(400), volume(12000)), baselines, exclusive)

	for _, comparison := range comparisons {
		if comparison.Measure.Metric == "http_reqs:rate" && comparison.Verdict != VerdictFail {
			t.Errorf("throughput = %s, expected degraded by the volume", comparison.Verdict)
		}
	}
}

// The other way round: an excellent volume does not redeem a collapsed throughput.
func TestVolumeNeverImprovesAVerdict(t *testing.T) {
	baselines := map[string]float64{"http_reqs:rate": 400, "http_reqs:count": 24000}

	comparisons := Compare(summaryOf(throughput(100), volume(48000)), baselines, exclusive)
	for _, comparison := range comparisons {
		if comparison.Measure.Metric == "http_reqs:rate" && comparison.Verdict != VerdictFail {
			t.Errorf("throughput = %s, expected fail", comparison.Verdict)
		}
	}
}

// A first campaign has no baseline. That is not a failure: it is a reference measurement in the
// making.
func TestAMeasureWithoutBaselineIsNotJudged(t *testing.T) {
	comparisons := Compare(summaryOf(latency(250)), nil, exclusive)
	if comparisons[0].Verdict != VerdictNA {
		t.Errorf("verdict = %s, expected na", comparisons[0].Verdict)
	}
	if comparisons[0].Baseline != nil {
		t.Error("no baseline must be invented")
	}
}

// The performance signal is worth the weakest point: an average would drown a regression in the
// conformity of the other measurements.
func TestWorstDrivesTheSignal(t *testing.T) {
	baselines := map[string]float64{"http_req_duration:p(95)": 300, "http_reqs:rate": 400}

	comparisons := Compare(summaryOf(latency(500), throughput(500)), baselines, exclusive)
	if worst := Worst(comparisons); worst != VerdictFail {
		t.Errorf("overall verdict = %s, expected fail", worst)
	}
}

func TestZeroBaselineDoesNotProduceInfinity(t *testing.T) {
	comparisons := Compare(summaryOf(latency(0)),
		map[string]float64{"http_req_duration:p(95)": 0}, exclusive)
	if comparisons[0].Verdict != VerdictPass {
		t.Errorf("verdict = %s", comparisons[0].Verdict)
	}
}
