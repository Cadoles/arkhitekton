package review

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Cadoles/arkhitekton/sdk/capability"
	"github.com/Cadoles/arkhitekton/sdk/facts"
	"github.com/Cadoles/arkhitekton/sdk/parse"
)

func TestThePluginShipsItsPipelineAndTask(t *testing.T) {
	kinds := map[string]string{}
	for _, manifest := range Manifests() {
		kinds[manifest.Name] = manifest.Kind
	}
	if kinds["review-pull-request"] != "Pipeline" || kinds["conclave-review"] != "Task" {
		t.Errorf("manifests = %v", kinds)
	}
}

// The review asks for what it reads and nothing more: no service account, no registry.
func TestTheReviewAsksToReadTheSourceAModelAndTheForge(t *testing.T) {
	spec := Plugin().Pipelines[0].Spec
	needs := map[capability.NeedName]bool{}
	for _, need := range spec.Needs {
		needs[need.Name] = true
	}
	if len(needs) != 3 || !needs[capability.NeedSourceRead] || !needs[capability.NeedModelAccess] || !needs[capability.NeedForgeRead] {
		t.Errorf("needs = %v", needs)
	}
	// Chained after every build, it would review a release's commit, which is no pull request.
	if spec.Verification {
		t.Error("a review is no verification of a build")
	}
}

// Every setting a parameter reads is declared, for the instance and for a project: the scheduler
// gives a pipeline only the keys its plugin declares, and a key nobody declares would fail the run.
func TestTheParametersReadTheDeclaredSettings(t *testing.T) {
	plugin := Plugin()
	declared := func(fields []capability.Field) map[string]bool {
		names := map[string]bool{}
		for _, field := range fields {
			if field.Secret != (field.Name == "apiKey") {
				t.Errorf("%s: secret %v, only the model's key is", field.Name, field.Secret)
			}
			names[ID+"."+field.Name] = true
		}
		return names
	}
	global, project := declared(plugin.Global), declared(plugin.Project)
	for name, param := range plugin.Pipelines[0].Spec.Params {
		if param.Source != capability.ParamBinding || !strings.HasPrefix(param.DefaultKey, ID+".") {
			continue
		}
		if param.DefaultKey == ID+".apiKey" {
			t.Errorf("%s reads the key: a parameter lands in the PipelineRun's spec", name)
		}
		if !global[param.DefaultKey] || !project[param.DefaultKey] {
			t.Errorf("%s reads %s, which the plugin does not declare everywhere", name, param.DefaultKey)
		}
	}
	if !plugin.PerProject {
		t.Error("a project must be able to switch the review off")
	}
}

func reviewRecords(t *testing.T) []parse.Record {
	t.Helper()
	raw, err := os.ReadFile("testdata/review.json")
	if err != nil {
		t.Fatal(err)
	}
	return []parse.Record{
		{Member: "review/review.json", Object: "raw/7/review.tar.gz", Payload: parse.Bytes(raw)},
		{Member: "review/review.md", Payload: parse.Attached{Object: "attachments/7/review/review.md", MediaType: "text/markdown", Size: 2048}},
	}
}

// A request for changes fails the review; each finding keeps where it is and who reported it, and
// the review as written is kept beside them.
func TestAReviewBecomesFindings(t *testing.T) {
	sets, err := reviews{}.Interpret(context.Background(), capability.RunState{}, reviewRecords(t))
	if err != nil || len(sets) != 1 {
		t.Fatalf("%v, %v", sets, err)
	}
	set := sets[0]
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
	if set.Group != Group || set.Status != facts.StatusFail || set.RawObject != "raw/7/review.tar.gz" ||
		!strings.Contains(set.Summary, "pagination") {
		t.Errorf("set = %+v", set)
	}
	if set.Attributes["verdict"] != "request_changes" || set.Attributes["reviewers"] != "2/3" ||
		set.Attributes["failed"] != "pi-llama" || set.Attributes["questions"] != "1" ||
		!strings.Contains(set.Attributes["models"], "pi-qwen: qwen3-coder") {
		t.Errorf("attributes = %v", set.Attributes)
	}
	if len(set.Attachments) != 1 || set.Attachments[0].Name != "review.md" {
		t.Errorf("attachments = %+v", set.Attachments)
	}

	findings := set.Findings
	if findings.Total != 3 || findings.Suppressed != 1 ||
		findings.BySeverity[facts.SeverityHigh] != 1 || findings.BySeverity[facts.SeverityLow] != 0 {
		t.Errorf("counts = %d, %d suppressed, %v", findings.Total, findings.Suppressed, findings.BySeverity)
	}
	leak := findings.Items[0]
	if leak.Location != "internal/http/client.go:42-44" || leak.Tool != "pi-qwen, pi-mistral" ||
		leak.RuleID != "security" || leak.Severity != facts.SeverityHigh ||
		!strings.Contains(leak.Description, "**Suggestion** : Retirer") {
		t.Errorf("finding = %+v", leak)
	}
	if findings.Items[1].Location != "internal/store/list.go:88" {
		t.Errorf("one line = %q", findings.Items[1].Location)
	}
	if outside := findings.Items[2]; !outside.Suppressed || outside.SuppressionReason == "" {
		t.Errorf("out of scope = %+v", outside)
	}
}

// The same remark on the same file is the same finding at the next push, whatever its line.
func TestAFindingKeepsItsFingerprintAcrossLines(t *testing.T) {
	if fingerprint("a.go", "Fuite ") != fingerprint("a.go", "fuite") || fingerprint("a.go", "x") == fingerprint("b.go", "x") {
		t.Error("fingerprint")
	}
}

// A review whose task stopped wrote nothing to read: no verdict, rather than a clean review.
func TestAReviewNotWrittenIsNoVerdict(t *testing.T) {
	killed := int32(137)
	run := capability.RunState{Tasks: []capability.TaskState{{
		Name: reviewTask, Status: "Failed", Reason: "OOMKilled",
		Steps: []capability.StepState{{Name: "review", ExitCode: &killed}},
	}}}
	sets, err := reviews{}.Interpret(context.Background(), run, nil)
	if err != nil || len(sets) != 1 {
		t.Fatalf("%v, %v", sets, err)
	}
	if sets[0].Status != facts.StatusNA || sets[0].Findings != nil || sets[0].Summary == "" {
		t.Errorf("set = %+v", sets[0])
	}
	if err := sets[0].Validate(); err != nil {
		t.Error(err)
	}
}

// An empty report is the one a failed task left: no verdict, rather than an error that records
// nothing and leaves the previous review standing for this one.
func TestAnEmptyReportIsNoVerdict(t *testing.T) {
	sets, err := reviews{}.Interpret(context.Background(), capability.RunState{}, []parse.Record{
		{Member: "review/review.json", Payload: parse.Bytes("\n")},
	})
	if err != nil || len(sets) != 1 || sets[0].Status != facts.StatusNA {
		t.Fatalf("%v, %v", sets, err)
	}
}

func TestAnUnknownVerdictIsRefused(t *testing.T) {
	_, err := reviews{}.Interpret(context.Background(), capability.RunState{}, []parse.Record{
		{Member: "review/review.json", Payload: parse.Bytes(`{"verdict": "lgtm", "findings": []}`)},
	})
	if err == nil {
		t.Error("an unknown verdict read")
	}
}
