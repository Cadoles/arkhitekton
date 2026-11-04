package golang

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// The manifests are extracted from what actually runs. If one disappears from the embedding (a
// file renamed, a directory moved), nothing else would report it before a pipeline could not be
// found in the cluster.
func TestThePluginShipsItsPipelinesAndTasks(t *testing.T) {
	kinds := map[string]string{}
	for _, manifest := range Manifests() {
		kinds[manifest.Name] = manifest.Kind
	}
	for name, kind := range map[string]string{
		"go-build": "Pipeline", "go-unit-tests": "Pipeline", "go-static-tests": "Pipeline", "go-vulncheck": "Pipeline",
		"go-build-binaries": "Task", "go-test": "Task", "golangci-lint": "Task", "govulncheck": "Task", "go-modules": "Task",
	} {
		if kinds[name] != kind {
			t.Errorf("%s: %q, expected a %s", name, kinds[name], kind)
		}
	}
}

// A build keeps its archives and their checksums, sorted by name, and counts only the archives.
func TestABuildKeepsItsArchivesAndChecksums(t *testing.T) {
	attached := func(member string) parse.Record {
		return parse.Record{Member: member, Payload: parse.Attached{Object: "attachments/1/" + member, Size: 10}}
	}
	sets, err := binaries{}.Interpret(context.Background(), capability.RunState{}, []parse.Record{
		attached("binaries/SHA256SUMS"),
		attached("binaries/app_1.0.0_linux_arm64.tar.gz"),
		attached("binaries/app_1.0.0_linux_amd64.tar.gz"),
	})
	if err != nil || len(sets) != 1 {
		t.Fatalf("%v, %v", sets, err)
	}
	set := sets[0]
	if set.Shape != facts.ShapeAttachments || set.Group != "build" || set.Summary != "2 binaire(s)" {
		t.Errorf("set %+v", set)
	}
	var names []string
	for _, attachment := range set.Attachments {
		names = append(names, attachment.Name)
	}
	expected := []string{"SHA256SUMS", "app_1.0.0_linux_amd64.tar.gz", "app_1.0.0_linux_arm64.tar.gz"}
	if len(names) != len(expected) {
		t.Fatalf("attachments %v", names)
	}
	for index := range expected {
		if names[index] != expected[index] {
			t.Errorf("attachment %d: %q, expected %q", index, names[index], expected[index])
		}
	}

	if _, err := (binaries{}).Interpret(context.Background(), capability.RunState{},
		[]parse.Record{attached("binaries/SHA256SUMS")}); err == nil {
		t.Error("a build with no archive read as a success")
	}
}

// govulncheck's stream, as its JSON output documents it: a vulnerability the code calls, one in a
// package it only imports, one in a module it only requires. The same vulnerability is reported at
// several levels: its deepest counts.
const vulncheckStream = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scan_level":"symbol"}}
{"progress":{"message":"Scanning your code and 42 packages across 7 dependent modules for known vulnerabilities..."}}
{"osv":{"id":"GO-2025-0001","summary":"Denial of service in HTTP/2","details":"A crafted frame exhausts memory.","aliases":["CVE-2025-1111"],"database_specific":{"url":"https://pkg.go.dev/vuln/GO-2025-0001"}}}
{"osv":{"id":"GO-2025-0002","summary":"Path traversal in archive reading","aliases":["CVE-2025-2222"]}}
{"osv":{"id":"GO-2025-0003","summary":"Weak default in a helper"}}
{"finding":{"osv":"GO-2025-0001","fixed_version":"v0.38.0","trace":[{"module":"golang.org/x/net","version":"v0.30.0"}]}}
{"finding":{"osv":"GO-2025-0001","fixed_version":"v0.38.0","trace":[{"module":"golang.org/x/net","version":"v0.30.0","package":"golang.org/x/net/http2"}]}}
{"finding":{"osv":"GO-2025-0001","fixed_version":"v0.38.0","trace":[{"module":"golang.org/x/net","version":"v0.30.0","package":"golang.org/x/net/http2","function":"ServeConn","receiver":"*Server"},{"module":"example.test/app","package":"example.test/app/server","function":"Run","position":{"filename":"server/run.go","line":42}}]}}
{"finding":{"osv":"GO-2025-0002","trace":[{"module":"example.test/archive","version":"v1.2.0","package":"example.test/archive/tar"}]}}
{"finding":{"osv":"GO-2025-0003","fixed_version":"v2.0.1","trace":[{"module":"example.test/helpers","version":"v2.0.0"}]}}
`

func TestGovulncheckFindingsAreRankedByWhatTheCodeReaches(t *testing.T) {
	sets, err := vulnerabilities{}.Interpret(context.Background(), capability.RunState{}, []parse.Record{{
		Member: "govulncheck/report.json", Object: "raw/vulncheck.json", Payload: parse.Bytes(vulncheckStream),
	}})
	if err != nil || len(sets) != 1 {
		t.Fatalf("%v, %v", sets, err)
	}
	set := sets[0]
	if set.Shape != facts.ShapeFindings || set.Group != "security" || set.Status != facts.StatusFail {
		t.Errorf("set %+v", set)
	}
	if set.Summary != "1 vulnérabilité(s) appelée(s), 3 au total" {
		t.Errorf("summary %q", set.Summary)
	}

	byID := map[string]facts.Finding{}
	for _, finding := range set.Findings.Items {
		byID[finding.Fingerprint] = finding
	}
	called := byID["GO-2025-0001:golang.org/x/net"]
	if called.Severity != facts.SeverityHigh || called.Location != "server/run.go:42" ||
		called.RuleID != "GO-2025-0001 (CVE-2025-1111)" || called.PackageFixed != "v0.38.0" || !called.Fixable {
		t.Errorf("called %+v", called)
	}
	imported := byID["GO-2025-0002:example.test/archive"]
	if imported.Severity != facts.SeverityLow || imported.Location != "example.test/archive/tar" || imported.Fixable {
		t.Errorf("imported %+v", imported)
	}
	if required := byID["GO-2025-0003:example.test/helpers"]; required.Severity != facts.SeverityInfo {
		t.Errorf("required %+v", required)
	}
}

// A report with nothing the code calls passes, whatever its modules require.
func TestGovulncheckPassesWhenNothingIsCalled(t *testing.T) {
	stream := `{"config":{"protocol_version":"v1.0.0"}}
{"finding":{"osv":"GO-2025-0003","trace":[{"module":"example.test/helpers","version":"v2.0.0"}]}}
`
	sets, err := vulnerabilities{}.Interpret(context.Background(), capability.RunState{},
		[]parse.Record{{Member: "govulncheck/report.json", Payload: parse.Bytes(stream)}})
	if err != nil || len(sets) != 1 || sets[0].Status != facts.StatusPass {
		t.Fatalf("%+v, %v", sets, err)
	}
	if _, err := readVulnerabilities(nil); err == nil {
		t.Error("an empty report read as clean")
	}
}

// A tool killed on its way leaves a partial report: an audit cut short is no clean audit, and a
// build that failed on one platform offers none of its archives.
func TestAToolThatStoppedConcludesNothing(t *testing.T) {
	killed := func(task string) capability.RunState {
		code := int32(137)
		return capability.RunState{Tasks: []capability.TaskState{{
			Name: task, Status: "failed", Reason: "OOMKilled",
			Steps: []capability.StepState{{Name: "exec", ExitCode: &code}},
		}}}
	}

	sets, err := vulnerabilities{}.Interpret(context.Background(), killed(vulncheckTask),
		[]parse.Record{{Member: "govulncheck/report.json", Payload: parse.Bytes(vulncheckStream)}})
	if err != nil || len(sets) != 1 || sets[0].Status != facts.StatusNA || sets[0].Findings != nil ||
		sets[0].Summary != "the tool did not conclude (code 137): OOMKilled" {
		t.Errorf("audit %+v, %v", sets, err)
	}

	sets, err = binaries{}.Interpret(context.Background(), killed(buildTask), []parse.Record{{
		Member: "binaries/app_1.0.0_linux_amd64.tar.gz", Payload: parse.Attached{Object: "attachments/1/a"},
	}})
	if err != nil || len(sets) != 1 || sets[0].Status != facts.StatusNA || len(sets[0].Attachments) != 0 {
		t.Errorf("build %+v, %v", sets, err)
	}
}
