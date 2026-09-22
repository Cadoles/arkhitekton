package results

import "testing"

// measured returns a measure at its signal's default weight.
//
// That is what a step declaring nothing produces: the agreed weighting stays the
// fallback, so these tests still exercise it.
func measured(signal Signal, value float64) Measure {
	return Measure{
		Key: SignalKey(signal), Signal: signal, Weight: WeightOf(nil, signal),
		Value: value, Measured: true,
	}
}

// absent returns a measure that did not run, at its signal's default weight.
func absent(signal Signal) Measure {
	return Measure{
		Key: SignalKey(signal), Signal: signal, Weight: WeightOf(nil, signal),
		Detail: "not run",
	}
}

func TestScoreIsWeighted(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 100),
		measured(SignalIntegration, 100),
		measured(SignalE2E, 100),
		measured(SignalCode, 100),
		measured(SignalSecurity, 100),
		measured(SignalPerformance, 100),
	})

	if breakdown.Score != 100 || !breakdown.Measured {
		t.Errorf("score = %d", breakdown.Score)
	}
}

// A 100 over three measured signals must not look like a 100 over six: the same
// number for two realities that do not support the same decision.
func TestUnmeasuredSignalsAreRedistributedAndNamed(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 100),
		measured(SignalIntegration, 100),
		absent(SignalE2E),
		absent(SignalCode),
		absent(SignalSecurity),
		absent(SignalPerformance),
	})

	if breakdown.Score != 100 {
		t.Errorf("score = %d, expected 100 over what was measured", breakdown.Score)
	}
	if len(breakdown.Redistributed) != 4 {
		t.Errorf("redistributed = %v", breakdown.Redistributed)
	}

	// The effective weight is the signal's share of what was measured: 25 and 20 out of
	// a total of 45.
	for _, signal := range breakdown.Signals {
		if signal.Signal == SignalUnit && int(signal.Weight) != 55 {
			t.Errorf("effective weight of unit tests = %.1f, expected ~55", signal.Weight)
		}
	}
}

// Returning zero would be a judgement, and the harshest possible, on a release
// nobody evaluated.
func TestNothingMeasuredIsNotAZero(t *testing.T) {
	breakdown := Score([]Measure{
		absent(SignalUnit), absent(SignalSecurity),
	})

	if breakdown.Measured {
		t.Error("an unmeasured score must not declare itself measured")
	}
	if Health(breakdown) != "na" {
		t.Errorf("health = %q, expected na", Health(breakdown))
	}
}

// A flaky test costs on every run without ever blocking: not counting it would say
// it costs nothing.
func TestFlakyCountsAsHalfAFailure(t *testing.T) {
	report := Report{Total: 10, Passed: 8, Failed: 1, Flaky: 1}

	measure := FromReport(SignalUnit, report, true)
	// 1 failure + 0.5 flaky out of 10 → 85.
	if measure.Value != 85 {
		t.Errorf("value = %.1f, expected 85", measure.Value)
	}
	if measure.Detail == "" {
		t.Error("the breakdown must accompany the value")
	}
}

// "Measured at zero" and "not measured" both display zero. Confusing them would sink
// the score of a release whose pipeline simply did not run.
func TestAnUnrunReportIsNotAZero(t *testing.T) {
	measure := FromReport(SignalUnit, Report{}, false)
	if measure.Measured {
		t.Error("a pipeline that did not run must not be measured")
	}
}

// A critical vulnerability is not "slightly worse" than zero: an average would drown
// it in the number of minor findings.
func TestSecurityIsGradedByThreshold(t *testing.T) {
	if value := FromSecurity(1, 40, true).Value; value != 0 {
		t.Errorf("critical = %.0f, expected 0", value)
	}
	if value := FromSecurity(0, 3, true).Value; value != 50 {
		t.Errorf("high = %.0f, expected 50", value)
	}
	if value := FromSecurity(0, 0, true).Value; value != 100 {
		t.Errorf("clean = %.0f, expected 100", value)
	}
	if FromSecurity(0, 0, false).Measured {
		t.Error("an audit that did not run must not be measured")
	}
}

// Grading a campaign that cannot be compared would blame the release for slowness
// caused by a neighbour.
func TestANonComparableBenchDoesNotScore(t *testing.T) {
	measure := FromBench("fail", false)
	if measure.Measured {
		t.Error("a measure that cannot be compared must not grade performance")
	}
	if measure.Detail == "" {
		t.Error("the reason must be stated")
	}
}

func TestHealthThresholds(t *testing.T) {
	cases := map[int]string{100: "healthy", 85: "healthy", 84: "warn", 70: "warn", 69: "critical"}
	for score, expected := range cases {
		if got := Health(Breakdown{Score: score, Measured: true}); got != expected {
			t.Errorf("health(%d) = %q, expected %q", score, got, expected)
		}
	}
}

// The breakdown is always exposed: "72" does not say whether to fix tests or
// vulnerabilities.
func TestBreakdownAlwaysCarriesEverySignal(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 90),
		{Signal: SignalE2E, Detail: "not run"},
	})

	if len(breakdown.Signals) != 2 {
		t.Errorf("%d signal(s) returned, expected 2", len(breakdown.Signals))
	}
	for _, signal := range breakdown.Signals {
		if signal.Detail == "" && !signal.Measured {
			t.Errorf("signal with no breakdown: %+v", signal)
		}
	}
}

// The weighting comes from a ConfigMap: a missing one must not make every score
// incomputable.
func TestAbsentWeightsFallBackToTheAcceptedOnes(t *testing.T) {
	breakdown := Score([]Measure{measured(SignalUnit, 80)})
	if !breakdown.Measured || breakdown.Score != 80 {
		t.Errorf("score = %+v", breakdown)
	}
}

func TestStaticIsGradedByThreshold(t *testing.T) {
	if value := FromStatic(0, true).Value; value != 100 {
		t.Errorf("propre = %.0f", value)
	}
	if value := FromStatic(300, true).Value; value != 0 {
		t.Errorf("300 violations = %.0f, expected 0", value)
	}
	if FromStatic(0, false).Measured {
		t.Error("an analysis that did not run must not be measured")
	}
}

/*
 * The weight the measurement carries.
 *
 * The change of axis rests on one property: two components of the **same signal** may weigh
 * differently. The table of weights per signal could not express it, and that is why it
 * disappeared from the signature.
 */

// Two end-to-end campaigns each carry their own weight.
//
// The case the previous model could not express: they were conflated into a single
// signal, and no weighting could separate them.
func TestTwoComponentsOfTheSameSignalEachCarryTheirOwnWeight(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Label: "E2E portail", Signal: SignalE2E,
			Weight: 90, Value: 100, Measured: true},
		{Key: "geste:1.1", Label: "E2E back-office", Signal: SignalE2E,
			Weight: 10, Value: 0, Measured: true},
	})

	// 90% at 100 and 10% at 0. One axis per signal would have merged the two into a
	// single value, and this score would be 50.
	if breakdown.Score != 90 {
		t.Errorf("score = %d, expected 90", breakdown.Score)
	}
	if len(breakdown.Signals) != 2 {
		t.Fatalf("%d component(s), expected two", len(breakdown.Signals))
	}
	// And they stay distinct on screen: it is the key that separates them, not the
	// signal they share.
	if breakdown.Signals[0].Key == breakdown.Signals[1].Key {
		t.Errorf("shared key %q", breakdown.Signals[0].Key)
	}
}

// A zero weight removes the component from the score without removing it from the
// screen.
//
// It is a decision — "this pipeline does not grade" — and not an oversight. The cell
// stays, with its detail: hiding it would suggest the pipeline did not run.
func TestAZeroWeightLeavesTheScoreButNotTheScreen(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Signal: SignalUnit, Weight: 100, Value: 80, Measured: true},
		{Key: "geste:1.1", Signal: SignalE2E, Weight: 0, Value: 0, Measured: true},
	})

	if breakdown.Score != 80 {
		t.Errorf("score = %d, expected 80 — the zero-weight component must not count",
			breakdown.Score)
	}
	if len(breakdown.Signals) != 2 {
		t.Errorf("%d component(s) — the zero-weight one vanished from the screen",
			len(breakdown.Signals))
	}
}

// The order of the components is the declaration order.
//
// Sorting by signal imposed the mock-up's. A plan that runs the audit first must
// display it first: that is the order someone wrote.
func TestComponentOrderIsTheDeclarationOrder(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Signal: SignalSecurity, Weight: 10, Value: 100, Measured: true},
		{Key: "geste:1.1", Signal: SignalUnit, Weight: 10, Value: 100, Measured: true},
	})

	if breakdown.Signals[0].Signal != SignalSecurity {
		t.Errorf("first component = %q, expected the one declared first",
			breakdown.Signals[0].Signal)
	}
}

// A component left out of the computation keeps its declared weight.
//
// Without it, a pipeline that did not run would show a silent cell. With it, it says
// what it would have weighed.
func TestAnUnmeasuredComponentKeepsItsDeclaredWeight(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Signal: SignalUnit, Weight: 25, Value: 100, Measured: true},
		{Key: "geste:1.1", Signal: SignalE2E, Weight: 40, Detail: "not run"},
	})

	for _, composant := range breakdown.Signals {
		if composant.Key != "geste:1.1" {
			continue
		}
		if composant.Declared != 40 {
			t.Errorf("declared weight = %v, expected 40", composant.Declared)
		}
		if composant.Weight != 0 {
			t.Errorf("effective weight = %v, expected zero — it did not count", composant.Weight)
		}
	}
}

// A silent step's weight is the one its signal carried.
//
// Nobody loses the agreed weighting by typing nothing.
func TestAStepWithoutAWeightTakesItsSignals(t *testing.T) {
	if got := WeightOf(nil, SignalUnit); got != 25 {
		t.Errorf("default weight of unit tests = %v, expected 25", got)
	}
	// A declared zero is not "nothing declared": it is a decision, and the fallback must
	// not overwrite it.
	zero := 0.0
	if got := WeightOf(&zero, SignalUnit); got != 0 {
		t.Errorf("declared zero weight = %v — the default overwrote it", got)
	}
}
