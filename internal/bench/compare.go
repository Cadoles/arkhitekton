package bench

import "fmt"

// Verdict is the judgement passed on a measurement.
type Verdict string

const (
	// VerdictPass: as good as the baseline, or better.
	VerdictPass Verdict = "pass"
	// VerdictWarn: degraded by less than 10%.
	VerdictWarn Verdict = "warn"
	// VerdictFail: degraded beyond it.
	VerdictFail Verdict = "fail"
	// VerdictNA: nothing to compare, or a measurement that cannot be compared.
	VerdictNA Verdict = "na"
)

// rank orders the verdicts from the most lenient to the most severe. Serves so that a second
// criterion can **degrade** a verdict without ever improving it.
var rank = map[Verdict]int{VerdictNA: 0, VerdictPass: 1, VerdictWarn: 2, VerdictFail: 3}

// Tolerance threshold. 10%: taken from the mock-up, where it is already applied and accepted by
// the client. Redefining it here would make the ported interface diverge from the accepted one.
const warnThreshold = 0.9

// Conditions describes the conditions under which a measurement was taken.
//
// Without them, a measurement is only a number. The cluster hosting Tekton is also the target's:
// two measurements, one taken during a build and the other in an idle cluster, do not compare,
// and putting them on the same curve produces an invented trend.
type Conditions struct {
	// Exclusive is true when the `exclusive-cluster` barrier was held: the dashboard let no other
	// job run.
	Exclusive bool
	// ConcurrentJobs is the number of runs observed at start-up. Zero with Exclusive true is the
	// nominal case; non-zero, it quantifies the disturbance.
	ConcurrentJobs int
}

// Comparable says whether the measurement can enter a comparison with the baseline.
//
// The rule is deliberately strict: exclusivity alone is not enough if runs were still going at
// start-up. The barrier drains before launching, so the two should coincide — but if they
// diverge, it is the observation that is authoritative, not the intention.
func (c Conditions) Comparable() bool {
	return c.Exclusive && c.ConcurrentJobs == 0
}

// Why explains, in French, why a measurement is not comparable.
//
// The interface must be able to say it: a measurement displayed with no verdict and no reason
// reads as a measurement failure, whereas it is a valid measurement whose comparison alone is
// refused.
func (c Conditions) Why() string {
	switch {
	case c.Comparable():
		return ""
	case !c.Exclusive && c.ConcurrentJobs > 0:
		return fmt.Sprintf(
			"measured without reserving the cluster, %d concurrent run(s) observed",
			c.ConcurrentJobs)
	case !c.Exclusive:
		return "measured without reserving the cluster"
	default:
		return fmt.Sprintf(
			"%d run(s) still running at start despite the reservation", c.ConcurrentJobs)
	}
}

// Comparison sets a measurement against its baseline.
type Comparison struct {
	Measure  Measure
	Baseline *float64
	Verdict  Verdict
	// Ratio is 1 at parity, above it when the measurement is better. Returned so that the interface
	// displays the gap rather than a bare verdict.
	Ratio float64
	// Comparable is false when the measurement conditions forbid the comparison. The measurement
	// stays displayed: hiding it would suggest it did not happen.
	Comparable bool
	// Reason carries the ground for refusing the comparison, empty otherwise.
	Reason string
}

// Compare sets a campaign against its baselines.
//
// The measurements that cannot be compared receive [VerdictNA] and their reason — they are
// neither set aside nor judged. Setting them aside would amount to hiding a campaign that did
// happen; judging them would amount to charging a release with a slowness caused by a
// neighbouring build.
func Compare(summary Summary, baselines map[string]float64, conditions Conditions) []Comparison {
	comparable := conditions.Comparable()
	reason := conditions.Why()

	// The total volume may degrade the verdict of the other measurements, never improve it: a
	// conforming throughput obtained by handling fewer requests is not a success. Its own verdict is
	// therefore computed first.
	volume := VerdictNA
	for _, measure := range summary.Measures {
		if !measure.Volume {
			continue
		}
		if baseline, known := baselines[measure.Metric]; known && comparable {
			volume = judge(measure.Value, baseline, measure.LowerBetter)
		}
	}

	comparisons := make([]Comparison, 0, len(summary.Measures))
	for _, measure := range summary.Measures {
		comparison := Comparison{
			Measure: measure, Verdict: VerdictNA, Comparable: comparable, Reason: reason,
		}

		baseline, known := baselines[measure.Metric]
		if known {
			value := baseline
			comparison.Baseline = &value
		}

		if known && comparable {
			comparison.Ratio = ratio(measure.Value, baseline, measure.LowerBetter)
			comparison.Verdict = judge(measure.Value, baseline, measure.LowerBetter)

			// The volume degrades the others, not itself.
			if !measure.Volume && rank[volume] > rank[comparison.Verdict] {
				comparison.Verdict = volume
			}
		}

		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func ratio(value, baseline float64, lowerBetter bool) float64 {
	if baseline == 0 || value == 0 {
		// A zero baseline would make any ratio infinite; a zero measurement on a metric where lower is
		// better is perfect but cannot be divided by.
		if lowerBetter && value == 0 {
			return 1
		}
		return 0
	}
	if lowerBetter {
		return baseline / value
	}
	return value / baseline
}

func judge(value, baseline float64, lowerBetter bool) Verdict {
	r := ratio(value, baseline, lowerBetter)
	switch {
	case r >= 1:
		return VerdictPass
	case r >= warnThreshold:
		return VerdictWarn
	default:
		return VerdictFail
	}
}

// Worst returns the most severe verdict of a set.
//
// It is what feeds the quality score's performance signal: a campaign is worth its weakest
// point, and not its average — an average would drown a regression on one metric in the
// conformity of the others.
func Worst(comparisons []Comparison) Verdict {
	worst := VerdictNA
	for _, comparison := range comparisons {
		if rank[comparison.Verdict] > rank[worst] {
			worst = comparison.Verdict
		}
	}
	return worst
}
