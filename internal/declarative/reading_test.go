package declarative

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// spec reads phpstan and phpunit reports from one archive.
var spec = capability.PipelineSpec{Artifacts: []capability.Artifact{{
	Name: "results", Format: capability.FormatArchive, Members: []capability.Member{
		{Match: "*/phpstan.json", Parser: parse.PHPStanJSON, Tool: "phpstan"},
		{Match: "*/junit.xml", Parser: parse.JUnitXML, Tool: "phpunit"},
	},
}}}

const reading = `
apiVersion: arkhitekton.reading/v1
tools:
  - tool: phpstan
    task: run-phpstan
    group: static
    summary: { violations: "violation(s)" }
    empty: pass
    exitCodes: { results: [0, 1] }
    cases:
      - from: phpstan.messages
        suite: "{file}"
        name: "{identifier | or: message | firstline}"
        status: failed
        message: "line {line}: {message | firstline}"
  - tool: phpunit
    task: run-unit-tests
    group: unit
    summary: tests
    duration: "{duration_ms}"
    cases:
      - from: junit.cases
        suite: "{classname | or: path}"
        name: "{name}"
        duration: "{duration_ms}"
        status:
          from: outcome
          map: { passed: passed, failed: failed, errored: failed, rerun: flaky, skipped: skipped }
`

func compile(t *testing.T, source string) capability.Interpreter {
	t.Helper()
	interpreter, err := Compile([]byte(source), spec)
	if err != nil {
		t.Fatal(err)
	}
	return interpreter
}

func code(value int32) *int32 { return &value }

func TestAReportBecomesCasesAsDeclared(t *testing.T) {
	report := parse.PHPStan{Files: []parse.PHPStanFile{{Path: "src/A.php", Messages: []parse.PHPStanMessage{
		{Message: "Dead code.\nmore", Line: 9, Identifier: "deadCode.unreachable"},
		{Message: "Variable $x might not be defined.", Line: 4},
	}}}}
	sets, err := compile(t, reading).Interpret(context.Background(), capability.RunState{},
		[]parse.Record{{Tool: "phpstan", Parser: parse.PHPStanJSON, Object: "raw/run/results.tar.gz", Payload: report}})
	if err != nil || len(sets) != 1 {
		t.Fatalf("sets %+v, err %v", sets, err)
	}
	set := sets[0]
	if set.Group != "static" || set.RawObject != "raw/run/results.tar.gz" || set.Summary != "2 violation(s) in 1 file(s)" ||
		set.Verdict() != facts.StatusFail {
		t.Errorf("set = %+v", set)
	}
	if first := set.Cases[0]; first.Suite != "src/A.php" || first.Name != "deadCode.unreachable" ||
		first.Message != "line 9: Dead code." {
		t.Errorf("first case = %+v", first)
	}
	if second := set.Cases[1]; second.Name != "Variable $x might not be defined." {
		t.Errorf("second case = %+v", second)
	}
}

// A clean analysis passed; one whose tool exited with a code outside the results did not conclude;
// a task that ran with no readable report did not conclude either; one that never started says
// nothing.
func TestTheRunsStateDecidesWhatTheReportCannot(t *testing.T) {
	interpreter := compile(t, reading)
	clean := []parse.Record{{Tool: "phpstan", Parser: parse.PHPStanJSON, Payload: parse.PHPStan{}}}

	sets, _ := interpreter.Interpret(context.Background(), capability.RunState{}, clean)
	if len(sets) != 1 || sets[0].Verdict() != facts.StatusPass {
		t.Errorf("clean: %+v", sets)
	}

	crashed := capability.RunState{Tasks: []capability.TaskState{{Name: "run-phpstan",
		Steps: []capability.StepState{{Name: "exec", ExitCode: code(255)}}}}}
	sets, _ = interpreter.Interpret(context.Background(), crashed, clean)
	if len(sets) != 1 || sets[0].Verdict() != facts.StatusNA || sets[0].Summary != "the tool did not conclude (code 255)" {
		t.Errorf("crashed: %+v", sets)
	}

	killed := capability.RunState{Tasks: []capability.TaskState{{Name: "run-unit-tests", Reason: "OOMKilled",
		Steps: []capability.StepState{{Name: "exec", ExitCode: code(137)}}}}}
	sets, _ = interpreter.Interpret(context.Background(), killed, nil)
	if len(sets) != 1 || sets[0].Tool != "phpunit" || sets[0].Summary != "the tool did not conclude (code 137): OOMKilled" {
		t.Errorf("killed: %+v", sets)
	}

	if sets, _ := interpreter.Interpret(context.Background(), capability.RunState{}, nil); len(sets) != 0 {
		t.Errorf("never started: %+v", sets)
	}
}

// What would misread a run is refused when the plugin is loaded.
func TestAnInconsistentReadingIsRefusedAtLoad(t *testing.T) {
	for label, broken := range map[string]string{
		"unknown view":   strings.Replace(reading, "phpstan.messages", "phpstan.lines", 1),
		"wrong parser":   strings.Replace(reading, "from: phpstan.messages", "from: junit.cases", 1),
		"unknown field":  strings.Replace(reading, "{file}", "{path}", 1),
		"unmapped value": strings.Replace(reading, "rerun: flaky, ", "", 1),
		"unread tool":    strings.Replace(reading, "tool: phpstan", "tool: psalm", 1),
		"report field":   strings.Replace(reading, `duration: "{duration_ms}"`+"\n    cases", `duration: "{total}"`+"\n    cases", 1),
		"not yaml":       "tools: [",
	} {
		if _, err := Compile([]byte(broken), spec); err == nil {
			t.Errorf("%s: accepted", label)
		}
	}
}
