package http

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The score, step by step.
 *
 * # What changes axis
 *
 * The score weighed six **signals** of fixed weights. Two distinct E2E campaigns therefore merged
 * into one, and no weighting could separate them. The component is now the **step** of the
 * verification stage: two campaigns are two steps, each with its weight.
 *
 * The signal does not disappear. It still says **how** to score what was measured — a test report
 * is not scored like an audit — and still serves to pick an icon. It has only stopped being the
 * identity.
 *
 * # Where the attachment comes from
 *
 * Nothing was added to the schema: a result carries its run, a run carries its job, and a step is
 * the only thing carrying that job. `ResultGestes` follows that chain.
 *
 * # The weights are those of the plan that ran
 *
 * They are read from `plan_runs.plan`, frozen when the run was opened. Replaying the score of a
 * release delivered six months ago therefore uses that day's weights. A figure that was
 * communicated is not rewritten.
 */

// planScoreStore is what the per-step score needs from the database, beyond the rest.
type planScoreStore interface {
	PlanRunByEvent(ctx context.Context, trigger, eventKey string) (store.PlanRun, error)
	ResultGestes(ctx context.Context, releaseID int64) (map[int64]store.GesteRef, error)
}

// stepMeasures returns one score component per verification step.
//
// Returns nil, nil when the release has no plan, or when it verifies nothing: the caller
// then falls back to the families. That fallback is not a convenience — releases predating
// plans have none, and their score must stay readable.
func stepMeasures(
	ctx context.Context, source scoreStore, releaseID int64,
	reports []store.TestResult, registry *catalog.Catalog,
) ([]results.Measure, error) {
	plans, ok := source.(planScoreStore)
	if !ok {
		return nil, nil
	}

	run, err := plans.PlanRunByEvent(ctx,
		plan.ReleaseTrigger, event.ReleaseCreatedKey(releaseID))
	if err != nil {
		if errors.Is(err, store.ErrPlanRunNotFound) {
			return nil, nil
		}
		return nil, err
	}

	// The last verification stage, as in the life cycle: a plan may verify in two passes, and
	// it is the conclusion of the last that says the release is verified.
	stage := run.Plan.LastWithRole(plan.RoleTest)
	if stage < 0 || len(run.Plan.Stages[stage].Gestes) == 0 {
		return nil, nil
	}

	byResult, err := plans.ResultGestes(ctx, releaseID)
	if err != nil {
		return nil, err
	}

	reportsByGeste := map[store.GesteRef][]store.TestResult{}
	for _, report := range reports {
		if ref, attribue := byResult[report.ID]; attribue {
			reportsByGeste[ref] = append(reportsByGeste[ref], report)
		}
	}

	audit, auditGeste, err := stepAudit(ctx, source, releaseID, byResult)
	if err != nil {
		return nil, err
	}
	campagne, campagneGeste, err := stepCampaign(ctx, source, releaseID, byResult)
	if err != nil {
		return nil, err
	}

	measures := make([]results.Measure, 0, len(run.Plan.Stages[stage].Gestes))
	for position, step := range run.Plan.Stages[stage].Gestes {
		ref := store.GesteRef{Stage: stage, Position: position}
		siens := reportsByGeste[ref]

		famille := stepKind(step, siens, registry)
		signal, connu := results.SignalOf(famille)

		measure := results.Measure{
			Key: stepKey(ref), Label: step.Name(), Signal: signal,
			// What produced the measure: the pipeline the replay button relaunches, and the report
			// family the detail opens.
			Ref: step.Ref, Family: famille,
			Weight: results.WeightOf(step.Weight, signal),
		}

		switch {
		case !connu:
			// A step whose output cannot be read. Its weight stays displayed — otherwise one would
			// think it does not count — but it grades nothing, and the detail says why.
			measure.Detail = "no known result kind for this pipeline"
		case famille == results.FamilySecurity:
			measure.Measured, measure.Value, measure.Detail =
				valueOf(auditMeasure(audit, auditGeste == ref))
		case famille == results.FamilyLoad:
			mesure, err := campaignMeasure(ctx, source, campagne, campagneGeste == ref)
			if err != nil {
				return nil, err
			}
			measure.Measured, measure.Value, measure.Detail = valueOf(mesure)
		case famille == results.FamilyStatic:
			combine, ran := cumulative(siens)
			// Static analysis violations are counted as cases, not as passing tests: the scoring rule
			// is not the same.
			measure.Measured, measure.Value, measure.Detail =
				valueOf(results.FromStatic(combine.Failed, ran))
		default:
			combine, ran := cumulative(siens)
			measure.Measured, measure.Value, measure.Detail =
				valueOf(results.FromReport(signal, combine, ran))
		}

		measures = append(measures, measure)
	}
	return measures, nil
}

// stepKey composes the key of a component carried by a step.
//
// Its position in the frozen plan, and not its label: two steps may share one, and a
// renamed plan must not break matching a score with the cell that displays it.
func stepKey(ref store.GesteRef) string {
	return "geste:" + strconv.Itoa(ref.Stage) + "." + strconv.Itoa(ref.Position)
}

// familyOfStep returns the result family this step feeds.
//
// What it produced first, the catalogue second. The order matters: a step that ran says
// itself what it returned, and the catalogue may have changed since. The catalogue serves
// the step that produced nothing — the only way to know what default weight to give it, and
// therefore to display it rather than hide it.
func stepKind(
	step plan.Geste, siens []store.TestResult, registry *catalog.Catalog,
) results.Family {
	for _, report := range siens {
		if report.Family != "" {
			return results.Family(report.Family)
		}
	}
	if registry == nil {
		return ""
	}
	pipeline, found := registry.Find(step.Ref)
	if !found {
		return ""
	}
	return results.Family(pipeline.Family())
}

// accumulate sums a step's reports, one per tool.
//
// Keeping a single report would lose the pipelines with several tools. Static analysis has
// two, and only the more recent entered the score: a release with forty errors from one and
// a clean result from the other showed 100 as soon as the clean one concluded last.
func cumulative(siens []store.TestResult) (results.Report, bool) {
	combine := results.Report{}
	vus := map[string]bool{}
	ran := false

	for _, report := range siens {
		// The list is newest first: the first one met for a tool is the most recent, the rest are
		// its history.
		if vus[report.Tool] {
			continue
		}
		vus[report.Tool] = true
		ran = true

		combine.Total += report.Total
		combine.Passed += report.Passed
		combine.Failed += report.Failed
		combine.Skipped += report.Skipped
		combine.Flaky += report.Flaky
		// The harshest status wins: two tools of one step return a common verdict, and the milder
		// would mask the other.
		combine.Status = worstStatus(combine.Status, report.Status)
	}
	return combine, ran
}

// valueOf extracts what a measure says, to carry it onto a component.
func valueOf(measure results.Measure) (bool, float64, string) {
	return measure.Measured, measure.Value, measure.Detail
}

/*
 * The audit and the campaign, attached to the step that produced them.
 *
 * # An accepted limit
 *
 * Both are read as "the most recent of the release", and not "that of this step". Two security
 * steps in one plan would therefore not be scored separately: the second would declare itself not
 * measured, saying so, rather than display the first's value under its name.
 *
 * It is not the final shape. It is the only one that is **true** without a new query, and the case
 * it leaves aside exists in no deployed catalogue.
 */

func stepAudit(
	ctx context.Context, source scoreStore, releaseID int64,
	byResult map[int64]store.GesteRef,
) (store.SecurityAudit, store.GesteRef, error) {
	audit, err := source.LatestSecurityAudit(ctx, releaseID)
	if err != nil {
		if errors.Is(err, store.ErrAuditNotFound) {
			return store.SecurityAudit{}, store.GesteRef{Stage: -1}, nil
		}
		return store.SecurityAudit{}, store.GesteRef{}, err
	}
	ref, attribue := byResult[audit.TestResultID]
	if !attribue {
		return audit, store.GesteRef{Stage: -1}, nil
	}
	return audit, ref, nil
}

func stepCampaign(
	ctx context.Context, source scoreStore, releaseID int64,
	byResult map[int64]store.GesteRef,
) (*store.LoadResult, store.GesteRef, error) {
	campagnes, err := source.ListReleaseLoadResults(ctx, releaseID)
	if err != nil {
		return nil, store.GesteRef{}, err
	}
	if len(campagnes) == 0 {
		return nil, store.GesteRef{Stage: -1}, nil
	}
	ref, attribue := byResult[campagnes[0].ID]
	if !attribue {
		return &campagnes[0], store.GesteRef{Stage: -1}, nil
	}
	return &campagnes[0], ref, nil
}

// auditMeasure grades security from a step's audit.
//
// The audit decides for itself whether it is measurable: a failed scanner produces zero
// findings, exactly like a clean audit, and only its descriptor tells them apart. Counting
// it as clean would be the most expensive possible error on this signal.
func auditMeasure(audit store.SecurityAudit, sien bool) results.Measure {
	if !sien {
		return results.FromSecurity(0, 0, false)
	}
	if !audit.Measured() {
		return results.Measure{Signal: results.SignalSecurity,
			Detail: "audit incomplet : " + strings.Join(audit.FailedScanners, ", ") +
				" did not conclude"}
	}
	// The counters come from the summary report, which already excludes findings suppressed by
	// exception. An exception is a dated human decision: honouring it here is what makes the
	// score actionable rather than endured.
	return results.FromSecurity(audit.Critical, audit.High, true)
}

// campaignMeasure grades performance from a step's campaign.
func campaignMeasure(
	ctx context.Context, source scoreStore, campagne *store.LoadResult, sien bool,
) (results.Measure, error) {
	if !sien || campagne == nil {
		return results.FromBench("", false), nil
	}

	conditions := bench.Conditions{
		Exclusive: campagne.Exclusive, ConcurrentJobs: campagne.ConcurrentJobs,
	}
	if !conditions.Comparable() {
		// A campaign that cannot be compared is not an absent campaign, but it cannot grade
		// performance: grading it would blame the release for slowness caused by a neighbour.
		return results.FromBench("", false), nil
	}

	measures, err := source.ListResultMeasures(ctx, campagne.ID)
	if err != nil {
		return results.Measure{}, err
	}
	baselines, err := source.Baselines(ctx, campagne.Scenario)
	if err != nil {
		return results.Measure{}, err
	}

	summary := bench.Summary{Scenario: campagne.Scenario}
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

	return results.FromBench(string(bench.Worst(
		bench.Compare(summary, values, conditions))), true), nil
}
