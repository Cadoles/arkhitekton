package results

import "testing"

func measured(signal Signal, value float64) Measure {
	return Measure{Signal: signal, Value: value, Measured: true}
}

func TestScoreIsWeighted(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 100),
		measured(SignalIntegration, 100),
		measured(SignalE2E, 100),
		measured(SignalCode, 100),
		measured(SignalSecurity, 100),
		measured(SignalPerformance, 100),
	}, DefaultWeights())

	if breakdown.Score != 100 || !breakdown.Measured {
		t.Errorf("score = %d", breakdown.Score)
	}
}

// Un 100 sur trois signaux mesurés ne doit pas ressembler à un 100 sur six :
// c'est le même nombre pour deux réalités qui n'engagent pas la même décision.
func TestUnmeasuredSignalsAreRedistributedAndNamed(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 100),
		measured(SignalIntegration, 100),
		{Signal: SignalE2E, Detail: "non exécuté"},
		{Signal: SignalCode, Detail: "non exécuté"},
		{Signal: SignalSecurity, Detail: "non exécuté"},
		{Signal: SignalPerformance, Detail: "non exécuté"},
	}, DefaultWeights())

	if breakdown.Score != 100 {
		t.Errorf("score = %d, attendu 100 sur ce qui est mesuré", breakdown.Score)
	}
	if len(breakdown.Redistributed) != 4 {
		t.Errorf("redistribués = %v", breakdown.Redistributed)
	}

	// Le poids effectif est la part du signal dans ce qui a été mesuré : 25 et 20
	// sur un total de 45.
	for _, signal := range breakdown.Signals {
		if signal.Signal == SignalUnit && int(signal.Weight) != 55 {
			t.Errorf("poids effectif des tests unitaires = %.1f, attendu ~55", signal.Weight)
		}
	}
}

// Rendre zéro serait un jugement, et le plus sévère possible sur une release
// qu'on n'a pas évaluée.
func TestNothingMeasuredIsNotAZero(t *testing.T) {
	breakdown := Score([]Measure{
		{Signal: SignalUnit}, {Signal: SignalSecurity},
	}, DefaultWeights())

	if breakdown.Measured {
		t.Error("un score non mesuré ne doit pas se déclarer mesuré")
	}
	if Health(breakdown) != "na" {
		t.Errorf("santé = %q, attendu na", Health(breakdown))
	}
}

// Un test instable coûte à chaque exécution sans jamais bloquer : ne pas le
// compter reviendrait à dire qu'il ne coûte rien.
func TestFlakyCountsAsHalfAFailure(t *testing.T) {
	report := Report{Total: 10, Passed: 8, Failed: 1, Flaky: 1}

	measure := FromReport(SignalUnit, report, true)
	// 1 échec + 0,5 instable sur 10 → 85.
	if measure.Value != 85 {
		t.Errorf("valeur = %.1f, attendu 85", measure.Value)
	}
	if measure.Detail == "" {
		t.Error("le détail doit accompagner la valeur")
	}
}

// « Mesuré à zéro » et « non mesuré » affichent zéro. Les confondre ferait
// tomber le score d'une release dont un pipeline n'a simplement pas tourné.
func TestAnUnrunReportIsNotAZero(t *testing.T) {
	measure := FromReport(SignalUnit, Report{}, false)
	if measure.Measured {
		t.Error("un pipeline non exécuté ne doit pas être mesuré")
	}
}

// Une vulnérabilité critique n'est pas « un peu moins bien » que zéro : une
// moyenne la noierait dans le nombre de findings mineurs.
func TestSecurityIsGradedByThreshold(t *testing.T) {
	if value := FromSecurity(1, 40, true).Value; value != 0 {
		t.Errorf("critique = %.0f, attendu 0", value)
	}
	if value := FromSecurity(0, 3, true).Value; value != 50 {
		t.Errorf("haute = %.0f, attendu 50", value)
	}
	if value := FromSecurity(0, 0, true).Value; value != 100 {
		t.Errorf("propre = %.0f, attendu 100", value)
	}
	if FromSecurity(0, 0, false).Measured {
		t.Error("un audit non exécuté ne doit pas être mesuré")
	}
}

// Juger une campagne non comparable imputerait à la release une lenteur causée
// par un voisin (B16).
func TestANonComparableBenchDoesNotScore(t *testing.T) {
	measure := FromBench("fail", false)
	if measure.Measured {
		t.Error("une mesure non comparable ne doit pas noter la performance")
	}
	if measure.Detail == "" {
		t.Error("le motif doit être dit")
	}
}

func TestHealthThresholds(t *testing.T) {
	cases := map[int]string{100: "healthy", 85: "healthy", 84: "warn", 70: "warn", 69: "critical"}
	for score, expected := range cases {
		if got := Health(Breakdown{Score: score, Measured: true}); got != expected {
			t.Errorf("santé(%d) = %q, attendu %q", score, got, expected)
		}
	}
}

// Le détail est toujours exposé : « 72 » ne dit pas s'il faut corriger des tests
// ou des vulnérabilités.
func TestBreakdownAlwaysCarriesEverySignal(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 90),
		{Signal: SignalE2E, Detail: "non exécuté"},
	}, DefaultWeights())

	if len(breakdown.Signals) != 2 {
		t.Errorf("%d signal(aux) rendus, attendu 2", len(breakdown.Signals))
	}
	for _, signal := range breakdown.Signals {
		if signal.Detail == "" && !signal.Measured {
			t.Errorf("signal sans détail: %+v", signal)
		}
	}
}

// La pondération vient d'une ConfigMap : une absente ne doit pas rendre tout
// score incalculable.
func TestAbsentWeightsFallBackToTheAcceptedOnes(t *testing.T) {
	breakdown := Score([]Measure{measured(SignalUnit, 80)}, nil)
	if !breakdown.Measured || breakdown.Score != 80 {
		t.Errorf("score = %+v", breakdown)
	}
}

func TestStaticIsGradedByThreshold(t *testing.T) {
	if value := FromStatic(0, true).Value; value != 100 {
		t.Errorf("propre = %.0f", value)
	}
	if value := FromStatic(300, true).Value; value != 0 {
		t.Errorf("300 violations = %.0f, attendu 0", value)
	}
	if FromStatic(0, false).Measured {
		t.Error("une analyse non exécutée ne doit pas être mesurée")
	}
}
