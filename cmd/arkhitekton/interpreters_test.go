package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/parsers"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

/*
 * The built-in plugins' interpreters, on reference reports.
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
	parsing, _ := parsers.Lookup(parser)
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

func TestTheSecurityAuditReadsTheProducersReport(t *testing.T) {
	sets := interpret(t, "security/security-audit", capability.RunState{},
		record(t, "security-audit", parse.SecuritySummaryJSON, report(t, "summary.json")),
		record(t, "security-audit", parse.SecurityFindingsJSONL, report(t, "findings.jsonl")))
	if len(sets) != 1 {
		t.Fatalf("sets = %+v", sets)
	}
	set := sets[0]
	if set.Verdict() != facts.StatusFail ||
		set.Summary != "5 finding(s), 1 critical, 2 high, 2 new, 1 suppressed by exception" {
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

// One scenario published under `load/` and under `k6/` is one campaign, not two history points.
func TestAScenarioPublishedTwiceIsReadOnce(t *testing.T) {
	load := record(t, "k6", parse.K6SummaryJSON, report(t, "k6-summary.json"))
	load.Object = "raw/run/load/checkout-summary.json"
	duplicate := load
	duplicate.Object = "raw/run/k6/checkout.json"

	sets := interpret(t, "k6/load", capability.RunState{}, load, duplicate)
	if len(sets) != 1 || sets[0].Measures.Scenario != "checkout" {
		t.Fatalf("sets = %+v", sets)
	}
}

// What each built-in interpreter reads from the reference reports, recorded. They were recorded
// while the readings equalled the ingestion they replaced; a change now shows as a diff to review.
func TestInterpretersReadAsRecorded(t *testing.T) {
	load := record(t, "k6", parse.K6SummaryJSON, report(t, "k6-summary.json"))
	load.Object = "raw/run/load/checkout-summary.json"

	cases := map[string][]facts.Set{
		"unit-tests.json": interpret(t, "php/unit-tests", capability.RunState{},
			record(t, "phpunit", parse.JUnitXML, report(t, "junit.xml"))),
		"integration-tests.json": interpret(t, "mse/integration-tests", capability.RunState{},
			record(t, "phpunit-integration", parse.JUnitXML, report(t, "junit.xml"))),
		"e2e-tests.json": interpret(t, "mse/e2e-tests", capability.RunState{},
			record(t, "cypress", parse.MochawesomeJSON, report(t, "mochawesome.json"))),
		"static-tests.json": interpret(t, "php/static-tests", capability.RunState{},
			record(t, "phpstan", parse.PHPStanJSON, report(t, "phpstan.json")),
			record(t, "cs-fixer", parse.CSFixerJSON, report(t, "cs-fixer.json"))),
		"static-tests-clean.json": interpret(t, "php/static-tests", capability.RunState{},
			record(t, "phpstan", parse.PHPStanJSON, report(t, "phpstan-clean.json"))),
		"security-audit.json": interpret(t, "security/security-audit", capability.RunState{},
			record(t, "security-audit", parse.SecuritySummaryJSON, report(t, "summary.json")),
			record(t, "security-audit", parse.SecurityFindingsJSONL, report(t, "findings.jsonl"))),
		"load.json": interpret(t, "k6/load", capability.RunState{}, load),
	}
	for name, sets := range cases {
		t.Run(name, func(t *testing.T) { golden(t, filepath.Join("facts", name), sets) })
	}
}

// PHP-CS-Fixer exits 8 when files need fixing: a result. Any other non-zero code is a failure of
// the tool, even with a report on disk.
func TestCSFixerFailsOnAnUnexpectedExitCode(t *testing.T) {
	stateWith := func(code int32) capability.RunState {
		return capability.RunState{Tasks: []capability.TaskState{{
			Name: "run-cs-fixer", Steps: []capability.StepState{{Name: "exec", ExitCode: &code}},
		}}}
	}
	fixes := record(t, "cs-fixer", parse.CSFixerJSON, report(t, "cs-fixer.json"))

	if sets := interpret(t, "php/static-tests", stateWith(8), fixes); len(sets) != 1 || sets[0].Verdict() != facts.StatusFail {
		t.Errorf("exit 8: %+v", sets)
	}
	if sets := interpret(t, "php/static-tests", stateWith(16), fixes); len(sets) != 1 || sets[0].Verdict() != facts.StatusNA {
		t.Errorf("exit 16: %+v", sets)
	}
}
