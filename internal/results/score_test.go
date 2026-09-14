package results

import "testing"

// measured rend une mesure au poids **par défaut de son signal**.
//
// C'est ce que produit un geste qui ne déclare rien : la pondération acceptée par
// le client reste le repli, et ces tests continuent donc de porter sur elle.
func measured(signal Signal, value float64) Measure {
	return Measure{
		Key: SignalKey(signal), Signal: signal, Weight: WeightOf(nil, signal),
		Value: value, Measured: true,
	}
}

// absente rend une mesure non exécutée, au poids par défaut de son signal.
func absente(signal Signal) Measure {
	return Measure{
		Key: SignalKey(signal), Signal: signal, Weight: WeightOf(nil, signal),
		Detail: "non exécuté",
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

// Un 100 sur trois signaux mesurés ne doit pas ressembler à un 100 sur six :
// c'est le même nombre pour deux réalités qui n'engagent pas la même décision.
func TestUnmeasuredSignalsAreRedistributedAndNamed(t *testing.T) {
	breakdown := Score([]Measure{
		measured(SignalUnit, 100),
		measured(SignalIntegration, 100),
		absente(SignalE2E),
		absente(SignalCode),
		absente(SignalSecurity),
		absente(SignalPerformance),
	})

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
		absente(SignalUnit), absente(SignalSecurity),
	})

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
	})

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
		t.Errorf("300 violations = %.0f, attendu 0", value)
	}
	if FromStatic(0, false).Measured {
		t.Error("une analyse non exécutée ne doit pas être mesurée")
	}
}

/*
 * Le poids porté par la mesure.
 *
 * Le changement d'axe de doc 15 §15.6 tient dans une propriété : deux composants
 * de **même signal** peuvent peser différemment. La table des poids par signal ne
 * savait pas l'exprimer, et c'est pour cela qu'elle a disparu de la signature.
 */

// Deux campagnes E2E pèsent chacune la sienne.
//
// Le cas que le modèle précédent ne savait pas dire : elles se confondaient dans
// un signal unique, et aucune pondération ne pouvait les séparer.
func TestDeuxComposantsDeMemeSignalPesentChacunLeSien(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Label: "E2E portail", Signal: SignalE2E,
			Weight: 90, Value: 100, Measured: true},
		{Key: "geste:1.1", Label: "E2E back-office", Signal: SignalE2E,
			Weight: 10, Value: 0, Measured: true},
	})

	// 90 % à 100 et 10 % à 0. Un axe par signal aurait fondu les deux en une
	// seule valeur, et ce score serait 50.
	if breakdown.Score != 90 {
		t.Errorf("score = %d, attendu 90", breakdown.Score)
	}
	if len(breakdown.Signals) != 2 {
		t.Fatalf("%d composant(s), attendu deux", len(breakdown.Signals))
	}
	// Et ils restent distincts à l'affichage : c'est la clé qui les sépare, pas
	// le signal qu'ils partagent.
	if breakdown.Signals[0].Key == breakdown.Signals[1].Key {
		t.Errorf("clé partagée %q", breakdown.Signals[0].Key)
	}
}

// Un poids nul retire le composant du score sans le retirer de l'écran.
//
// C'est une décision — « ce pipeline ne note pas » — et pas un oubli. La case
// reste, avec son détail : la faire disparaître ferait croire qu'il n'a pas
// tourné.
func TestUnPoidsNulSortDuCalculSansSortirDeLecran(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Signal: SignalUnit, Weight: 100, Value: 80, Measured: true},
		{Key: "geste:1.1", Signal: SignalE2E, Weight: 0, Value: 0, Measured: true},
	})

	if breakdown.Score != 80 {
		t.Errorf("score = %d, attendu 80 — le composant à poids nul ne doit pas peser",
			breakdown.Score)
	}
	if len(breakdown.Signals) != 2 {
		t.Errorf("%d composant(s) — celui à poids nul a disparu de l'écran",
			len(breakdown.Signals))
	}
}

// L'ordre des composants est celui de la déclaration.
//
// Le tri par signal imposait celui de la maquette. Un plan qui lance l'audit en
// premier doit l'afficher en premier : c'est l'ordre que quelqu'un a écrit.
func TestLordreDesComposantsEstCeluiDeLaDeclaration(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Signal: SignalSecurity, Weight: 10, Value: 100, Measured: true},
		{Key: "geste:1.1", Signal: SignalUnit, Weight: 10, Value: 100, Measured: true},
	})

	if breakdown.Signals[0].Signal != SignalSecurity {
		t.Errorf("premier composant = %q, attendu celui déclaré en premier",
			breakdown.Signals[0].Signal)
	}
}

// Un composant sorti du calcul garde son poids déclaré.
//
// Sans lui, un pipeline qui n'a pas tourné afficherait une case muette. Avec, il
// dit ce qu'il aurait pesé.
func TestUnComposantNonMesureGardeSonPoidsDeclare(t *testing.T) {
	breakdown := Score([]Measure{
		{Key: "geste:1.0", Signal: SignalUnit, Weight: 25, Value: 100, Measured: true},
		{Key: "geste:1.1", Signal: SignalE2E, Weight: 40, Detail: "non exécuté"},
	})

	for _, composant := range breakdown.Signals {
		if composant.Key != "geste:1.1" {
			continue
		}
		if composant.Declared != 40 {
			t.Errorf("poids déclaré = %v, attendu 40", composant.Declared)
		}
		if composant.Weight != 0 {
			t.Errorf("poids effectif = %v, attendu nul — il n'a pas pesé", composant.Weight)
		}
	}
}

// Le poids d'un geste muet est celui que son signal pesait (B11).
//
// Personne ne perd la pondération convenue en ne saisissant rien.
func TestUnGesteSansPoidsPrendCeluiDeSonSignal(t *testing.T) {
	if got := WeightOf(nil, SignalUnit); got != 25 {
		t.Errorf("poids par défaut des tests unitaires = %v, attendu 25", got)
	}
	// Zéro déclaré n'est pas « rien déclaré » : c'est une décision, et le repli ne
	// doit pas l'écraser.
	zero := 0.0
	if got := WeightOf(&zero, SignalUnit); got != 0 {
		t.Errorf("poids nul déclaré = %v — le défaut l'a écrasé", got)
	}
}
