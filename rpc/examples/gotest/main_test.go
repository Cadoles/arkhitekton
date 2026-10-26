package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// read runs the interpreter natively on a recorded stream: the same code the module runs.
func readStream(t *testing.T, name string) facts.Set {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	sets, err := reader{}.Interpret(context.Background(), capability.RunState{},
		[]parse.Record{{Tool: tool, Parser: parse.Raw, Object: "raw/run/go-test.json", Payload: parse.Bytes(raw)}})
	if err != nil || len(sets) != 1 {
		t.Fatalf("sets %+v, err %v", sets, err)
	}
	if err := sets[0].Validate(); err != nil {
		t.Fatal(err)
	}
	return sets[0]
}

func TestEachTestBecomesACase(t *testing.T) {
	set := readStream(t, "calc.json")
	if len(set.Cases) != 3 || set.Verdict() != facts.StatusFail || set.Summary != "3 test(s), 1 failed, 1 skipped" {
		t.Fatalf("set = %+v", set)
	}
	divides := set.Cases[1]
	if divides.Suite != "example.org/sample/calc" || divides.Name != "TestDivides" ||
		divides.Status != facts.CaseFailed || divides.Message != "calc_test.go:12: expected 2, got 3" {
		t.Errorf("TestDivides = %+v", divides)
	}
	if slow := set.Cases[2]; slow.Status != facts.CaseSkipped || slow.Message != "calc_test.go:16: needs a database" {
		t.Errorf("TestSlow = %+v", slow)
	}
}

// A package that does not build has no test to fail: it is a case of its own, saying why.
func TestAPackageThatDoesNotBuildIsACase(t *testing.T) {
	set := readStream(t, "all.json")
	var broken *facts.Case
	for i := range set.Cases {
		if set.Cases[i].Suite == "example.org/sample/broken" {
			broken = &set.Cases[i]
		}
	}
	if broken == nil || broken.Name != "(package)" || broken.Status != facts.CaseFailed ||
		broken.Message != "broken/broken_test.go:5:34: undefined: undefined" {
		t.Errorf("broken = %+v among %+v", broken, set.Cases)
	}
}

// A stream cut off leaves tests with no outcome: not run, rather than lost or passed.
func TestATruncatedStreamLeavesTestsNotRun(t *testing.T) {
	set := readStream(t, "truncated.json")
	if len(set.Cases) == 0 {
		t.Fatal("no case")
	}
	last := set.Cases[len(set.Cases)-1]
	if last.Status != facts.CaseSkipped || last.Message != "no outcome: the stream ended before it" {
		t.Errorf("last = %+v", last)
	}
}

// A task that ran and left no stream did not conclude; one that never started says nothing.
func TestNoStreamIsReadFromTheRunsState(t *testing.T) {
	killed := int32(137)
	sets, _ := reader{}.Interpret(context.Background(), capability.RunState{Tasks: []capability.TaskState{{
		Name: task, Reason: "OOMKilled", Steps: []capability.StepState{{Name: "exec", ExitCode: &killed}},
	}}}, nil)
	if len(sets) != 1 || sets[0].Verdict() != facts.StatusNA {
		t.Errorf("killed: %+v", sets)
	}
	if sets, _ := (reader{}).Interpret(context.Background(), capability.RunState{}, nil); len(sets) != 0 {
		t.Errorf("never started: %+v", sets)
	}
}
