package results

import (
	"fmt"
	"math"
)

/*
Score de qualité.

Le score n'a pas de définition évidente : la maquette en affiche un sur 100 et en
dérive le statut global, mais rien ne dit comment il est calculé ([B11]). La
pondération ci-dessous a été acceptée par le client, et **elle est donc une donnée
de configuration, pas une constante de code** — c'est ce qui permet de l'ajuster
sans redéployer.

Deux propriétés comptent autant que les chiffres.

**Le détail est toujours exposé à côté du score.** Un score dont on ne peut pas
voir la décomposition n'est pas ajustable en connaissance de cause : « 72 » ne dit
pas s'il faut corriger des tests ou des vulnérabilités.

**Un signal non mesuré sort du calcul et son poids est redistribué**, avec mention
explicite. Un 100 sur trois signaux mesurés ne doit pas ressembler à un 100 sur
six — c'est le même nombre pour deux réalités qui n'engagent pas la même décision.
*/

// Signal names a component of the score.
type Signal string

const (
	SignalUnit        Signal = "unit"
	SignalIntegration Signal = "integration"
	SignalE2E         Signal = "e2e"
	SignalCode        Signal = "code"
	SignalSecurity    Signal = "security"
	SignalPerformance Signal = "performance"
)

// Weights is the weighting of the signals.
//
// A named type rather than an anonymous map: this value comes from a ConfigMap, and
// naming it makes visible that it crosses the configuration boundary.
type Weights map[Signal]float64

// DefaultWeights is the agreed weighting.
//
// Present in code as a fallback, not as the reference: without it, a missing
// ConfigMap would make every score incomputable, which is worse than a score
// computed with the original weighting and displayed with its breakdown.
//
// Now that weight is declared on the step, it serves mostly as the per-signal
// default: a step that says nothing weighs what its signal weighed. Nobody loses the
// agreed weighting by typing nothing.
func DefaultWeights() Weights {
	return Weights{
		SignalUnit:        25,
		SignalIntegration: 20,
		SignalE2E:         20,
		SignalCode:        10,
		SignalSecurity:    15,
		SignalPerformance: 10,
	}
}

// SignalOf returns the signal a result family feeds.
//
// The family says where the figure comes from, the signal says how to grade it. The
// correspondence used to live implicitly in a list of calls — and became wrong as
// soon as a step chose its family instead of inheriting it.
func SignalOf(family Family) (Signal, bool) {
	switch family {
	case FamilyUnit:
		return SignalUnit, true
	case FamilyIntegration:
		return SignalIntegration, true
	case FamilyE2E:
		return SignalE2E, true
	case FamilyStatic:
		return SignalCode, true
	case FamilySecurity:
		return SignalSecurity, true
	case FamilyLoad:
		return SignalPerformance, true
	default:
		return "", false
	}
}

// WeightOf returns a step's weight: the one it declares, otherwise its signal's.
//
// The pointer carries three states, which is why it is one. Absent: the signal's
// default applies. Zero: the step is deliberately out of the score. A value: it
// applies.
func WeightOf(declared *float64, signal Signal) float64 {
	if declared != nil {
		return *declared
	}
	return DefaultWeights()[signal]
}

// Measure is the value of one component of the score.
type Measure struct {
	// Key identifies the component, and that is what changed axis.
	//
	// The signal used to be the identity: six components, one per nature, so two
	// distinct end-to-end campaigns were conflated into one. The key is now the step
	// that measured — or, for a release predating plans, the family for want of better.
	Key string
	// Label names the component on screen, in the words of whoever declared it.
	Label string
	// Signal says how to measure, no longer how to weigh. It remains what links a report
	// to its grading rule.
	Signal Signal
	// Ref and Family say what produced the measure, in the catalogue's vocabulary: the
	// pipeline family to re-run, the report family to open in detail.
	//
	// Carried by the measure rather than inferred from the signal by a table in the
	// interface. That table existed in three copies, and became wrong as soon as a
	// project declared two campaigns of the same nature: they do not re-run the same
	// pipeline.
	Ref    string
	Family Family
	// Weight is this component's declared weight.
	//
	// Carried by the measure rather than looked up in a table: that is what lets two
	// steps of the same signal weigh differently.
	Weight float64
	// Value ranges from 0 to 100. Ignored when Measured is false.
	Value float64
	// Measured separates "measured at zero" from "not measured". Both display zero, and
	// confusing them would sink the score of a release whose pipeline simply did not
	// run.
	Measured bool
	// Detail explique la valeur, en français. Affiché à côté du score.
	Detail string
}

// SignalKey returns the key of a component identified by its signal alone.
//
// The fallback for releases that have no plan. Their score stays computable, which
// is the only reason this form still exists.
func SignalKey(signal Signal) string { return "signal:" + string(signal) }

// Breakdown is the score and its decomposition.
type Breakdown struct {
	// Score ranges from 0 to 100, rounded to an integer. Zero when nothing is measured —
	// and Measured is then false, which is the only way to tell a project scoring zero
	// from a project not evaluated.
	Score    int
	Measured bool
	Signals  []SignalScore
	// Redistributed lists the components left out of the computation, by key. Displayed
	// as is: the reader must know what the score they are reading covers.
	Redistributed []string
}

// SignalScore is one component's contribution.
type SignalScore struct {
	// Key and Label identify the component. The signal comes along because it says the
	// nature of what was measured, and still picks an icon.
	Key    string
	Label  string
	Signal Signal
	// Ref and Family say what produced the measure: the pipeline to re-run, and the
	// report family to open.
	Ref    string
	Family Family
	Value  float64
	// Weight is the effective weight, after redistribution. Distinct from the declared
	// weight: it is what explains the real contribution.
	Weight float64
	// Declared is the weight as it was typed, before redistribution. Returned because an
	// effective weight alone cannot be read back as a weighting: "33" does not say
	// whether someone wrote 20 or 40.
	Declared float64
	Measured bool
	Detail   string
}

// Score computes the weighted score and its decomposition.
//
// Weights travel with the measures: a component knows what it weighs, so two
// components of the same signal may weigh differently. The weights table left the
// signature for that reason — looking weights up by signal was exactly what stopped
// two end-to-end campaigns from counting separately.
//
// The order of the measures is preserved. It is the declaration order, hence the
// order the screen must show: a plan that runs the audit first displays it first,
// and sorting here would impose an order nobody asked for.
func Score(measures []Measure) Breakdown {
	var totalWeight float64
	for _, measure := range measures {
		if measure.Measured {
			totalWeight += measure.Weight
		}
	}

	breakdown := Breakdown{}

	// A zero totalWeight covers two cases at once: nothing was measured, or everything
	// measured weighs zero. In both, no score is computable — and returning zero would
	// be a judgement, the harshest possible, on a release nobody evaluated.
	if totalWeight == 0 {
		for _, measure := range measures {
			breakdown.Signals = append(breakdown.Signals, retired(measure))
			breakdown.Redistributed = append(breakdown.Redistributed, measure.Key)
		}
		return breakdown
	}

	var weighted float64
	for _, measure := range measures {
		if !measure.Measured {
			breakdown.Signals = append(breakdown.Signals, retired(measure))
			breakdown.Redistributed = append(breakdown.Redistributed, measure.Key)
			continue
		}

		// The effective weight is the component's share of what was measured. That is the
		// redistribution: by renormalisation, without arbitrarily reassigning the missing
		// weight to a neighbour.
		effective := measure.Weight / totalWeight * 100
		weighted += measure.Value * measure.Weight / totalWeight

		breakdown.Signals = append(breakdown.Signals, SignalScore{
			Key: measure.Key, Label: measure.Label, Signal: measure.Signal,
			Ref: measure.Ref, Family: measure.Family,
			Value: measure.Value, Weight: effective, Declared: measure.Weight,
			Measured: true, Detail: measure.Detail,
		})
	}

	breakdown.Score = int(math.Round(weighted))
	breakdown.Measured = true
	return breakdown
}

// retired returns the line of a component left out of the computation.
//
// Its declared weight is kept: that is what allows reading "this step would have
// weighed 20" on a pipeline that did not run, rather than a silent cell.
func retired(measure Measure) SignalScore {
	return SignalScore{
		Key: measure.Key, Label: measure.Label, Signal: measure.Signal,
		Ref: measure.Ref, Family: measure.Family,
		Declared: measure.Weight, Detail: measure.Detail,
	}
}

// FromReport returns a signal's measure from a test report.
//
// The rule: 100 if everything passes, otherwise 100 × (1 − failures/total), with a
// flaky test counting as half a failure. A flaky test costs on every run without
// ever blocking: not counting it at all would say it costs nothing.
func FromReport(signal Signal, report Report, ran bool) Measure {
	if !ran || report.Total == 0 {
		reason := "not run"
		if ran {
			reason = "no test"
		}
		return Measure{Signal: signal, Detail: reason}
	}

	failures := float64(report.Failed) + float64(report.Flaky)/2
	value := math.Max(0, 100*(1-failures/float64(report.Total)))

	detail := fmt.Sprintf("%d/%d passed", report.Passed, report.Total)
	if report.Flaky > 0 {
		detail += fmt.Sprintf(", %d instable(s)", report.Flaky)
	}

	return Measure{Signal: signal, Value: value, Measured: true, Detail: detail}
}

// FromSecurity returns the security signal's measure.
//
// By threshold rather than proportionally: a critical vulnerability is not
// "slightly worse" than zero, it forbids shipping. An average would drown it in the
// number of minor findings.
func FromSecurity(critical, high int, ran bool) Measure {
	if !ran {
		return Measure{Signal: SignalSecurity, Detail: "not run"}
	}

	switch {
	case critical > 0:
		return Measure{Signal: SignalSecurity, Value: 0, Measured: true,
			Detail: fmt.Sprintf("%d finding(s) critique(s)", critical)}
	case high > 0:
		return Measure{Signal: SignalSecurity, Value: 50, Measured: true,
			Detail: fmt.Sprintf("%d high-severity finding(s)", high)}
	default:
		return Measure{Signal: SignalSecurity, Value: 100, Measured: true,
			Detail: "no critical or high finding"}
	}
}

// FromBench returns the performance signal's measure.
func FromBench(verdict string, comparable bool) Measure {
	if !comparable {
		// A campaign that cannot be compared is not an absent campaign, but it cannot grade
		// performance: grading it would blame the release for slowness caused by a
		// neighbour.
		return Measure{Signal: SignalPerformance,
			Detail: "measure not comparable to the baseline conditions"}
	}

	switch verdict {
	case "pass":
		return Measure{Signal: SignalPerformance, Value: 100, Measured: true, Detail: "conforme"}
	case "warn":
		return Measure{Signal: SignalPerformance, Value: 60, Measured: true, Detail: "limite"}
	case "fail":
		return Measure{Signal: SignalPerformance, Value: 0, Measured: true, Detail: "hors seuil"}
	default:
		return Measure{Signal: SignalPerformance, Detail: "not measured"}
	}
}

// FromStatic returns the code quality measure.
//
// By threshold: 100 when the linters are clean, degraded step by step. The
// thresholds live here because they are not part of the weighting — that says a
// signal's weight, not how to measure it.
func FromStatic(violations int, ran bool) Measure {
	if !ran {
		return Measure{Signal: SignalCode, Detail: "not run"}
	}

	value := 0.0
	switch {
	case violations == 0:
		value = 100
	case violations <= 10:
		value = 80
	case violations <= 50:
		value = 60
	case violations <= 200:
		value = 30
	}

	return Measure{Signal: SignalCode, Value: value, Measured: true,
		Detail: fmt.Sprintf("%d violation(s)", violations)}
}

// Health derives the overall status of the score from the thresholds.
func Health(breakdown Breakdown) string {
	if !breakdown.Measured {
		return "na"
	}
	return healthFromValue(float64(breakdown.Score))
}

// SignalHealth derives a signal's status, at the same thresholds as the overall
// score.
//
// Returned by the API rather than recomputed in the browser: the interface derives
// no verdict, otherwise changing a threshold here would produce a second, diverging
// opinion client-side. Applying the score's thresholds to a signal is an
// extrapolation, and this is the only place it is made.
func SignalHealth(signal SignalScore) string {
	if !signal.Measured {
		return "na"
	}
	return healthFromValue(signal.Value)
}

func healthFromValue(value float64) string {
	switch {
	case value >= 85:
		return "healthy"
	case value >= 70:
		return "warn"
	default:
		return "critical"
	}
}

// What disappeared here: sorting by signal.
//
// It imposed the mock-up's order, because the signal was the identity and a Go map
// has none. A component is now a step, and steps arrive in the plan's order: the one
// someone declared, and therefore the one they expect to read. Sorting here would
// overwrite it.
