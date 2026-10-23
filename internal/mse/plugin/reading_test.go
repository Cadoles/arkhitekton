package plugin

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/host/hosttest"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	sdkinterpret "forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

/*
 * The plugin's readings, on reference reports, as the dashboard runs them.
 */

// update rewrites the golden files from what the code produces now: `go test ./... -update`.
var update = flag.Bool("update", false, "rewrite the golden files")

func report(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "reports", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// kept is a member the host stored as an attachment.
func kept(member, mediaType string, size int64) parse.Record {
	object := "attachments/run/" + strings.ReplaceAll(member, " ", "-")
	return parse.Record{Artifact: "media", Member: member, Object: "raw/run/media.tar.gz", Parser: parse.Attach,
		Tool: "cypress", Payload: parse.Attached{Object: object, MediaType: mediaType, Size: size}}
}

func read(t *testing.T, pipeline string, run capability.RunState, records ...parse.Record) []facts.Set {
	t.Helper()
	return hosttest.Interpret(t, Plugin(), pipeline, run, records...)
}

// golden compares a value with its golden file, as indented JSON.
func golden(t *testing.T, name string, value any) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from its golden file (run with -update and review the diff):\n%s", name, got)
	}
}

// What each reading produces from the reference reports, recorded.
func TestTheReadingsReadAsRecorded(t *testing.T) {
	cases := map[string][]facts.Set{
		"integration-tests.json": read(t, "integration-tests", capability.RunState{},
			hosttest.Record("phpunit-integration", parse.JUnitXML, report(t, "junit.xml"))),
		"e2e-tests.json": read(t, "e2e-tests", capability.RunState{},
			hosttest.Record("cypress", parse.MochawesomeJSON, report(t, "mochawesome.json"))),
		"e2e-tests-attachments.json": read(t, "e2e-tests", capability.RunState{},
			hosttest.Record("cypress", parse.MochawesomeJSON, report(t, "mochawesome.json")),
			kept("videos/login.cy.js.mp4", "video/mp4", 2048),
			kept("screenshots/login.cy.js/Login -- fails (failed).png", "image/png", 512),
			kept("screenshots/login.cy.js/Unknown -- test (failed).png", "image/png", 256)),
		"e2e-tests-no-report.json": read(t, "e2e-tests", capability.RunState{},
			kept("videos/login.cy.js.mp4", "video/mp4", 2048)),
	}
	for name, sets := range cases {
		t.Run(name, func(t *testing.T) { golden(t, filepath.Join("facts", name), sets) })
	}
}

// The cases recorded for the declared reading read as recorded: they are what
// `arkhitekton check-reading` replays.
func TestTheRecordedCasesReadAsExpected(t *testing.T) {
	cases, _ := filepath.Glob(filepath.Join("testdata", "readings", "integration-tests", "*"))
	if len(cases) == 0 {
		t.Fatal("no recorded case")
	}
	reading, err := os.ReadFile(filepath.Join("readings", "integration-tests.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range cases {
		got, expected, err := hosttest.CheckReading(reading, dir)
		if err != nil {
			t.Errorf("%s: %v", dir, err)
			continue
		}
		if expected == nil || !hosttest.SameFacts(got, expected) {
			t.Errorf("%s reads differently:\n%s", dir, got)
		}
	}
}

// oracle is the integration tests' reading as it was written in Go, before it was declared.
type oracle struct{}

func (oracle) Interpret(_ context.Context, run capability.RunState, records []parse.Record) ([]facts.Set, error) {
	const tool = "phpunit-integration"
	if record, readable := sdkinterpret.Readable(records, tool); readable {
		report := record.Payload.(parse.JUnit)
		cases := sdkinterpret.JUnitCases(report)
		return []facts.Set{{
			Shape: facts.ShapeCases, Group: "integration", Tool: tool, RawObject: record.Object,
			Cases: cases, DurationMS: report.DurationMS, Summary: sdkinterpret.SummarizeTests(cases),
		}}, nil
	}
	// The tests ran inside a deployed MSE: when they leave no report, saying they did not conclude
	// is what tells a failed deployment from a clean run.
	if summary, ran := sdkinterpret.NotConcluded(run, "run-integration-tests"); ran {
		return []facts.Set{{
			Shape: facts.ShapeCases, Group: "integration", Tool: tool,
			Status: facts.StatusNA, Summary: summary,
		}}, nil
	}
	return nil, nil
}

// The declared reading reads every combination of report and run state as its oracle did.
func TestTheDeclaredReadingReadsAsItsOracle(t *testing.T) {
	states := map[string]capability.RunState{"not started": {}}
	for _, exit := range []int32{0, 1, 8, 16, 137, 255} {
		reason := ""
		if exit == 137 {
			reason = "OOMKilled"
		}
		code := exit
		states[fmt.Sprintf("exit %d", exit)] = capability.RunState{Tasks: []capability.TaskState{{
			Name: "run-integration-tests", Reason: reason, Steps: []capability.StepState{{Name: "exec", ExitCode: &code}},
		}}}
	}
	recordSets := map[string][]parse.Record{
		"no report": nil,
		"junit":     {hosttest.Record("phpunit-integration", parse.JUnitXML, report(t, "junit.xml"))},
		"broken":    {hosttest.Record("phpunit-integration", parse.JUnitXML, []byte("<testsuites"))},
	}
	for stateName, state := range states {
		for recordsName, records := range recordSets {
			expected, _ := oracle{}.Interpret(context.Background(), state, records)
			got := read(t, "integration-tests", state, records...)
			if !sameSets(expected, got) {
				t.Errorf("%s, %s:\n oracle   %+v\n declared %+v", stateName, recordsName, expected, got)
			}
		}
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
