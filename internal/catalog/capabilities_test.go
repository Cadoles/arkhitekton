package catalog

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// noFacts reads nothing: these tests are about binding, not reading.
type noFacts struct{}

func (noFacts) Interpret(context.Context, capability.RunState, []parse.Record) ([]facts.Set, error) {
	return nil, nil
}

func deployRegistry(t *testing.T) *capabilities.Registry {
	t.Helper()
	registry, err := capabilities.New(capability.Plugin{ID: "mse", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{
		Name: "integration-tests", Label: "Tests d'intégration", Interpreter: noFacts{},
		Spec: capability.PipelineSpec{
			PipelineRef: "php-integration-test", Exclusive: true, Verification: true, Group: "integration",
			Produces: []capability.Product{capability.ProductResults},
			Params: map[string]capability.Param{
				"mse-repo":  {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
				"image-tag": {Source: capability.ParamInput, Required: true, Suggest: SuggestReleaseTag},
			},
			Workspaces: []capability.Workspace{
				{Name: "git-credentials", Need: capability.NeedSourceRead},
				{Name: "mse", Volume: &capability.Volume{Storage: "1Gi"}},
			},
			Needs: []capability.Need{{Name: capability.NeedSourceRead}, {Name: capability.NeedClusterDeploy}},
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "integration-tests.tar.gz",
				Format: capability.FormatArchive,
			}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestABindingResolvesIntoAPipeline(t *testing.T) {
	parsed, err := Parse([]byte(`
capabilities:
  - ref: mse/integration-tests
    timeout: 45m
    params:
      mse-repo: { key: forge.mse.repoUrl }
    needs:
      cluster-deploy: { serviceAccountName: tekton-kubectl }
      source-read:    { secret: { configKey: pipeline.gitSecret } }
    volumes:
      mse: { storageClassName: standard, storage: 2Gi }
`), WithCapabilities(deployRegistry(t)))
	if err != nil {
		t.Fatal(err)
	}

	pipeline, found := parsed.Find("mse/integration-tests")
	if !found {
		t.Fatalf("kinds = %v", parsed.Kinds())
	}
	if pipeline.Capability != "mse/integration-tests" || pipeline.Unavailable != "" {
		t.Errorf("pipeline = %+v", pipeline)
	}
	if pipeline.ServiceAccountName != "tekton-kubectl" || pipeline.MaxConcurrent != 1 || pipeline.Timeout != "45m" {
		t.Errorf("instantiation = %+v", pipeline)
	}
	if pipeline.Params["mse-repo"].Key != "forge.mse.repoUrl" || pipeline.Params["image-tag"].Source != SourceInput {
		t.Errorf("params = %+v", pipeline.Params)
	}
	if pipeline.Uploads["results"].Group != "integration" {
		t.Errorf("uploads = %+v", pipeline.Uploads)
	}
	for _, workspace := range pipeline.Workspaces {
		if workspace.Name == "mse" && (workspace.VolumeClaimTemplate.Storage != "2Gi" ||
			workspace.VolumeClaimTemplate.StorageClassName != "standard") {
			t.Errorf("volume = %+v", workspace.VolumeClaimTemplate)
		}
	}
}

// An unbound need is not a broken catalogue: the capability stays known, so a plan naming it
// stays valid, and its runs are refused with the reason.
func TestAnUnboundNeedMakesThePipelineUnavailable(t *testing.T) {
	parsed, err := Parse([]byte(`
capabilities:
  - ref: mse/integration-tests
    needs:
      source-read: { secret: { configKey: pipeline.gitSecret } }
`), WithCapabilities(deployRegistry(t)))
	if err != nil {
		t.Fatal(err)
	}
	pipeline, _ := parsed.Find("mse/integration-tests")
	if !strings.Contains(pipeline.Unavailable, "cluster-deploy") {
		t.Errorf("unavailable = %q", pipeline.Unavailable)
	}
}

// A service account is a cluster identity: granting one under a need that is not deploying would
// let a binding give more than its name says.
func TestOnlyDeployingTakesAServiceAccount(t *testing.T) {
	_, err := Parse([]byte(`
capabilities:
  - ref: mse/integration-tests
    needs:
      cluster-deploy: { serviceAccountName: tekton-kubectl }
      source-read:    { serviceAccountName: admin, secret: { secretName: git } }
`), WithCapabilities(deployRegistry(t)))
	if err == nil || !strings.Contains(err.Error(), "service account") {
		t.Errorf("err = %v", err)
	}
}

func TestABindingNamesOnlyWhatTheCapabilityDeclares(t *testing.T) {
	for name, source := range map[string]string{
		"unknown capability": "  - ref: mse/unknown\n",
		"unknown param":      "  - ref: mse/integration-tests\n    params:\n      other: { key: x }\n",
		"input param":        "  - ref: mse/integration-tests\n    params:\n      image-tag: { key: x }\n",
		"undeclared need":    "  - ref: mse/integration-tests\n    needs:\n      registry-push: { secret: { secretName: r } }\n",
		"undeclared volume":  "  - ref: mse/integration-tests\n    volumes:\n      other: { storage: 1Gi }\n",
	} {
		if _, err := Parse([]byte("capabilities:\n"+source), WithCapabilities(deployRegistry(t))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestBindingsNeedThePlugins(t *testing.T) {
	if _, err := Parse([]byte("capabilities:\n  - ref: mse/integration-tests\n")); err == nil {
		t.Error("bindings resolved without plugins")
	}
}

// A pipeline at odds with the cluster is refused on its own: the others keep running.
func TestAvailabilityIsPerPipeline(t *testing.T) {
	parsed, err := parseDeclared([]byte(`
pipelines:
  - kind: unit-tests
    pipelineRef: php-unit-tests
    produces: [none]
  - kind: static-tests
    pipelineRef: php-static-tests
    produces: [none]
`))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Pipelines[1].ExternalTasks = []string{"composer-install"}

	unavailable, err := parsed.Availability(context.Background(), fakeCluster{
		pipelines: map[string]PipelineSpec{
			"php-unit-tests":   {Name: "php-unit-tests"},
			"php-static-tests": {Name: "php-static-tests"},
		},
		tasks: map[string]bool{"git-clone": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := unavailable["unit-tests"]; found {
		t.Errorf("unit-tests refused: %v", unavailable)
	}
	if !strings.Contains(unavailable["static-tests"], "composer-install") {
		t.Errorf("static-tests = %q, expected the missing Task named", unavailable["static-tests"])
	}
}

// An administrator's binding completes the file, never overrides it, and never names a service
// account.
func TestRecordedBindingsCompleteTheFile(t *testing.T) {
	registry, err := capabilities.New(capability.Plugin{ID: "go", Version: "1", APIVersion: sdk.APIVersion,
		Pipelines: []capability.Pipeline{{Name: "unit", Label: "Tests Go", Interpreter: noFacts{},
			Spec: capability.PipelineSpec{
				PipelineRef: "go-unit", Produces: []capability.Product{capability.ProductResults},
				Artifacts:  []capability.Artifact{{Name: "results", Param: "results-upload-url", Object: "go-test.json", Format: capability.FormatFile}},
				Workspaces: []capability.Workspace{{Name: "git-credentials", Need: capability.NeedSourceRead}},
				Needs:      []capability.Need{{Name: capability.NeedSourceRead}},
			}}}})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := ParseBinding([]byte(`{"ref": "go/unit", "timeout": "20m",
		"needs": {"source-read": {"secret": {"configKey": "pipeline.gitSecret"}}}}`))
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse([]byte("maxConcurrentRuns: 2\n"), WithCapabilities(registry), WithBindings([]Binding{recorded}))
	if err != nil {
		t.Fatal(err)
	}
	pipeline, found := parsed.Find("go/unit")
	if !found || pipeline.Timeout != "20m" || parsed.BoundByFile("go/unit") {
		t.Errorf("pipeline %+v, found %v", pipeline, found)
	}

	fromFile, err := Parse([]byte("capabilities:\n  - ref: go/unit\n    timeout: 5m\n    needs:\n      source-read: { secret: { secretName: git } }\n"),
		WithCapabilities(registry), WithBindings([]Binding{recorded}))
	if err != nil {
		t.Fatal(err)
	}
	if pipeline, _ := fromFile.Find("go/unit"); pipeline.Timeout != "5m" || !fromFile.BoundByFile("go/unit") {
		t.Errorf("the file's binding was overridden: %+v", pipeline)
	}

	if _, err := ParseBinding([]byte(`{"ref": "go/unit", "needs": {"cluster-deploy": {"serviceAccountName": "admin"}}}`)); err == nil {
		t.Error("a recorded service account accepted")
	}
	if _, err := ParseBinding([]byte(`{"ref": "go/unit", "timeouts": "5m"}`)); err == nil {
		t.Error("an unknown key accepted")
	}
}

// The file binds, before its plugin is installed, what only the file can bind: the binding waits.
func TestABindingWaitsForItsPlugin(t *testing.T) {
	parsed, err := Parse([]byte(`
capabilities:
  - ref: mse/integration-tests
    needs:
      source-read: { secret: { configKey: pipeline.gitSecret } }
  - ref: absent/deploy
    needs:
      cluster-deploy: { serviceAccountName: deployer }
`), WithCapabilities(deployRegistry(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Awaiting) != 1 || parsed.Awaiting[0] != "absent/deploy" {
		t.Errorf("awaiting = %v", parsed.Awaiting)
	}
	if _, found := parsed.Find("absent/deploy"); found {
		t.Error("a capability without its plugin is launchable")
	}
}

// policyRegistry declares a capability expecting data the operator owns.
//
// Separate from [deployRegistry]: a config map is not a privilege, and mixing the two in one
// fixture would hide which of the two paths a failure came from.
func policyRegistry(t *testing.T) *capabilities.Registry {
	t.Helper()
	registry, err := capabilities.New(capability.Plugin{ID: "security", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{
		Name: "security-audit", Label: "Audit de sécurité", Interpreter: noFacts{},
		Spec: capability.PipelineSpec{
			PipelineRef: "security-audit", Group: "security",
			Produces: []capability.Product{capability.ProductNone},
			Workspaces: []capability.Workspace{
				{Name: "policies", ConfigMap: &capability.ConfigMapWorkspace{Reason: "the severity scale"}},
			},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestAConfigMapIsBoundByNameOrByKey(t *testing.T) {
	for name, binding := range map[string]string{
		"a literal name":      "policies: { name: security-policies }",
		"a configuration key": "policies: { configKey: security.policies }",
	} {
		parsed, err := Parse([]byte(`
capabilities:
  - ref: security/security-audit
    configMaps:
      `+binding+`
`), WithCapabilities(policyRegistry(t)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		pipeline, found := parsed.Find("security/security-audit")
		if !found {
			t.Fatalf("%s: kinds = %v", name, parsed.Kinds())
		}
		if pipeline.Unavailable != "" {
			t.Fatalf("%s: unavailable: %s", name, pipeline.Unavailable)
		}
		source := pipeline.Workspaces[0].ConfigMap
		if source == nil || (source.Name == "" && source.ConfigKey == "") {
			t.Errorf("%s: workspace = %+v", name, pipeline.Workspaces[0])
		}
	}
}

// Left unbound, the workspace has no source and start-up stops, naming it. The same thing an
// unbound need does today: launching the pipeline would create a PipelineRun whose workspace is
// bound to nothing, which Tekton accepts before failing at mount time, where the reason is harder
// to read.
func TestAnUnboundConfigMapStopsStartUp(t *testing.T) {
	_, err := Parse([]byte(`
capabilities:
  - ref: security/security-audit
`), WithCapabilities(policyRegistry(t)))
	if err == nil {
		t.Fatal("a capability expecting a config map was accepted with none bound")
	}
	if !strings.Contains(err.Error(), "policies") {
		t.Errorf("error = %v", err)
	}
}

func TestAConfigMapBindingIsChecked(t *testing.T) {
	for name, binding := range map[string]string{
		"a workspace the capability does not declare": "rules: { name: semgrep-rules }",
		"a name and a key together":                   "policies: { name: p, configKey: security.policies }",
	} {
		_, err := Parse([]byte(`
capabilities:
  - ref: security/security-audit
    configMaps:
      `+binding+`
`), WithCapabilities(policyRegistry(t)))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
