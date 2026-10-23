package app

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	sdkinterpret "forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

/*
 * The readings the PHP and MSE plugins wrote in Go before declaring them, kept as the oracle their
 * declarations are checked against.
 */

// staticReader reads PHPStan and PHP-CS-Fixer.
//
// Violations, not tests, projected onto cases all the same: the question asked is the same, what
// appeared and what disappeared. A violation's identity is its file and its rule, never its line:
// a line moves at every commit.
type staticReader struct{}

func (staticReader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	var sets []facts.Set
	if set, found := tool(run, records, "phpstan", "run-phpstan", phpstanSet); found {
		sets = append(sets, set)
	}
	if set, found := tool(run, records, "cs-fixer", "run-cs-fixer", csFixerSet); found {
		// PHP-CS-Fixer exits 0 when everything conforms and 8 when files need fixing. Any other code
		// is a failure of the tool, whatever its report says, and says why the task ended.
		if code, ran := exitCode(run, "run-cs-fixer"); ran && code != 0 && code != 8 {
			summary, _ := sdkinterpret.NotConcluded(run, "run-cs-fixer")
			set = facts.Set{
				Shape: facts.ShapeCases, Group: "static", Tool: "cs-fixer", RawObject: set.RawObject,
				Status: facts.StatusNA, Summary: summary,
			}
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// unitReader reads PHPUnit's JUnit report.
type unitReader struct{}

func (unitReader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	if set, found := tool(run, records, "phpunit", "run-unit-tests", junitSet("unit")); found {
		return []facts.Set{set}, nil
	}
	return nil, nil
}

// tool reads one tool's record, or says it did not conclude when its task ran and left nothing
// readable.
func tool(
	run capability.RunState, records []parse.Record, name, task string,
	read func(parse.Record) facts.Set,
) (facts.Set, bool) {
	if record, readable := sdkinterpret.Readable(records, name); readable {
		set := read(record)
		set.Tool, set.RawObject = name, record.Object
		return set, true
	}
	if summary, ran := sdkinterpret.NotConcluded(run, task); ran {
		return facts.Set{
			Shape: facts.ShapeCases, Group: groupOf(name), Tool: name,
			Status: facts.StatusNA, Summary: summary,
		}, true
	}
	return facts.Set{}, false
}

// exitCode is the first non-zero exit code of a task's steps, or zero when they all succeeded.
func exitCode(run capability.RunState, task string) (int32, bool) {
	for _, state := range run.Tasks {
		if state.Name != task {
			continue
		}
		var found bool
		for _, step := range state.Steps {
			if step.ExitCode == nil {
				continue
			}
			found = true
			if *step.ExitCode != 0 {
				return *step.ExitCode, true
			}
		}
		return 0, found
	}
	return 0, false
}

func groupOf(tool string) string {
	if tool == "phpunit" {
		return "unit"
	}
	return "static"
}

func junitSet(group string) func(parse.Record) facts.Set {
	return func(record parse.Record) facts.Set {
		report := record.Payload.(parse.JUnit)
		cases := sdkinterpret.JUnitCases(report)
		return facts.Set{
			Shape: facts.ShapeCases, Group: group, Cases: cases,
			DurationMS: report.DurationMS, Summary: sdkinterpret.SummarizeTests(cases),
		}
	}
}

func phpstanSet(record parse.Record) facts.Set {
	report := record.Payload.(parse.PHPStan)
	var cases []facts.Case
	// Errors outside a file are failures of the tool, not of the code; they are kept apart under a
	// suite of their own.
	for _, message := range report.Errors {
		cases = append(cases, facts.Case{
			Suite: "phpstan", Name: "erreur d'analyse",
			Status: facts.CaseFailed, Message: parse.FirstLine(message),
		})
	}
	for _, file := range report.Files {
		for _, message := range file.Messages {
			rule := message.Identifier
			if rule == "" {
				// Older versions emit no identifier: the message stands in for the rule.
				rule = parse.FirstLine(message.Message)
			}
			cases = append(cases, facts.Case{
				Suite: file.Path, Name: rule, Status: facts.CaseFailed,
				Message: fmt.Sprintf("line %d: %s", message.Line, parse.FirstLine(message.Message)),
			})
		}
	}
	return violations(cases, "violation(s)")
}

func csFixerSet(record parse.Record) facts.Set {
	report := record.Payload.(parse.CSFixer)
	var cases []facts.Case
	for _, file := range report.Files {
		fixers := file.AppliedFixers
		if len(fixers) == 0 {
			fixers = []string{"style"}
		}
		for _, fixer := range fixers {
			cases = append(cases, facts.Case{
				Suite: file.Name, Name: fixer, Status: facts.CaseFailed,
				Message: "style deviation: " + fixer, Trace: file.Diff,
			})
		}
	}
	return violations(cases, "style deviation(s)")
}

// violations is a static analysis' set. A clean one has no cases and still passes: it ran and
// found nothing, which is not the same as not running.
func violations(cases []facts.Case, noun string) facts.Set {
	set := facts.Set{
		Shape: facts.ShapeCases, Group: "static", Cases: cases,
		Summary: sdkinterpret.SummarizeViolations(cases, noun),
	}
	if len(cases) == 0 {
		set.Status = facts.StatusPass
	}
	return set
}

// Every combination of report and run state the readings must tell apart: a report present,
// unreadable or absent, against a task that never started, succeeded, failed, crashed or was killed.
func TestTheDeclaredReadingsReadAsTheirOracles(t *testing.T) {
	stateOf := func(task string, exit int32, reason string) capability.RunState {
		return capability.RunState{Tasks: []capability.TaskState{{
			Name: task, Reason: reason, Steps: []capability.StepState{{Name: "exec", ExitCode: &exit}},
		}}}
	}
	type reading struct {
		ref    string
		oracle capability.Interpreter
		tasks  []string
		tools  map[string]parse.Record
	}
	readings := []reading{
		{"php/static-tests", staticReader{}, []string{"run-phpstan", "run-cs-fixer"}, map[string]parse.Record{
			"phpstan":       record(t, "phpstan", parse.PHPStanJSON, report(t, "phpstan.json")),
			"phpstan-clean": record(t, "phpstan", parse.PHPStanJSON, report(t, "phpstan-clean.json")),
			"cs-fixer":      record(t, "cs-fixer", parse.CSFixerJSON, report(t, "cs-fixer.json")),
			"broken":        record(t, "phpstan", parse.PHPStanJSON, []byte("{truncated")),
		}},
		{"php/unit-tests", unitReader{}, []string{"run-unit-tests"}, map[string]parse.Record{
			"junit":  record(t, "phpunit", parse.JUnitXML, report(t, "junit.xml")),
			"broken": record(t, "phpunit", parse.JUnitXML, []byte("<testsuites")),
		}},
	}

	compared := 0
	for _, entry := range readings {
		states := map[string]capability.RunState{"not started": {}}
		for _, task := range entry.tasks {
			for _, exit := range []int32{0, 1, 8, 16, 137, 255} {
				reason := ""
				if exit == 137 {
					reason = "OOMKilled"
				}
				states[fmt.Sprintf("%s exit %d", task, exit)] = stateOf(task, exit, reason)
			}
		}
		recordSets := map[string][]parse.Record{"no report": nil}
		for name, one := range entry.tools {
			recordSets[name] = []parse.Record{one}
		}
		if entry.ref == "php/static-tests" {
			recordSets["both"] = []parse.Record{entry.tools["phpstan"], entry.tools["cs-fixer"]}
		}

		for stateName, state := range states {
			for recordsName, records := range recordSets {
				expected, _ := entry.oracle.Interpret(context.Background(), state, records)
				got := interpret(t, entry.ref, state, records...)
				if !sameSets(expected, got) {
					t.Errorf("%s, %s, %s:\n oracle   %+v\n declared %+v", entry.ref, stateName, recordsName, expected, got)
				}
				compared++
			}
		}
	}
	if compared < 50 {
		t.Fatalf("only %d combinations compared", compared)
	}
}

// sameSets compares sets, an empty list of cases equal to none: the stored facts are the same.
func sameSets(a, b []facts.Set) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left, right := a[i], b[i]
		if len(left.Cases) == 0 && len(right.Cases) == 0 {
			left.Cases, right.Cases = nil, nil
		}
		if !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}
