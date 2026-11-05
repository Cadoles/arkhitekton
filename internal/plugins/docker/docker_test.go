package docker

import (
	"context"
	_ "embed"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// A real report of `trivy image --format json alpine:3.18.0`, reduced to a few vulnerabilities.
//
//go:embed testdata/trivy-report.json
var report []byte

// The manifests are extracted from what actually runs. If one disappears from the embedding (a
// file renamed, a directory moved), nothing else would report it before a pipeline could not be
// found in the cluster.
func TestThePluginShipsItsPipelinesAndTasks(t *testing.T) {
	kinds := map[string]string{}
	for _, manifest := range Manifests() {
		kinds[manifest.Name] = manifest.Kind
	}
	for name, kind := range map[string]string{
		"build-image": "Pipeline", "retag-image": "Pipeline", "docker-scan": "Pipeline",
		"generate-image-tag": "Task", "buildx-build-image": "Task", "push-image": "Task", "tag-image": "Task",
		"trivy-image": "Task",
	} {
		if kinds[name] != kind {
			t.Errorf("%s: %q, expected a %s", name, kinds[name], kind)
		}
	}
}

func TestAScanReadsTrivysFindings(t *testing.T) {
	sets, err := scanned{}.Interpret(context.Background(), capability.RunState{}, []parse.Record{{
		Member: "trivy/report.json", Object: "raw/scan.tar.gz", Payload: parse.Bytes(report),
	}})
	if err != nil || len(sets) != 1 {
		t.Fatalf("%v, %v", sets, err)
	}
	set := sets[0]
	if set.Shape != facts.ShapeFindings || set.Group != "security" || set.Status != facts.StatusFail {
		t.Errorf("set %+v", set)
	}
	if set.Findings.BySeverity[facts.SeverityCritical] == 0 || set.Findings.BySeverity[facts.SeverityHigh] == 0 {
		t.Errorf("severities %v", set.Findings.BySeverity)
	}
	busybox := set.Findings.Items[0]
	if busybox.TargetName != "alpine:3.18.0" || busybox.Location == "" || busybox.PackageName == "" ||
		busybox.Severity != facts.SeverityCritical || busybox.CVSS == 0 || !busybox.Fixable {
		t.Errorf("finding %+v", busybox)
	}
}

// The same vulnerability of a package in two targets is one thing to upgrade, and a severity
// Trivy could not establish is still shown.
func TestAScanReportsAVulnerabilityOnce(t *testing.T) {
	findings, err := readFindings([]byte(`{"ArtifactName":"app:1","Results":[
		{"Target":"a","Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"p","InstalledVersion":"1","Severity":"UNKNOWN"}]},
		{"Target":"b","Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"p","InstalledVersion":"1","Severity":"UNKNOWN"}]}]}`))
	if err != nil || findings.Total != 1 || findings.BySeverity[facts.SeverityInfo] != 1 || findings.Items[0].Title != "CVE-1" {
		t.Fatalf("%+v, %v", findings, err)
	}
}

// An image with no vulnerability passes: Trivy then reports no `Results` at all.
func TestACleanImagePasses(t *testing.T) {
	sets, err := scanned{}.Interpret(context.Background(), capability.RunState{},
		[]parse.Record{{Member: "trivy/report.json", Payload: parse.Bytes(`{"ArtifactName":"app:1"}`)}})
	if err != nil || len(sets) != 1 || sets[0].Status != facts.StatusPass || sets[0].Findings.Total != 0 {
		t.Fatalf("%+v, %v", sets, err)
	}
}

// A scan killed on its way concludes nothing, whatever it left: a cut report is no clean image.
func TestAScanThatStoppedConcludesNothing(t *testing.T) {
	code := int32(137)
	sets, err := scanned{}.Interpret(context.Background(), capability.RunState{Tasks: []capability.TaskState{{
		Name: scanTask, Status: "failed", Reason: "OOMKilled",
		Steps: []capability.StepState{{Name: "exec", ExitCode: &code}},
	}}}, []parse.Record{{Member: "trivy/report.json", Payload: parse.Bytes(`{"ArtifactName":"app:1"}`)}})
	if err != nil || len(sets) != 1 || sets[0].Status != facts.StatusNA || sets[0].Findings != nil ||
		sets[0].Summary != "the tool did not conclude (code 137): OOMKilled" {
		t.Fatalf("%+v, %v", sets, err)
	}
}
