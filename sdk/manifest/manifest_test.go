package manifest

import (
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

const valid = `{
  "apiVersion": "arkhitekton.plugin/v1", "id": "gotest", "version": "1.0.0", "sdk": "1", "label": "Go",
  "pipelines": [{
    "name": "unit-tests", "label": "Tests Go", "pipelineRef": "go-unit-tests",
    "tekton": ["tekton/pipeline.yaml", "tekton/task.yaml"], "externalTasks": ["git-clone"],
    "params": {"repo-url": {"source": "binding", "defaultKey": "project.repoUrl"}, "revision": {"source": "input", "required": true}},
    "workspaces": [{"name": "git-credentials", "need": "source-read"}, {"name": "shared-data", "volume": {"storage": "1Gi"}}],
    "needs": [{"name": "source-read", "reason": "clone the project"}],
    "artifacts": [{"name": "results", "param": "results-upload-url", "object": "go-test.json", "format": "file",
      "members": [{"match": "go-test.json", "parser": "raw", "tool": "gotest"}]}],
    "produces": ["results"], "group": "unit", "module": "gotest.wasm"
  }]
}`

func TestAManifestBecomesAPlugin(t *testing.T) {
	declared, err := Decode([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	plugin := declared.Plugin()
	if plugin.ID != "gotest" || plugin.APIVersion != "1" || len(plugin.Pipelines) != 1 {
		t.Fatalf("plugin = %+v", plugin)
	}
	spec := plugin.Pipelines[0].Spec
	if spec.PipelineRef != "go-unit-tests" || spec.Params["revision"].Source != capability.ParamInput ||
		spec.Workspaces[1].Volume.Storage != "1Gi" || spec.Artifacts[0].Members[0].Tool != "gotest" ||
		spec.Needs[0].Name != capability.NeedSourceRead || spec.Group != "unit" {
		t.Errorf("spec = %+v", spec)
	}
}

func TestAManifestAsksForNothingBeyondItsSchema(t *testing.T) {
	for name, edit := range map[string][2]string{
		"service account":    {`"group": "unit"`, `"group": "unit", "serviceAccountName": "admin"`},
		"other schema":       {`arkhitekton.plugin/v1`, `arkhitekton.plugin/v2`},
		"other sdk":          {`"sdk": "1"`, `"sdk": "2"`},
		"bad id":             {`"id": "gotest"`, `"id": "Go Test"`},
		"path outside":       {`"tekton/task.yaml"`, `"../task.yaml"`},
		"absolute path":      {`"gotest.wasm"`, `"/gotest.wasm"`},
		"two readings":       {`"module": "gotest.wasm"`, `"module": "gotest.wasm", "reading": "reading.yaml"`},
		"no reading":         {`, "module": "gotest.wasm"`, ``},
		"deploying":          {`"name": "source-read", "reason"`, `"name": "cluster-deploy", "reason"`},
		"pushing images":     {`"produces": ["results"]`, `"produces": ["results", "image"]`},
		"undeclared need":    {`"need": "source-read"`, `"need": "registry-pull"`},
		"no reason":          {`"reason": "clone the project"`, `"reason": ""`},
		"two sources":        {`"volume": {"storage": "1Gi"}`, `"volume": {"storage": "1Gi"}, "emptyDir": true`},
		"unknown param":      {`"source": "input"`, `"source": "environment"`},
		"unknown format":     {`"format": "file"`, `"format": "zip"`},
		"no Tekton":          {`"tekton/pipeline.yaml", "tekton/task.yaml"`, ``},
		"incomplete member":  {`"tool": "gotest"`, `"tool": ""`},
		"incomplete product": {`"object": "go-test.json"`, `"object": ""`},
	} {
		if !strings.Contains(valid, edit[0]) {
			t.Fatalf("%s: %q not in the manifest", name, edit[0])
		}
		if _, err := Decode([]byte(strings.Replace(valid, edit[0], edit[1], 1))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
