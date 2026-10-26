// Command gotest is an example native plugin: its program reads the event stream of
// `go test -json`, a format the dashboard does not parse, from an artifact declared with the `raw`
// parser. The dashboard launches it and asks it to read each run.
//
//	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/plugin-linux-amd64 .
//
// A test is a case: its package is the suite, its name the case's, its last action its status.
// A package that fails with no failed test (it did not build, its TestMain failed) is a case of
// its own, so that the failure is not lost. A test the stream leaves without an outcome was cut
// off, and is counted as not run.
//
// Like everything under rpc/examples, it is under the Apache License 2.0, to be copied as the
// start of a plugin.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// The tool as the capability's artifact names it, and the task that runs it.
const (
	tool = "gotest"
	task = "run-go-test"
)

// event is one line of `go test -json`.
type event struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
	// ImportPath names the package a build event is about, FailedBuild the build a package's
	// failure comes from.
	ImportPath  string
	FailedBuild string
}

type reader struct{}

func (reader) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	record, readable := rawRecord(records)
	if !readable {
		if summary, ran := interpret.NotConcluded(run, task); ran {
			return []facts.Set{{Shape: facts.ShapeCases, Group: "unit", Tool: tool, Status: facts.StatusNA, Summary: summary}}, nil
		}
		return nil, nil
	}

	cases, duration := read(record.Payload.(parse.Bytes))
	return []facts.Set{{
		Shape: facts.ShapeCases, Group: "unit", Tool: tool, RawObject: record.Object,
		Cases: cases, DurationMS: duration, Summary: interpret.SummarizeTests(cases),
	}}, nil
}

func rawRecord(records []parse.Record) (parse.Record, bool) {
	for _, record := range records {
		if record.Tool == tool && record.Parser == parse.Raw && record.Err == nil {
			return record, true
		}
	}
	return parse.Record{}, false
}

// test is a test being followed through the stream.
type test struct {
	pkg, name string
	action    string
	elapsed   float64
	output    strings.Builder
}

func read(stream []byte) ([]facts.Case, int64) {
	var order []*test
	tests := map[[2]string]*test{}
	packages := map[string]*test{}
	var packageOrder []string
	builds := map[string]*strings.Builder{}
	var duration float64

	scanner := bufio.NewScanner(bytes.NewReader(stream))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		var line event
		// A line that is not an event (a build error printed as text) is skipped: the package's own
		// outcome still says it failed.
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.Action == "build-output" {
			if builds[line.ImportPath] == nil {
				builds[line.ImportPath] = &strings.Builder{}
			}
			builds[line.ImportPath].WriteString(line.Output)
			continue
		}
		if line.Package == "" {
			continue
		}
		if line.Test == "" {
			entry, seen := packages[line.Package]
			if !seen {
				entry = &test{pkg: line.Package}
				packages[line.Package] = entry
				packageOrder = append(packageOrder, line.Package)
			}
			follow(entry, line)
			if line.FailedBuild != "" && builds[line.FailedBuild] != nil {
				// The compiler's own words say more than "[build failed]".
				entry.output.Reset()
				entry.output.WriteString(builds[line.FailedBuild].String())
			}
			if line.Action == "pass" || line.Action == "fail" || line.Action == "skip" {
				duration += line.Elapsed
			}
			continue
		}
		key := [2]string{line.Package, line.Test}
		entry, seen := tests[key]
		if !seen {
			entry = &test{pkg: line.Package, name: line.Test}
			tests[key] = entry
			order = append(order, entry)
		}
		follow(entry, line)
	}

	cases := make([]facts.Case, 0, len(order))
	failedIn := map[string]bool{}
	for _, entry := range order {
		built := facts.Case{Suite: entry.pkg, Name: entry.name, DurationMS: int(entry.elapsed * 1000)}
		switch entry.action {
		case "pass":
			built.Status = facts.CasePassed
		case "skip":
			built.Status = facts.CaseSkipped
			built.Message = firstMessage(entry.output.String())
		case "fail":
			built.Status = facts.CaseFailed
			built.Message = firstMessage(entry.output.String())
			built.Trace = strings.TrimSpace(entry.output.String())
			failedIn[entry.pkg] = true
		default:
			built.Status = facts.CaseSkipped
			built.Message = "no outcome: the stream ended before it"
		}
		cases = append(cases, built)
	}
	for _, name := range packageOrder {
		entry := packages[name]
		if entry.action == "fail" && !failedIn[name] {
			cases = append(cases, facts.Case{
				Suite: name, Name: "(package)", Status: facts.CaseFailed,
				Message: firstMessage(entry.output.String()), Trace: strings.TrimSpace(entry.output.String()),
			})
		}
	}
	return cases, int64(duration * 1000)
}

func follow(entry *test, line event) {
	switch line.Action {
	case "output":
		entry.output.WriteString(line.Output)
	case "pass", "fail", "skip":
		entry.action, entry.elapsed = line.Action, line.Elapsed
	}
}

// firstMessage is the first line of a test's output that is not one of go test's own markers.
func firstMessage(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "=== ") || strings.HasPrefix(trimmed, "--- ") ||
			strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "FAIL") || trimmed == "PASS" {
			continue
		}
		return trimmed
	}
	return ""
}

func main() {
	extension.Serve(func(context.Context, extension.Runtime) (extension.Hooks, error) {
		return extension.Hooks{Interpreters: map[string]capability.Interpreter{"unit-tests": reader{}}}, nil
	})
}
