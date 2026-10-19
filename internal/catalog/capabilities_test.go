package catalog

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
)

func deployRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	registry, err := capability.NewRegistry(capability.Plugin{ID: "mse", Version: "1", Pipelines: []capability.Pipeline{{
		Name: "integration-tests", Label: "Tests d'intégration",
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
	// A kind from before qualified references still finds it, for what an older replica enqueues
	// during an upgrade.
	if legacy, found := parsed.Find("integration-tests"); !found || legacy.Kind != "mse/integration-tests" {
		t.Errorf("legacy kind found %v: %+v", found, legacy)
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
	if pipeline.Uploads["results"].Family != "integration" {
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
	parsed, err := Parse([]byte(`
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
