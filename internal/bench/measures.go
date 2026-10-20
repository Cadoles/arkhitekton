// Package bench makes load-test measures comparable.
//
// Reading a k6 summary is the k6 plugin's. **Deciding whether a measurement is comparable** is the
// dashboard's, and it is not mechanical: the cluster hosting Tekton is also that of the load-test
// environment. A build, a Trivy audit
// or an E2E campaign running during the measurement skews it as much as the generator would have
// — it is a problem of neighbourhood, not of generation, and moving the runner out of the
// cluster only half solves it.
//
// The consequence is direct: a trend curve over six points whose conditions vary means nothing.
// This package therefore carries the rule that makes the comparison honest — only the
// measurements taken under exclusivity enter the comparison with the baseline; the others are
// kept, displayed, and marked as not comparable.
//
// Everything here is pure: no database, no cluster, no network.
package bench

// Measure is a readable measurement, ready to be compared.
type Measure struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`
	Value  float64
	// LowerBetter is copied from the metric: the recorded measurement must be re-readable and
	// comparable without rebuilding the metrics catalogue, which may have changed in the meantime.
	LowerBetter bool
	Volume      bool
	// ThresholdOK takes the verdict k6 itself gives on its declared thresholds. Distinct from the
	// comparison with the baseline: k6 says "the contract is met", the baseline says "it is as good
	// as before". The two can contradict each other, and ignoring it would lose the most direct
	// piece of information.
	ThresholdOK *bool
}

// Summary is a campaign's result.
type Summary struct {
	Scenario string
	Measures []Measure
	// VUs is the number of virtual users at peak. Reading context: comparing two measurements taken
	// at different loads makes no sense.
	VUs int
	// ThresholdsFailed lists the k6 thresholds not met, in reading order.
	ThresholdsFailed []string
}
