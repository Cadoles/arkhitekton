package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/bench"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
)

/*
 * The plugins' interpreters set against the ingestion they replace.
 *
 * Same reports in, same facts out: cases with the same identity, status, message and trace, the
 * same verdict and the same summary line. The ingestion goes once these hold, and not before.
 */

func report(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "reports", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func record(t *testing.T, tool string, parser parse.ID, raw []byte) parse.Record {
	t.Helper()
	parsing, _ := parse.Lookup(parser)
	payload, err := parsing(raw)
	return parse.Record{Tool: tool, Parser: parser, Object: "raw/run/" + tool, Payload: payload, Err: err}
}

func interpret(t *testing.T, ref string, run capability.RunState, records ...parse.Record) []facts.Set {
	t.Helper()
	registry, err := builtin.Registry()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := capability.ParseRef(ref)
	pipeline, _, _ := registry.Pipeline(parsed)
	sets, err := pipeline.Interpreter.Interpret(context.Background(), run, records)
	if err != nil {
		t.Fatalf("%s: %v", ref, err)
	}
	for _, set := range sets {
		if err := set.Validate(); err != nil {
			t.Errorf("%s: invalid set: %v", ref, err)
		}
	}
	return sets
}

func sameReport(t *testing.T, legacy results.Report, set facts.Set) {
	t.Helper()
	if string(set.Verdict()) != legacy.Status {
		t.Errorf("%s: verdict %q, legacy %q", legacy.Tool, set.Verdict(), legacy.Status)
	}
	if set.Summary != legacy.Summary {
		t.Errorf("%s: summary %q, legacy %q", legacy.Tool, set.Summary, legacy.Summary)
	}
	if set.DurationMS != legacy.DurationMS {
		t.Errorf("%s: duration %d, legacy %d", legacy.Tool, set.DurationMS, legacy.DurationMS)
	}
	if len(set.Cases) != len(legacy.Cases) {
		t.Fatalf("%s: %d cases, legacy %d", legacy.Tool, len(set.Cases), len(legacy.Cases))
	}
	for i, before := range legacy.Cases {
		after := set.Cases[i]
		if after.Suite != before.Suite || after.Name != before.Name || string(after.Status) != string(before.Status) ||
			after.DurationMS != before.DurationMS || after.Message != before.Message || after.Trace != before.Trace {
			t.Errorf("%s case %d:\n  legacy %+v\n  now    %+v", legacy.Tool, i, before, after)
		}
	}
}

func TestUnitTestsReadAsBefore(t *testing.T) {
	raw := report(t, "junit.xml")
	legacy, err := results.ParseJUnit(results.FamilyUnit, "phpunit", raw)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Summary = results.SummarizeJUnit(legacy)

	sets := interpret(t, "php/unit-tests", capability.RunState{}, record(t, "phpunit", parse.JUnitXML, raw))
	if len(sets) != 1 || sets[0].Group != "unit" || sets[0].Tool != "phpunit" {
		t.Fatalf("sets = %+v", sets)
	}
	sameReport(t, legacy, sets[0])
}

func TestIntegrationTestsReadAsBefore(t *testing.T) {
	raw := report(t, "junit.xml")
	legacy, _ := results.ParseJUnit(results.FamilyIntegration, "phpunit-integration", raw)
	legacy.Summary = results.SummarizeJUnit(legacy)

	sets := interpret(t, "mse/integration-tests", capability.RunState{},
		record(t, "phpunit-integration", parse.JUnitXML, raw))
	if len(sets) != 1 || sets[0].Group != "integration" {
		t.Fatalf("sets = %+v", sets)
	}
	sameReport(t, legacy, sets[0])
}

func TestTheEndToEndCampaignReadsAsBefore(t *testing.T) {
	raw := report(t, "mochawesome.json")
	legacy, err := results.ParseMochawesome(raw)
	if err != nil {
		t.Fatal(err)
	}

	sets := interpret(t, "mse/e2e-tests", capability.RunState{}, record(t, "cypress", parse.MochawesomeJSON, raw))
	if len(sets) != 1 || sets[0].Group != "e2e" {
		t.Fatalf("sets = %+v", sets)
	}
	sameReport(t, legacy, sets[0])
}

func TestStaticAnalysisReadsAsBefore(t *testing.T) {
	phpstan, csFixer := report(t, "phpstan.json"), report(t, "cs-fixer.json")
	legacyPHPStan, _ := results.ParsePHPStan(phpstan)
	legacyCSFixer, _ := results.ParseCSFixer(csFixer)

	sets := interpret(t, "php/static-tests", capability.RunState{},
		record(t, "phpstan", parse.PHPStanJSON, phpstan), record(t, "cs-fixer", parse.CSFixerJSON, csFixer))
	if len(sets) != 2 {
		t.Fatalf("sets = %+v", sets)
	}
	sameReport(t, legacyPHPStan, sets[0])
	sameReport(t, legacyCSFixer, sets[1])
}

// A clean analysis has no case and still passes: it ran and found nothing.
func TestACleanAnalysisPassesAsBefore(t *testing.T) {
	raw := report(t, "phpstan-clean.json")
	legacy, _ := results.ParsePHPStan(raw)

	sets := interpret(t, "php/static-tests", capability.RunState{}, record(t, "phpstan", parse.PHPStanJSON, raw))
	if len(sets) != 1 {
		t.Fatalf("sets = %+v", sets)
	}
	sameReport(t, legacy, sets[0])
}

// The distinction the run's state now carries: a tool that ran and left nothing readable did not
// conclude, a tool that never started was not measured.
func TestATaskThatRanWithoutReportDidNotConclude(t *testing.T) {
	killed := int32(137)
	run := capability.RunState{Tasks: []capability.TaskState{{
		Name: "run-unit-tests", Status: "failed", Reason: "OOMKilled",
		Steps: []capability.StepState{{Name: "exec", Status: "failed", ExitCode: &killed}},
	}}}

	sets := interpret(t, "php/unit-tests", run)
	if len(sets) != 1 || sets[0].Verdict() != facts.StatusNA || sets[0].Summary != "the tool did not conclude (code 137): OOMKilled" {
		t.Errorf("sets = %+v", sets)
	}

	if sets := interpret(t, "php/unit-tests", capability.RunState{}); len(sets) != 0 {
		t.Errorf("a task that never started produced %+v", sets)
	}
}

func TestAnUnreadableReportDidNotConclude(t *testing.T) {
	crashed := int32(255)
	run := capability.RunState{Tasks: []capability.TaskState{{
		Name: "run-phpstan", Status: "failed",
		Steps: []capability.StepState{{Name: "exec", Status: "failed", ExitCode: &crashed}},
	}}}

	sets := interpret(t, "php/static-tests", run, record(t, "phpstan", parse.PHPStanJSON, []byte("{truncated")))
	if len(sets) != 1 || sets[0].Tool != "phpstan" || sets[0].Verdict() != facts.StatusNA {
		t.Errorf("sets = %+v", sets)
	}
}

func TestTheSecurityAuditReadsAsBefore(t *testing.T) {
	sets := interpret(t, "security/security-audit", capability.RunState{},
		record(t, "security-audit", parse.SecuritySummaryJSON, report(t, "summary.json")),
		record(t, "security-audit", parse.SecurityFindingsJSONL, report(t, "findings.jsonl")))
	if len(sets) != 1 {
		t.Fatalf("sets = %+v", sets)
	}
	set := sets[0]
	if set.Verdict() != facts.StatusFail ||
		set.Summary != "5 finding(s), 1 critique(s), 2 haut(s), 2 nouveau(x), 1 suppressed by exception" {
		t.Errorf("verdict %q, summary %q", set.Verdict(), set.Summary)
	}
	audit := set.Findings
	if audit.BySeverity[facts.SeverityHigh] != 2 || audit.DeltaResolved != 1 || audit.PreviousRunID != "20260830T100000Z" {
		t.Errorf("audit = %+v", audit)
	}
	if len(audit.Items) != 2 || audit.Items[0].Location != "composer.lock:42" || audit.Items[0].PackageFixed != "1.1" {
		t.Errorf("first finding = %+v", audit.Items[0])
	}
	if second := audit.Items[1]; second.Severity != facts.SeverityInfo || !second.Suppressed ||
		second.SuppressionReason != "accepted" {
		t.Errorf("second finding = %+v", second)
	}
}

func TestALoadCampaignReadsAsBefore(t *testing.T) {
	raw := report(t, "k6-summary.json")
	legacy, err := bench.ParseSummary("checkout", raw, nil)
	if err != nil {
		t.Fatal(err)
	}

	load := record(t, "k6", parse.K6SummaryJSON, raw)
	load.Object = "raw/run/load/checkout-summary.json"
	duplicate := load
	duplicate.Object = "raw/run/k6/checkout.json"
	sets := interpret(t, "k6/load", capability.RunState{}, load, duplicate)
	if len(sets) != 1 {
		t.Fatalf("one scenario published twice gave %d sets", len(sets))
	}
	measures := sets[0].Measures
	if measures.Scenario != "checkout" || measures.VUs != legacy.VUs || len(measures.Items) != len(legacy.Measures) {
		t.Fatalf("measures = %+v, legacy %+v", measures, legacy)
	}
	for i, before := range legacy.Measures {
		after := measures.Items[i]
		if after.Metric != before.Metric || after.Label != before.Label || after.Unit != before.Unit ||
			after.Value != before.Value || after.LowerBetter != before.LowerBetter || after.Volume != before.Volume ||
			(after.ThresholdOK == nil) != (before.ThresholdOK == nil) ||
			(after.ThresholdOK != nil && *after.ThresholdOK != *before.ThresholdOK) {
			t.Errorf("measure %d:\n  legacy %+v\n  now    %+v", i, before, after)
		}
	}
	if sets[0].Verdict() != facts.StatusFail ||
		sets[0].Summary != "5 mesure(s), 50 VUs at peak, seuils non tenus : http_req_duration: p(95)<300" {
		t.Errorf("verdict %q, summary %q", sets[0].Verdict(), sets[0].Summary)
	}
}
