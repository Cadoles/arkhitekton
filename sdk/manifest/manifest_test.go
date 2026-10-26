package manifest

import (
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

const valid = `{
  "apiVersion": "arkhitekton.plugin/v2", "id": "gotest", "version": "1.0.0", "sdk": "1", "label": "Go",
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
		"former schema":      {`arkhitekton.plugin/v2`, `arkhitekton.plugin/v1`},
		"other sdk":          {`"sdk": "1"`, `"sdk": "2"`},
		"bad id":             {`"id": "gotest"`, `"id": "Go Test"`},
		"bad version":        {`"version": "1.0.0"`, `"version": "1.0/../x"`},
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

const withProgram = `{
  "apiVersion": "arkhitekton.plugin/v2", "id": "deploy", "version": "2.0.0", "sdk": "1", "label": "Deploy",
  "perProject": true, "binary": "bin/plugin", "protocol": 1, "module": true,
  "migrations": {"dir": "migrations", "table": "goose_deploy_db_version"},
  "events": [{"suffix": "release.promoted", "label": "Promotion aboutie"}],
  "hooks": ["deployments", "promotionFacts"],
  "routes": [
    {"method": "GET", "path": "environments", "permission": "read"},
    {"method": "POST", "path": "releases/{tag}/promote/{environment}", "permission": "deploy:{environment}"},
    {"method": "GET", "path": "status/{rest...}", "permission": "read", "scope": "instance"}
  ],
  "ui": {"entry": "ui/index.js", "styles": ["ui/index.css"]}
}`

func TestAProgramBringsWhatTheDashboardRunsIt(t *testing.T) {
	declared, err := Decode([]byte(withProgram))
	if err != nil {
		t.Fatal(err)
	}
	plugin := declared.Plugin()
	if !plugin.PerProject || len(plugin.Events) != 1 || len(plugin.Capabilities) != 1 ||
		plugin.Capabilities[0].Kind != capability.KindModule {
		t.Errorf("plugin = %+v", plugin)
	}
	if parameter, deploy := declared.Routes[1].DeployParameter(); !deploy || parameter != "environment" {
		t.Errorf("deploy parameter = %q, %v", parameter, deploy)
	}
	if declared.Routes[0].EffectiveScope() != ScopeProject {
		t.Error("a route without a scope is not a project's")
	}
}

func TestAProgramIsCheckedForWhatItBrings(t *testing.T) {
	for name, edit := range map[string][2]string{
		"no program":            {`"binary": "bin/plugin", `, ``},
		"other protocol":        {`"protocol": 1`, `"protocol": 2`},
		"program outside":       {`"bin/plugin"`, `"../plugin"`},
		"the core's table":      {`"goose_deploy_db_version"`, `"goose_db_version"`},
		"quoted table":          {`"goose_deploy_db_version"`, `"Deploy-Versions"`},
		"unknown hook":          {`"promotionFacts"`, `"everything"`},
		"hook twice":            {`"promotionFacts"`, `"deployments"`},
		"unknown method":        {`"method": "GET", "path": "environments"`, `"method": "TRACE", "path": "environments"`},
		"bad segment":           {`"path": "environments"`, `"path": "../environments"`},
		"rest not last":         {`"status/{rest...}"`, `"{rest...}/status"`},
		"deploy on nothing":     {`"deploy:{environment}"`, `"deploy:{target}"`},
		"deploy on an instance": {`"permission": "read", "scope": "instance"`, `"permission": "deploy:{rest}", "scope": "instance"`},
		"unknown scope":         {`"scope": "instance"`, `"scope": "cluster"`},
		"route twice":           {`"path": "environments", "permission": "read"`, `"path": "status/{rest...}", "permission": "read", "scope": "instance"`},
		"entry not a script":    {`"ui/index.js"`, `"ui/index.html"`},
		"style not a sheet":     {`"ui/index.css"`, `"ui/index.js"`},
		"event without label":   {`"label": "Promotion aboutie"`, `"label": ""`},
	} {
		if !strings.Contains(withProgram, edit[0]) {
			t.Fatalf("%s: %q not in the manifest", name, edit[0])
		}
		if _, err := Decode([]byte(strings.Replace(withProgram, edit[0], edit[1], 1))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestANativeReadingNeedsAProgram(t *testing.T) {
	native := strings.Replace(valid, `"module": "gotest.wasm"`, `"interpreter": "native"`, 1)
	if _, err := Decode([]byte(native)); err == nil || !strings.Contains(err.Error(), "program") {
		t.Errorf("err = %v", err)
	}
	if _, err := Decode([]byte(strings.Replace(native, `"interpreter": "native"`, `"interpreter": "wasm"`, 1))); err == nil {
		t.Error("an unknown interpreter accepted")
	}
}
