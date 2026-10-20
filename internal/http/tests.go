package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// TestStore is what the results handlers need from the database.
type TestStore interface {
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	// The two ways of naming a project: by the request's path, or failing that by the one
	// being built. See internal/http/project.go.
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
	ListReleaseTestResults(ctx context.Context, releaseID int64) ([]store.TestResult, error)
	ListTestCases(ctx context.Context, resultID int64, page store.CasePage) ([]store.TestCase, int, error)
	DiffGroup(ctx context.Context, releaseID, previousID int64, group string) ([]store.CaseDiff, error)
	PreviousRelease(ctx context.Context, releaseID int64) (int64, error)
	LatestSecurityAudit(ctx context.Context, releaseID int64) (store.SecurityAudit, error)
	ListSecurityFindings(ctx context.Context, resultID int64, includeSuppressed bool) ([]store.SecurityFinding, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
}

// TestsHandler exposes test results and the quality score.
type TestsHandler struct {
	store TestStore
	// catalog serves to name the family of a step that produced nothing: without it, a
	// pipeline that did not run would have neither signal nor default weight, and its cell
	// would disappear instead of saying it is waiting.
	catalog *catalog.Catalog
}

// NewTestsHandler assembles the handler.
//
// The weighting is no longer received here: it is declared on the automation's steps, and
// each release's frozen plan carries it. What remains of the catalogue's weighting is the
// per-signal default.
func NewTestsHandler(tests TestStore, registry *catalog.Catalog) *TestsHandler {
	return &TestsHandler{store: tests, catalog: registry}
}

type testResultPayload struct {
	ID int64 `json:"id"`
	// Group is where the result is shown, Shape how it is compared, Producer which plugin version
	// read it.
	Group      string    `json:"group"`
	Shape      string    `json:"shape"`
	Producer   string    `json:"producer"`
	Tool       string    `json:"tool"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Flaky      int       `json:"flaky"`
	DurationMS int64     `json:"duration_ms"`
	Summary    string    `json:"summary,omitempty"`
	MeasuredAt time.Time `json:"measured_at"`
}

type signalPayload struct {
	// Key identifies the component: the step that measured — `geste:1.0` — or the family for a release
	// predating plans. It is what makes the screen dynamic: one cell per component returned,
	// and not six cells written in advance.
	Key string `json:"key"`
	// Label names it, in the words of whoever declared it.
	Label string `json:"label"`
	// Signal says the nature of what is measured, and still picks an icon. It has stopped
	// being the identity: two steps may share it.
	Signal string `json:"signal"`
	// Ref is the pipeline family to re-run in order to measure this component again. Empty
	// when nothing re-measures it from the dashboard.
	Ref string `json:"ref,omitempty"`
	// Group is the results to open in detail, and Shape how they read: test cases, findings or
	// measures.
	Group string  `json:"group,omitempty"`
	Shape string  `json:"shape,omitempty"`
	Value float64 `json:"value"`
	// Weight is the effective weight, after redistributing the unmeasured signals. It is what
	// explains the real contribution to the score.
	Weight float64 `json:"weight"`
	// Declared is the weight as typed, before redistribution. Without it a weighting cannot
	// be read back: "33" does not say whether someone wrote 20 or 40.
	Declared float64 `json:"declared"`
	Measured bool    `json:"measured"`
	Detail   string  `json:"detail,omitempty"`
	// Health is the signal's verdict — `healthy`, `warn`, `critique` or `na`. Returned by the
	// server so the interface derives no threshold.
	Health string `json:"health"`
}

// Tests returns a release's reports and the score they compose.
func (h *TestsHandler) Tests(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	reports, err := h.store.ListReleaseTestResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the results", "")
		return
	}

	payload := make([]testResultPayload, 0, len(reports))
	for _, report := range reports {
		payload = append(payload, testResultPayload{
			ID: report.ID, Group: report.Group, Shape: report.Shape, Producer: report.Producer,
			Tool: report.Tool, Status: report.Status,
			Total: report.Total, Passed: report.Passed, Failed: report.Failed,
			Skipped: report.Skipped, Flaky: report.Flaky, DurationMS: report.DurationMS,
			Summary: report.Summary, MeasuredAt: report.CreatedAt,
		})
	}

	breakdown, err := h.score(r.Context(), release.ID, reports)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot compute the score", "")
		return
	}

	signals := make([]signalPayload, 0, len(breakdown.Signals))
	for _, signal := range breakdown.Signals {
		signals = append(signals, signalPayload{
			Key: signal.Key, Label: signal.Label,
			Signal: string(signal.Signal), Ref: signal.Ref, Group: string(signal.Group), Shape: signal.Shape,
			Value: signal.Value, Weight: signal.Weight,
			Declared: signal.Declared, Measured: signal.Measured, Detail: signal.Detail,
			// A signal's verdict comes from the server like the score's: deriving it client-side
			// would make it a second opinion, diverging at the first threshold change.
			Health: results.SignalHealth(signal),
		})
	}

	// Already strings: these are the keys of the components left out of the computation.
	redistributed := breakdown.Redistributed
	if redistributed == nil {
		redistributed = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": payload,
		"score": map[string]any{
			"value":    breakdown.Score,
			"measured": breakdown.Measured,
			"health":   results.Health(breakdown),
			"signals":  signals,
			// The reader must know what the score they read covers: a 100 over three signals is not
			// a 100 over six.
			"redistributed": redistributed,
		},
	})
}

// severity orders report statuses, from the mildest to the harshest.
var severity = map[string]int{"pass": 0, "warn": 1, "fail": 2}

// worstStatus returns the harsher of two statuses.
//
// An unknown status is treated as the harshest: better to flag wrongly than to silently
// absolve a verdict one cannot read.
func worstStatus(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	rankA, knownA := severity[a]
	rankB, knownB := severity[b]
	switch {
	case !knownA:
		return a
	case !knownB:
		return b
	case rankB > rankA:
		return b
	default:
		return a
	}
}

// score composes a release's score from what was measured.
func (h *TestsHandler) score(
	ctx context.Context, releaseID int64, reports []store.TestResult,
) (results.Breakdown, error) {
	return computeScore(ctx, h.store, releaseID, reports, h.catalog)
}

// scoreStore is what computing the score needs.
//
// An interface rather than a concrete type because two handlers call it: the tests one,
// for a release's screen, and the releases one, for the list's badge. The same figure must
// come out of both, and two implementations would have diverged.
type scoreStore interface {
	LatestSecurityAudit(ctx context.Context, releaseID int64) (store.SecurityAudit, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
}

func computeScore(
	ctx context.Context, source scoreStore, releaseID int64,
	reports []store.TestResult, registry *catalog.Catalog,
) (results.Breakdown, error) {
	// The steps first: that is the intended axis, and the only one able to separate two
	// campaigns of the same nature.
	measures, err := stepMeasures(ctx, source, releaseID, reports, registry)
	if err != nil {
		return results.Breakdown{}, err
	}

	if measures == nil {
		// The release has no plan. It predates plans, or its tests were launched by hand from the
		// queue. Its score is computed by family, as before — losing it would be worse than
		// computing it on an axis that no longer exists.
		if measures, err = familyMeasures(ctx, source, releaseID, reports); err != nil {
			return results.Breakdown{}, err
		}
	}

	return results.Score(measures), nil
}

// familyMeasures returns one component per family, for a release with no plan.
//
// Each family is looked up explicitly rather than derived from a loop over the reports: a
// missing family must produce a "did not run" measure, and a loop over what exists would
// produce nothing at all — hence a score computed over a silent subset.
func familyMeasures(
	ctx context.Context, source scoreStore, releaseID int64, reports []store.TestResult,
) ([]results.Measure, error) {
	// The last report of each tool, then the sum per family.
	//
	// Keeping a single report per family lost the families with several tools. Static analysis
	// has two, and only the more recent of them entered the score: a release with forty errors
	// from one and a clean result from the other showed 100 as soon as the clean one concluded
	// last.
	byGroup := map[string]results.Report{}
	seen := map[string]bool{}
	seenTool := map[string]bool{}

	for _, report := range reports {
		key := report.Group + "/" + report.Tool
		if seenTool[key] {
			continue
		}
		seenTool[key] = true
		seen[report.Group] = true

		combined := byGroup[report.Group]
		combined.Total += report.Total
		combined.Passed += report.Passed
		combined.Failed += report.Failed
		combined.Skipped += report.Skipped
		combined.Flaky += report.Flaky
		combined.Status = worstStatus(combined.Status, report.Status)
		byGroup[report.Group] = combined
	}

	// Static analysis violations are counted as cases, not as passing tests: the scoring rule
	// is not the same.
	staticViolations, staticRan := 0, seen[string(results.GroupStatic)]
	if staticRan {
		staticViolations = byGroup[string(results.GroupStatic)].Failed
	}

	raw := []results.Measure{
		results.FromReport(results.SignalUnit,
			byGroup[string(results.GroupUnit)], seen[string(results.GroupUnit)]),
		results.FromReport(results.SignalIntegration,
			byGroup[string(results.GroupIntegration)], seen[string(results.GroupIntegration)]),
		results.FromReport(results.SignalE2E,
			byGroup[string(results.GroupE2E)], seen[string(results.GroupE2E)]),
		results.FromStatic(staticViolations, staticRan),
	}

	security, err := securityMeasure(ctx, source, releaseID)
	if err != nil {
		return nil, err
	}
	raw = append(raw, security)

	performance, err := benchMeasure(ctx, source, releaseID)
	if err != nil {
		return nil, err
	}
	raw = append(raw, performance)

	// The identity and the weight, which the measuring functions do not set: they say what was
	// measured, not what it is worth in the score.
	measures := make([]results.Measure, 0, len(raw))
	for _, measure := range raw {
		measure.Key = results.SignalKey(measure.Signal)
		measure.Label = signalLabels[measure.Signal]
		measure.Weight = results.WeightOf(nil, measure.Signal)
		// The pipeline and the group of the fallback, derived from the signal: here and nowhere
		// else, because this is the only path with no step to say them.
		measure.Ref = signalKinds[measure.Signal]
		measure.Group = signalGroups[measure.Signal]
		measure.Shape = defaultShape(measure.Group)
		measures = append(measures, measure)
	}
	return measures, nil
}

// signalLabels names the fallback components by group, in French.
//
// Written here because they have no declaration: they are natures, not steps. A step
// carries the label somebody gave it.
//
// signalKinds and signalGroups complete the per-family fallback.
//
// The pipeline family to re-run, and the report family to open. Empty for performance,
// whose campaigns come from an outside runner the dashboard does not trigger.
var signalKinds = map[results.Signal]string{
	results.SignalUnit:        "php/unit-tests",
	results.SignalIntegration: "mse/integration-tests",
	results.SignalE2E:         "mse/e2e-tests",
	results.SignalCode:        "php/static-tests",
	results.SignalSecurity:    "security/security-audit",
}

var signalGroups = map[results.Signal]results.Group{
	results.SignalUnit:        results.GroupUnit,
	results.SignalIntegration: results.GroupIntegration,
	results.SignalE2E:         results.GroupE2E,
	results.SignalCode:        results.GroupStatic,
	results.SignalSecurity:    results.GroupSecurity,
	results.SignalPerformance: results.GroupLoad,
}

var signalLabels = map[results.Signal]string{
	results.SignalUnit:        "Tests unitaires",
	results.SignalIntegration: "Tests d'intégration",
	results.SignalE2E:         "Tests E2E",
	results.SignalCode:        "Qualité du code",
	results.SignalSecurity:    "Sécurité",
	results.SignalPerformance: "Performance",
}

// securityMeasure returns the security signal of the latest audit.
//
// The audit decides for itself whether it is measurable: a failed scanner produces zero
// findings, exactly like a clean audit, and only its descriptor tells them apart. Counting
// it as clean would be the most expensive possible error on this signal.
func securityMeasure(
	ctx context.Context, source scoreStore, releaseID int64,
) (results.Measure, error) {
	audit, err := source.LatestSecurityAudit(ctx, releaseID)
	if err != nil {
		if errors.Is(err, store.ErrAuditNotFound) {
			return results.FromSecurity(0, 0, false), nil
		}
		return results.Measure{}, err
	}

	if !audit.Measured() {
		return results.Measure{Signal: results.SignalSecurity,
			Detail: "audit incomplet : " + strings.Join(audit.FailedScanners, ", ") + " did not conclude"}, nil
	}

	// The counters come from the summary report, which already excludes findings suppressed by
	// exception. An exception is a dated human decision: honouring it here is what makes the
	// score actionable rather than endured.
	return results.FromSecurity(audit.Critical, audit.High, true), nil
}

// benchMeasure returns the performance signal of the most recent campaign.
func benchMeasure(
	ctx context.Context, source scoreStore, releaseID int64,
) (results.Measure, error) {
	campaigns, err := source.ListReleaseLoadResults(ctx, releaseID)
	if err != nil {
		return results.Measure{}, err
	}
	if len(campaigns) == 0 {
		return results.FromBench("", false), nil
	}

	campaign := campaigns[0]
	conditions := bench.Conditions{
		Exclusive: campaign.Exclusive, ConcurrentJobs: campaign.ConcurrentJobs,
	}
	if !conditions.Comparable() {
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
	values := map[string]float64{}
	for _, measure := range measures {
		summary.Measures = append(summary.Measures, bench.Measure{
			Metric: measure.Metric, Value: measure.Value,
			LowerBetter: measure.LowerBetter, Volume: measure.Volume,
		})
	}
	for metric, baseline := range baselines {
		values[metric] = baseline.Value
	}

	verdict := bench.Worst(bench.Compare(summary, values, conditions))
	return results.FromBench(string(verdict), true), nil
}

type caseDiffPayload struct {
	Suite  string `json:"suite"`
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	Was    string `json:"was,omitempty"`
	Change string `json:"change"`
}

// casePageSize bounds a page of test cases.
//
// Five hundred rows fit in a usable table and cover every family except an end-to-end suite
// on a complete release. The size is not negotiable by the caller: a free `limit` in the URL
// would make the bound decorative.
const casePageSize = 500

type casePayload struct {
	Suite      string `json:"suite"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	Trace      string `json:"trace,omitempty"`
}

// Group returns a group's cases and its comparison with the previous release.
//
// The comparison is the view that answers the question actually asked before shipping —
// "what changed?" — and not "how many tests pass". A stable counter can hide a resolved
// failure and a new one.
func (h *TestsHandler) Group(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	group := r.PathValue("group")
	page := store.CasePage{
		Limit:  casePageSize,
		Offset: int(intQuery(r, "offset", 0)),
		Query:  r.URL.Query().Get("q"),
	}

	reports, err := h.store.ListReleaseTestResults(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the results", "")
		return
	}

	cases := make([]casePayload, 0, casePageSize)
	total := 0
	// The run that produced the displayed report. Returned with the cases so the reader can
	// open the run — its logs say why a test failed where the report only says it failed.
	var runName, tool string
	var measuredAt time.Time
	for _, report := range reports {
		if report.Group != group {
			continue
		}
		stored, count, err := h.store.ListTestCases(r.Context(), report.ID, page)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot read the cases", "")
			return
		}
		total = count
		for _, testCase := range stored {
			cases = append(cases, casePayload{
				Suite: testCase.Suite, Name: testCase.Name, Status: testCase.Status,
				DurationMS: testCase.DurationMS,
				Message:    testCase.FailureMessage, Trace: testCase.FailureTrace,
			})
		}
		runName, tool, measuredAt = report.RunName, report.Tool, report.CreatedAt
		// The group's most recent report is enough: it is what describes the release's state.
		break
	}

	// The comparison is not paginated: it is already reduced to what changed, and that is the
	// part one comes to read. Truncating it would hide a new failure because it falls on a
	// second page.
	response := map[string]any{
		"group":  group,
		"cases":  cases,
		"total":  total,
		"limit":  casePageSize,
		"offset": page.Offset,
	}
	if runName != "" {
		response["run_name"] = runName
	}
	if tool != "" {
		response["tool"] = tool
		response["measured_at"] = measuredAt
	}

	previous, err := h.store.PreviousRelease(r.Context(), release.ID)
	if err == nil {
		diffs, err := h.store.DiffGroup(r.Context(), release.ID, previous, group)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Cannot compare with the previous release", "")
			return
		}

		changed := make([]caseDiffPayload, 0, len(diffs))
		for _, diff := range diffs {
			// `unchanged` is the bulk of the volume and teaches nothing: the view exists for what
			// moved.
			if diff.Change == store.ChangeUnchanged {
				continue
			}
			changed = append(changed, caseDiffPayload{
				Suite: diff.Suite, Name: diff.Name,
				Status: diff.Status, Was: diff.Was, Change: diff.Change,
			})
		}
		response["diff"] = changed
	}
	// No previous release: no comparison, and that is not an error. A project's first release
	// has nothing to compare against.

	writeJSON(w, http.StatusOK, response)
}

type findingPayload struct {
	Fingerprint string  `json:"fingerprint"`
	Severity    string  `json:"severity"`
	Title       string  `json:"title"`
	Tool        string  `json:"tool"`
	Category    string  `json:"category"`
	RuleID      string  `json:"rule_id"`
	CVSS        float64 `json:"cvss,omitempty"`
	Target      string  `json:"target,omitempty"`
	Location    string  `json:"location,omitempty"`
	Package     string  `json:"package,omitempty"`
	// Installed and Fixed together: what decides an action is the gap between the installed
	// version and the one that fixes it.
	Installed string `json:"installed,omitempty"`
	Fixed     string `json:"fixed,omitempty"`
	Fixable   bool   `json:"fixable"`
	// Status is `new`, `existing` or `resolved`, established by the tool by comparing with the
	// previous run. That is what separates a regression from known debt.
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	Suppressed  bool   `json:"suppressed"`
	// Reason always accompanies a suppression: an exception with no reason is indefensible in
	// review.
	SuppressionReason  string `json:"suppression_reason,omitempty"`
	SuppressionExpires string `json:"suppression_expires,omitempty"`
}

// Security returns a release's latest audit and its findings.
func (h *TestsHandler) Security(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}

	audit, err := h.store.LatestSecurityAudit(r.Context(), release.ID)
	if err != nil {
		if errors.Is(err, store.ErrAuditNotFound) {
			// No audit is a legitimate state, distinct from an empty audit: say so rather than return
			// zeroed counters, which would read as "found nothing".
			writeJSON(w, http.StatusOK, map[string]any{"audited": false})
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the audit", "")
		return
	}

	// Suppressed findings are included: the record of what was set aside, and under which
	// exception, is worth as much as the list of what remains. The interface separates them, it
	// does not hide them.
	findings, err := h.store.ListSecurityFindings(r.Context(), audit.TestResultID, true)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the findings", "")
		return
	}

	payload := make([]findingPayload, 0, len(findings))
	for _, finding := range findings {
		payload = append(payload, findingPayload{
			Fingerprint: finding.Fingerprint, Severity: finding.Severity,
			Title: finding.Title, Tool: finding.Tool, Category: finding.Category,
			RuleID: finding.RuleID, CVSS: finding.CVSS,
			Target:   finding.TargetName,
			Location: finding.Location, Package: finding.PackageName,
			Installed: finding.PackageInstalled, Fixed: finding.PackageFixed,
			Fixable: finding.Fixable, Status: finding.Status,
			Description: finding.Description, Suppressed: finding.Suppressed,
			SuppressionReason:  finding.SuppressionReason,
			SuppressionExpires: finding.SuppressionExpires,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"audited":  true,
		"run_id":   audit.RunID,
		"run_name": audit.RunName,
		// Measured says whether the audit can pronounce a verdict. False, the counters stay
		// displayed but grade nothing: a partial audit showing "clean" is the worst possible result
		// here.
		"measured":        audit.Measured(),
		"failed_scanners": audit.FailedScanners,
		"scanner_status":  audit.ScannerStatus,
		"counts": map[string]int{
			"total": audit.Total, "critical": audit.Critical, "high": audit.High,
			"medium": audit.Medium, "low": audit.Low, "suppressed": audit.Suppressed,
		},
		"delta": map[string]int{
			"new": audit.DeltaNew, "existing": audit.DeltaExisting,
			"resolved": audit.DeltaResolved,
		},
		"previous_run_id": audit.PreviousRunID,
		"policy":          audit.Policy,
		"generated_at":    audit.GeneratedAt,
		"findings":        payload,
	})
}
