package parsers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// goTestOutputLimit bounds what a case keeps of its output: the end, where a failure is told.
const goTestOutputLimit = 16 << 10

// goTestEvent is a line of `go test -json`. The build events (Go 1.24+) name the package they
// build by ImportPath, and a package whose build failed says which in FailedBuild.
type goTestEvent struct {
	Action      string
	Package     string
	Test        string
	Elapsed     float64
	Output      string
	OutputType  string
	ImportPath  string
	FailedBuild string
}

type goTestRun struct {
	pkg, name string
	outcome   parse.GoTestOutcome
	elapsed   float64
	output    strings.Builder
}

// ParseGoTest reads a `go test -json` stream.
//
// Each test concluded is a case. A parent whose failure only repeats its subtests' is not counted
// twice. A package that failed with no test failing (it does not build, TestMain failed, a panic
// escaped) is a case of its own, named after nothing but its package. A test still running when
// its package ended is a failure: the package died under it.
func ParseGoTest(raw []byte) (parse.GoTest, error) {
	var report parse.GoTest
	builds := map[string]*strings.Builder{}
	tests := map[string]*goTestRun{}
	var order []string
	packageOutput := map[string]*strings.Builder{}
	read := 0

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event goTestEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		read++

		if event.ImportPath != "" && event.Action == "build-output" {
			if builds[event.ImportPath] == nil {
				builds[event.ImportPath] = &strings.Builder{}
			}
			builds[event.ImportPath].WriteString(event.Output)
			continue
		}
		if event.Package == "" {
			continue
		}

		if event.Test == "" {
			switch event.Action {
			case "output":
				if !frame(event) {
					if packageOutput[event.Package] == nil {
						packageOutput[event.Package] = &strings.Builder{}
					}
					packageOutput[event.Package].WriteString(event.Output)
				}
			case "pass", "fail":
				report.DurationMS += int64(event.Elapsed * 1000)
				if event.Action == "fail" {
					concludePackage(&report, event, tests, order, builds, packageOutput)
				}
			}
			continue
		}

		key := event.Package + "\x00" + event.Test
		run := tests[key]
		if run == nil {
			run = &goTestRun{pkg: event.Package, name: event.Test}
			tests[key] = run
			order = append(order, key)
		}
		switch event.Action {
		case "output":
			if !frame(event) {
				run.output.WriteString(event.Output)
			}
		case "pass":
			run.outcome, run.elapsed = parse.GoTestPassed, event.Elapsed
		case "fail":
			run.outcome, run.elapsed = parse.GoTestFailed, event.Elapsed
		case "skip":
			run.outcome, run.elapsed = parse.GoTestSkipped, event.Elapsed
		}
	}
	if err := scanner.Err(); err != nil {
		return report, fmt.Errorf("go test report: %w", err)
	}
	if read == 0 && len(bytes.TrimSpace(raw)) > 0 {
		return report, fmt.Errorf("go test report: no JSON event")
	}

	for _, key := range order {
		run := tests[key]
		if run.outcome == "" {
			continue
		}
		if run.outcome == parse.GoTestFailed && strings.TrimSpace(run.output.String()) == "" &&
			hasSubtest(tests, run) {
			continue
		}
		report.Cases = append(report.Cases, parse.GoTestCase{
			Package: run.pkg, Name: run.name, Outcome: run.outcome,
			DurationMS: int(time.Duration(run.elapsed * float64(time.Second)).Milliseconds()),
			Output:     tail(run.output.String()),
		})
	}
	return report, nil
}

// concludePackage records a failed package's own failure, and fails the tests it left running.
func concludePackage(report *parse.GoTest, event goTestEvent, tests map[string]*goTestRun, order []string,
	builds map[string]*strings.Builder, packageOutput map[string]*strings.Builder) {
	testFailed := false
	for _, key := range order {
		run := tests[key]
		if run.pkg != event.Package {
			continue
		}
		if run.outcome == "" {
			run.outcome = parse.GoTestFailed
		}
		if run.outcome == parse.GoTestFailed {
			testFailed = true
		}
	}
	if testFailed {
		return
	}

	output := ""
	switch {
	case event.FailedBuild != "" && builds[event.FailedBuild] != nil:
		output = builds[event.FailedBuild].String()
	case packageOutput[event.Package] != nil:
		output = packageOutput[event.Package].String()
	}
	report.Cases = append(report.Cases, parse.GoTestCase{
		Package: event.Package, Outcome: parse.GoTestFailed,
		DurationMS: int(event.Elapsed * 1000), Output: tail(output),
	})
}

// frame says whether an output line is the runner's bookkeeping (`=== RUN`, `--- PASS`) rather than
// what the test printed. Go 1.25 marks them; older versions are recognised by their prefix.
func frame(event goTestEvent) bool {
	if event.OutputType == "frame" {
		return true
	}
	for _, prefix := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME", "--- PASS", "--- FAIL", "--- SKIP"} {
		if strings.HasPrefix(strings.TrimSpace(event.Output), prefix) {
			return true
		}
	}
	return false
}

func hasSubtest(tests map[string]*goTestRun, parent *goTestRun) bool {
	for _, run := range tests {
		if run.pkg == parent.pkg && strings.HasPrefix(run.name, parent.name+"/") {
			return true
		}
	}
	return false
}

func tail(output string) string {
	if len(output) <= goTestOutputLimit {
		return output
	}
	return "…" + output[len(output)-goTestOutputLimit:]
}
