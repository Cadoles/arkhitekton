package k6

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// metric names a k6 metric and how to read it.
type metric struct {
	name, stat, label, unit string
	// lowerBetter inverts the comparison: a latency goes down, a throughput goes up.
	lowerBetter bool
	// volume may degrade another measure's verdict without ever improving it.
	volume bool
}

// metrics are the four questions asked before delivering: is it slow, is it slow for the
// unluckiest, how much does it take, does it break.
var metrics = []metric{
	{name: "http_req_duration", stat: "p(95)", label: "Latence p95", unit: "ms", lowerBetter: true},
	{name: "http_req_duration", stat: "med", label: "Latence médiane", unit: "ms", lowerBetter: true},
	{name: "http_reqs", stat: "rate", label: "Débit", unit: "req/s"},
	{name: "http_reqs", stat: "count", label: "Requêtes", unit: "req", volume: true},
	{name: "http_req_failed", stat: "rate", label: "Taux d'échec", unit: "%", lowerBetter: true},
}

// campaignReader reads one summary per scenario.
type campaignReader struct{}

func (campaignReader) Interpret(_ context.Context, _ capability.RunState, records []parse.Record) ([]facts.Set, error) {
	var sets []facts.Set
	var unreadable []string
	// One scenario once: the same summary published under `load/` and under `k6/` would otherwise
	// make two history points, a false trend.
	seen := map[string]bool{}
	for _, record := range records {
		scenario := scenarioOf(record.Object)
		if seen[scenario] {
			continue
		}
		seen[scenario] = true

		summary, ok := record.Payload.(parse.K6)
		if record.Err != nil || !ok {
			unreadable = append(unreadable, record.Object)
			continue
		}
		set, err := campaign(scenario, summary)
		if err != nil {
			unreadable = append(unreadable, record.Object)
			continue
		}
		set.RawObject = record.Object
		sets = append(sets, set)
	}
	if len(unreadable) > 0 {
		// The others are kept: a campaign of four scenarios is not lost to one truncated file.
		return sets, fmt.Errorf("%d summary(ies) unreadable: %s", len(unreadable), strings.Join(unreadable, ", "))
	}
	return sets, nil
}

func campaign(scenario string, summary parse.K6) (facts.Set, error) {
	measures := &facts.Measures{Scenario: scenario}
	failed := map[string]bool{}

	for _, wanted := range metrics {
		decoded, present := summary.Metrics[wanted.name]
		if !present {
			// A scenario may make no HTTP request: the measure is absent, not zero.
			continue
		}
		if decoded.Err != nil {
			return facts.Set{}, fmt.Errorf("metric %q of scenario %q: %w", wanted.name, scenario, decoded.Err)
		}
		value, measured := decoded.Values[wanted.stat]
		if !measured {
			continue
		}
		measure := facts.Measure{
			Metric: wanted.name + ":" + wanted.stat, Label: wanted.label, Unit: wanted.unit,
			Value: value, LowerBetter: wanted.lowerBetter, Volume: wanted.volume,
		}
		// A k6 rate is 0..1: shown as a percentage, "0.02%" would read as two hundredths of one.
		if wanted.unit == "%" {
			measure.Value = value * 100
		}
		for name, ok := range decoded.Thresholds {
			measure.ThresholdOK = &ok
			if !ok {
				failed[wanted.name+": "+name] = true
			}
		}
		measures.Items = append(measures.Items, measure)
	}
	if len(measures.Items) == 0 {
		return facts.Set{}, fmt.Errorf("k6 summary of scenario %q: no usable metric", scenario)
	}

	if vus, present := summary.Metrics["vus_max"]; present && vus.Err == nil {
		// `value` on a gauge, `max` on a series: both forms exist depending on the k6 version.
		if peak, found := vus.Values["value"]; found {
			measures.VUs = int(peak)
		} else if peak, found := vus.Values["max"]; found {
			measures.VUs = int(peak)
		}
	}

	var thresholds []string
	for name := range failed {
		thresholds = append(thresholds, name)
	}
	sort.Strings(thresholds)
	measures.ThresholdsFailed = len(thresholds) > 0

	parts := []string{fmt.Sprintf("%d measure(s)", len(measures.Items))}
	if measures.VUs > 0 {
		parts = append(parts, fmt.Sprintf("%d VUs at peak", measures.VUs))
	}
	if len(thresholds) > 0 {
		// Named and not counted: knowing which threshold gave way decides what follows.
		parts = append(parts, "thresholds not met: "+strings.Join(thresholds, ", "))
	}

	return facts.Set{
		Shape: facts.ShapeMeasures, Group: "load", Tool: "k6",
		Status: status(measures.Items), Summary: strings.Join(parts, ", "), Measures: measures,
	}, nil
}

// status is the campaign's verdict on its own thresholds. Not the comparison with the baseline:
// that one moves when a baseline is pinned again, and a recorded status must not.
func status(measures []facts.Measure) facts.Status {
	declared := false
	for _, measure := range measures {
		if measure.ThresholdOK == nil {
			continue
		}
		declared = true
		if !*measure.ThresholdOK {
			return facts.StatusFail
		}
	}
	if !declared {
		// No threshold declared: the campaign ran, but pronounces no verdict on itself.
		return facts.StatusNA
	}
	return facts.StatusPass
}

// scenarioOf derives a scenario from an object's key: its name, without `.json` nor `-summary`.
func scenarioOf(key string) string {
	name := key
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	name = strings.TrimSuffix(name, ".json")
	name = strings.TrimSuffix(name, "-summary")
	if name == "" {
		return "default"
	}
	return name
}
