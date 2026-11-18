package http

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The score, one tile per pipeline launched.
 *
 * A release's or a pull request's results are what was launched on it, by hand or by an
 * automation: each pipeline launched adds its tile, a pipeline launched again keeps it and shows
 * its latest result. Nothing is announced before it runs, and nothing launched goes unseen, a
 * pipeline outside the natures the score knows included.
 *
 * Each tile weighs what an administrator set for its pipeline, otherwise what its nature weighs,
 * otherwise nothing: out of the score, shown all the same. The weights are those in force when the
 * score is read.
 */

// scoreStore is what computing the score needs.
//
// An interface because three handlers call it: the results, the releases' list badge and the
// dossier. The same figure must come out of all three.
type scoreStore interface {
	ListJobsIn(ctx context.Context, scope store.Scope) ([]store.ReleaseJob, error)
	LatestFindingsOf(ctx context.Context, scope store.Scope, capability string) (store.SecurityAudit, error)
	ListLoadResults(ctx context.Context, scope store.Scope) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
	ScoreWeights(ctx context.Context) (map[string]float64, error)
}

// computeScore composes a subject's score from what was launched on it and what it measured.
func computeScore(
	ctx context.Context, source scoreStore, scope store.Scope,
	reports []store.TestResult, registry *catalog.Catalog,
) (results.Breakdown, error) {
	jobs, err := source.ListJobsIn(ctx, scope)
	if err != nil {
		return results.Breakdown{}, err
	}
	weights, err := source.ScoreWeights(ctx)
	if err != nil {
		return results.Breakdown{}, err
	}

	measures := make([]results.Measure, 0)
	for _, kind := range tiledKinds(jobs, reports, registry) {
		measure, err := tileMeasure(ctx, source, scope, kind, reports, registry, weights)
		if err != nil {
			return results.Breakdown{}, err
		}
		measures = append(measures, measure)
	}
	return results.Score(measures), nil
}

// tiledKinds returns the pipelines that have a tile, in the order they were first launched.
//
// A job's pipeline counts when the catalogue says it produces results; a result's pipeline counts
// whatever produced it, an outside runner's campaign included. Groups with a section of their own
// and a build's files have no tile.
func tiledKinds(jobs []store.ReleaseJob, reports []store.TestResult, registry *catalog.Catalog) []string {
	first := map[string]time.Time{}
	see := func(kind string, at time.Time) {
		if kind == "" {
			return
		}
		if known, seen := first[kind]; !seen || at.Before(known) {
			first[kind] = at
		}
	}
	for _, job := range jobs {
		pipeline, found := find(registry, job.Kind)
		if !found || !producesResults(pipeline) || untiled(results.Group(pipeline.Group())) {
			continue
		}
		see(pipeline.Kind, job.RequestedAt)
	}
	for _, report := range reports {
		if untiled(results.Group(report.Group)) {
			continue
		}
		see(tileOf(report), report.CreatedAt)
	}

	kinds := make([]string, 0, len(first))
	for kind := range first {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if !first[kinds[i]].Equal(first[kinds[j]]) {
			return first[kinds[i]].Before(first[kinds[j]])
		}
		return kinds[i] < kinds[j]
	})
	return kinds
}

// tileOf is the tile a result goes to: its pipeline's, or, for a result recorded before results
// named their pipeline, its group's.
func tileOf(report store.TestResult) string {
	if report.Capability != "" {
		return report.Capability
	}
	if report.Group != "" {
		return legacyTile + report.Group
	}
	return ""
}

// natureLabels name a legacy tile by its group, in French: it has no pipeline to name it.
var natureLabels = map[results.Group]string{
	results.GroupUnit:        "Tests unitaires",
	results.GroupIntegration: "Tests d'intégration",
	results.GroupE2E:         "Tests E2E",
	results.GroupStatic:      "Qualité du code",
	results.GroupSecurity:    "Sécurité",
	results.GroupLoad:        "Performance",
}

// legacyTile prefixes the tile of results that name no pipeline.
const legacyTile = "group:"

// untiled says whether a group's results stay out of the tiles: a review has its own section, a
// build's files are kept with the release.
func untiled(group results.Group) bool {
	return group == results.GroupReview || group == results.GroupBuild
}

func producesResults(pipeline catalog.Pipeline) bool {
	for _, product := range pipeline.Produces {
		if product == catalog.ProductResults {
			return true
		}
	}
	return false
}

// tileMeasure grades one pipeline's tile from its own results.
func tileMeasure(
	ctx context.Context, source scoreStore, scope store.Scope, kind string,
	reports []store.TestResult, registry *catalog.Catalog, weights map[string]float64,
) (results.Measure, error) {
	var own []store.TestResult
	for _, report := range reports {
		if tileOf(report) == kind {
			own = append(own, report)
		}
	}

	group, shape := tileKind(kind, own, registry)
	signal, known := results.SignalOf(group)
	measure := results.Measure{
		Key: "pipeline:" + kind, Label: kind, Signal: signal, Group: group, Shape: shape,
		Weight: weightOf(kind, signal, known, weights),
	}
	if group, legacy := strings.CutPrefix(kind, legacyTile); legacy {
		measure.Label = group
		if label, named := natureLabels[results.Group(group)]; named {
			measure.Label = label
		}
	}
	if pipeline, found := find(registry, kind); found {
		measure.Ref = pipeline.Kind
		if pipeline.Label != "" {
			measure.Label = pipeline.Label
		}
	}

	var graded results.Measure
	switch {
	case shape == shapeAttachments:
		// Files are shown, never graded.
		measure.Weight = 0
		graded = results.Measure{Measured: len(own) > 0, Value: 100, Detail: "files to consult, not graded"}
		if len(own) == 0 {
			graded = results.Measure{Detail: "not run"}
		}
	case shape == shapeFindings:
		audit, err := findingsMeasure(ctx, source, scope, kind)
		if err != nil {
			return results.Measure{}, err
		}
		graded = audit
	case shape == shapeMeasures:
		campaign, err := campaignMeasure(ctx, source, scope, kind)
		if err != nil {
			return results.Measure{}, err
		}
		graded = campaign
	case group == results.GroupStatic:
		combined, ran := cumulative(own)
		graded = results.FromStatic(combined.Failed, ran)
	default:
		combined, ran := cumulative(own)
		graded = results.FromReport(signal, combined, ran)
	}
	measure.Measured, measure.Value, measure.Detail = graded.Measured, graded.Value, graded.Detail
	return measure, nil
}

// weightOf is a pipeline's weight: the one set for it, otherwise its nature's, otherwise none.
func weightOf(kind string, signal results.Signal, known bool, weights map[string]float64) float64 {
	if weight, set := weights[kind]; set {
		return weight
	}
	if known {
		return results.WeightOf(nil, signal)
	}
	return 0
}

// Shapes the score reads differently: an audit, a load campaign, files it does not grade.
// Everything else is cases.
const (
	shapeFindings    = "findings"
	shapeMeasures    = "measures"
	shapeAttachments = "attachments"
)

// tileKind returns the group a pipeline's results go to and their shape.
//
// What it produced first, the catalogue second: a pipeline that ran says itself what it returned,
// and the catalogue serves the one still waiting for its first result.
func tileKind(kind string, own []store.TestResult, registry *catalog.Catalog) (results.Group, string) {
	var attached *store.TestResult
	for index, report := range own {
		if report.Group == "" {
			continue
		}
		if report.Shape == shapeAttachments {
			if attached == nil {
				attached = &own[index]
			}
			continue
		}
		shape := report.Shape
		if shape == "" {
			shape = defaultShape(results.Group(report.Group))
		}
		return results.Group(report.Group), shape
	}
	if attached != nil {
		return results.Group(attached.Group), shapeAttachments
	}
	if pipeline, found := find(registry, kind); found {
		group := results.Group(pipeline.Group())
		return group, defaultShape(group)
	}
	return "", ""
}

// defaultShape is the shape a group's results take before any is read.
func defaultShape(group results.Group) string {
	switch group {
	case results.GroupSecurity:
		return shapeFindings
	case results.GroupLoad:
		return shapeMeasures
	}
	return "cases"
}

// cumulative sums a pipeline's reports, one per tool, the latest of each.
//
// Keeping a single report would lose the pipelines with several tools: static analysis has two,
// and only the more recent would count.
func cumulative(own []store.TestResult) (results.Report, bool) {
	combined := results.Report{}
	seen := map[string]bool{}
	ran := false

	for _, report := range own {
		// The list is newest first: the first one met for a tool is the most recent.
		if seen[report.Tool] {
			continue
		}
		seen[report.Tool] = true
		ran = true

		combined.Total += report.Total
		combined.Passed += report.Passed
		combined.Failed += report.Failed
		combined.Skipped += report.Skipped
		combined.Flaky += report.Flaky
		// The harshest status wins: the milder would mask the other tool's.
		combined.Status = worstStatus(combined.Status, report.Status)
	}
	return combined, ran
}

// findingsMeasure grades a pipeline's latest audit.
//
// The audit decides for itself whether it is measurable: a failed scanner produces zero findings,
// exactly like a clean audit, and only its descriptor tells them apart.
func findingsMeasure(ctx context.Context, source scoreStore, scope store.Scope, kind string) (results.Measure, error) {
	audit, err := source.LatestFindingsOf(ctx, scope, kind)
	if errors.Is(err, store.ErrAuditNotFound) {
		return results.FromSecurity(0, 0, false), nil
	}
	if err != nil {
		return results.Measure{}, err
	}
	if !audit.Measured() {
		return results.Measure{Signal: results.SignalSecurity,
			Detail: "audit incomplet : " + strings.Join(audit.FailedScanners, ", ") + " did not conclude"}, nil
	}
	// The counters exclude findings suppressed by exception, a dated human decision.
	return results.FromSecurity(audit.Critical, audit.High, true), nil
}

// campaignMeasure grades a pipeline's latest load campaign against its baselines.
func campaignMeasure(ctx context.Context, source scoreStore, scope store.Scope, kind string) (results.Measure, error) {
	campaigns, err := source.ListLoadResults(ctx, scope)
	if err != nil {
		return results.Measure{}, err
	}
	var campaign *store.LoadResult
	for index := range campaigns {
		if campaigns[index].Capability == kind {
			campaign = &campaigns[index]
			break
		}
	}
	if campaign == nil {
		return results.FromBench("", false), nil
	}

	conditions := bench.Conditions{Exclusive: campaign.Exclusive, ConcurrentJobs: campaign.ConcurrentJobs}
	if !conditions.Comparable() {
		// Grading it would blame the release for slowness caused by a neighbour.
		return results.FromBench("", false), nil
	}

	measures, err := source.ListResultMeasures(ctx, campaign.ID)
	if err != nil {
		return results.Measure{}, err
	}
	baselines, err := source.Baselines(ctx, campaign.Scenario)
	if err != nil {
		return results.Measure{}, err
	}

	summary := bench.Summary{Scenario: campaign.Scenario}
	for _, measure := range measures {
		summary.Measures = append(summary.Measures, bench.Measure{
			Metric: measure.Metric, Value: measure.Value,
			LowerBetter: measure.LowerBetter, Volume: measure.Volume,
		})
	}
	values := map[string]float64{}
	for metric, baseline := range baselines {
		values[metric] = baseline.Value
	}
	return results.FromBench(string(bench.Worst(bench.Compare(summary, values, conditions))), true), nil
}

// find reads a pipeline from a catalogue that may be missing.
func find(registry *catalog.Catalog, kind string) (catalog.Pipeline, bool) {
	if registry == nil {
		return catalog.Pipeline{}, false
	}
	return registry.Find(kind)
}
