package parse

// K6SummaryJSON parses a k6 summary into a [K6].
const K6SummaryJSON ID = "k6-summary-json"

// K6 is a k6 summary: every metric, decoded.
type K6 struct {
	Metrics map[string]K6Metric
}

// K6Metric is one metric's statistics and thresholds.
type K6Metric struct {
	// Values maps a statistic (`p(95)`, `rate`, `count`...) to its value.
	Values map[string]float64
	// Thresholds maps a declared threshold to whether k6 says it is met.
	Thresholds map[string]bool
	// Err is set when the metric's object has neither of k6's forms. Kept per metric: one odd
	// metric must not cost the others.
	Err error
}
