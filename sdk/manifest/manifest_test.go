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
    "produces": ["results"], "group": "unit", "reading": "reading.yaml"
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
		"absolute path":      {`"reading.yaml"`, `"/reading.yaml"`},
		"no reading":         {`, "reading": "reading.yaml"`, ``},
		"former module":      {`"reading": "reading.yaml"`, `"module": "gotest.wasm"`},
		"an unknown need":    {`"name": "source-read", "reason"`, `"name": "root", "reason"`},
		"pushing images":     {`"produces": ["results"]`, `"produces": ["results", "image"]`},
		"undeclared need":    {`"need": "source-read"`, `"need": "registry-pull"`},
		"no reason":          {`"reason": "clone the project"`, `"reason": ""`},
		"two sources":        {`"volume": {"storage": "1Gi"}`, `"volume": {"storage": "1Gi"}, "emptyDir": true`},
		"unknown param":      {`"source": "input"`, `"source": "environment"`},
		"unknown format":     {`"format": "file"`, `"format": "zip"`},
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
  "ui": {"entry": "ui/index.js", "styles": ["ui/index.css"], "resources": ["ui/resources/*.yaml"]}
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
		"resources not YAML":    {`"ui/resources/*.yaml"`, `"ui/resources/*.json"`},
		"resources outside":     {`"ui/resources/*.yaml"`, `"../resources/*.yaml"`},
		"styles without entry":  {`"entry": "ui/index.js", `, ``},
		"empty interface":       {`"entry": "ui/index.js", "styles": ["ui/index.css"], "resources": ["ui/resources/*.yaml"]`, ``},
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
	native := strings.Replace(valid, `"reading": "reading.yaml"`, `"interpreter": "native"`, 1)
	if _, err := Decode([]byte(native)); err == nil || !strings.Contains(err.Error(), "program") {
		t.Errorf("err = %v", err)
	}
	if _, err := Decode([]byte(strings.Replace(native, `"interpreter": "native"`, `"interpreter": "wasm"`, 1))); err == nil {
		t.Error("an unknown interpreter accepted")
	}
}

func TestAPipelineReadsItsRunsOneWay(t *testing.T) {
	both := strings.Replace(valid, `"reading": "reading.yaml"`, `"reading": "reading.yaml", "interpreter": "native"`, 1)
	both = strings.Replace(both, `"label": "Go",`, `"label": "Go", "binary": "bin/plugin", "protocol": 1,`, 1)
	if _, err := Decode([]byte(both)); err == nil || !strings.Contains(err.Error(), "one way") {
		t.Errorf("err = %v", err)
	}
}

// A pipeline the operator provides ships no definition; its inputs say what the form offers.
func TestAPipelineMayLeaveItsDefinitionsToTheOperator(t *testing.T) {
	provided := strings.Replace(valid, `"tekton": ["tekton/pipeline.yaml", "tekton/task.yaml"], `, ``, 1)
	provided = strings.Replace(provided, `"revision": {"source": "input", "required": true}`,
		`"revision": {"source": "input", "required": true, "suggest": "git-ref"}`, 1)
	declared, err := Decode([]byte(provided))
	if err != nil {
		t.Fatal(err)
	}
	if suggest := declared.Plugin().Pipelines[0].Spec.Params["revision"].Suggest; suggest != "git-ref" {
		t.Errorf("suggest = %q", suggest)
	}
}

// Deploying is asked for, and only the operator's catalogue grants it: a binding from the screen
// names no service account.
func TestAPluginMayAskToDeploy(t *testing.T) {
	deploying := strings.Replace(valid, `"needs": [{"name": "source-read", "reason": "clone the project"}]`,
		`"needs": [{"name": "source-read", "reason": "clone the project"}, {"name": "cluster-deploy", "reason": "deploy the release"}]`, 1)
	if _, err := Decode([]byte(deploying)); err != nil {
		t.Error(err)
	}
}

const withProviders = `{
  "apiVersion": "arkhitekton.plugin/v2", "id": "forgery", "version": "1.0.0", "sdk": "1", "label": "Forgery",
  "perProject": true, "binary": "bin/plugin", "protocol": 1, "tracker": true, "forge": true,
  "events": [{"suffix": "pull_request.opened", "label": "PR ouverte"}],
  "probes": [{"name": "pull_requests", "interval": "5m", "eager": true}],
  "actions": [{"suffix": "status", "label": "Statut de commit", "summary": "Pose un statut.",
    "fields": [{"name": "commit", "label": "Commit", "required": true, "template": true},
      {"name": "state", "label": "État", "one_of": ["success", "failure"]}]}]
}`

func TestAProgramMayBringATrackerAForgeProbesAndActions(t *testing.T) {
	declared, err := Decode([]byte(withProviders))
	if err != nil {
		t.Fatal(err)
	}
	if schedule := declared.Probes[0].Schedule(); schedule.Interval.Minutes() != 5 || !schedule.Eager {
		t.Errorf("schedule = %+v", schedule)
	}
	plugin := declared.Plugin()
	if len(plugin.Actions) != 1 || plugin.ActionRef(plugin.Actions[0]) != "forgery.status" ||
		plugin.Actions[0].Fields[1].OneOf[1] != "failure" {
		t.Errorf("actions = %+v", plugin.Actions)
	}
	// The dashboard attaches what runs them: the manifest only declares.
	if plugin.Tracker != nil || plugin.Forge != nil || plugin.Executor != nil || len(plugin.Probes) != 0 {
		t.Errorf("plugin = %+v", plugin)
	}
}

func TestATrackerAForgeProbesAndActionsAreChecked(t *testing.T) {
	for name, edit := range map[string][2]string{
		"no program":            {`"binary": "bin/plugin", "protocol": 1, `, ``},
		"not per project":       {`"perProject": true, `, ``},
		"probe without events":  {`"events": [{"suffix": "pull_request.opened", "label": "PR ouverte"}],`, ``},
		"badly named probe":     {`"name": "pull_requests"`, `"name": "Pull Requests"`},
		"probe twice":           {`"eager": true}]`, `"eager": true}, {"name": "pull_requests"}]`},
		"bad interval":          {`"interval": "5m"`, `"interval": "often"`},
		"negative interval":     {`"interval": "5m"`, `"interval": "-5m"`},
		"bad action suffix":     {`"suffix": "status"`, `"suffix": "Set Status"`},
		"action without label":  {`"label": "Statut de commit"`, `"label": ""`},
		"badly named parameter": {`"name": "commit"`, `"name": "Commit SHA"`},
		"parameter twice":       {`"name": "state"`, `"name": "commit"`},
	} {
		if !strings.Contains(withProviders, edit[0]) {
			t.Fatalf("%s: %q not in the manifest", name, edit[0])
		}
		if _, err := Decode([]byte(strings.Replace(withProviders, edit[0], edit[1], 1))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A ConfigMap workspace is the fourth source, and the schema counts it: without that, a manifest
// declaring one would be read as declaring none, and refused for having no source.
func TestAWorkspaceMayExpectAConfigMap(t *testing.T) {
	declared, err := Decode([]byte(strings.Replace(valid,
		`{"name": "shared-data", "volume": {"storage": "1Gi"}}`,
		`{"name": "policies", "configMap": {"reason": "the severity scale"}}`, 1)))
	if err != nil {
		t.Fatal(err)
	}

	workspace := declared.Plugin().Pipelines[0].Spec.Workspaces[1]
	if workspace.ConfigMap == nil || workspace.ConfigMap.Reason != "the severity scale" {
		t.Errorf("workspace = %+v", workspace)
	}
}

func TestAConfigMapWorkspaceIsChecked(t *testing.T) {
	for name, replacement := range map[string]string{
		// A binding is the operator's act, and nothing else in the manifest says which ConfigMap.
		"no reason": `{"name": "policies", "configMap": {}}`,
		// Two sources at once: the host would have to guess which one to mount.
		"two sources": `{"name": "policies", "configMap": {"reason": "r"}, "emptyDir": true}`,
	} {
		edited := strings.Replace(valid, `{"name": "shared-data", "volume": {"storage": "1Gi"}}`, replacement, 1)
		if _, err := Decode([]byte(edited)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

var withWebhook = strings.Replace(withProviders, `"tracker": true,`, `"tracker": true,
  "settings": {"project": [{"name": "webhook_secret", "label": "Secret", "type": "text", "secret": true}]},
  "webhook": {"secret": "webhook_secret", "signature": "X-Hub-Signature-256", "prefix": "sha256=",
    "scheme": "hmac-sha256", "event": "X-GitHub-Event"},`, 1)

// The manifest declares how a delivery is authenticated; the program only reads it, and the
// dashboard attaches what calls the program.
func TestAProgramMayReceiveWebhooks(t *testing.T) {
	declared, err := Decode([]byte(withWebhook))
	if err != nil {
		t.Fatal(err)
	}
	spec := declared.Webhook.Spec()
	if spec.Secret != "webhook_secret" || spec.Prefix != "sha256=" || spec.Scheme != capability.SignatureHMACSHA256 ||
		spec.Signature != "X-Hub-Signature-256" || spec.Event != "X-GitHub-Event" || spec.Receiver != nil {
		t.Errorf("webhook = %+v", spec)
	}
	if declared.Plugin().Webhook != nil {
		t.Error("the manifest attached a webhook nothing reads")
	}
}

func TestAWebhookIsChecked(t *testing.T) {
	for name, edit := range map[string][2]string{
		"no program":        {`"binary": "bin/plugin", "protocol": 1, `, ``},
		"no events":         {`"events": [{"suffix": "pull_request.opened", "label": "PR ouverte"}],`, `"events": [],`},
		"unknown scheme":    {`"scheme": "hmac-sha256"`, `"scheme": "md5"`},
		"bad header":        {`"event": "X-GitHub-Event"`, `"event": "X GitHub Event"`},
		"no signature":      {`"signature": "X-Hub-Signature-256", `, ``},
		"secret not typed":  {`"secret": "webhook_secret", "signature"`, `"secret": "token", "signature"`},
		"secret not secret": {`"type": "text", "secret": true`, `"type": "text"`},
	} {
		if !strings.Contains(withWebhook, edit[0]) {
			t.Fatalf("%s: %q not in the manifest", name, edit[0])
		}
		edited := strings.Replace(withWebhook, edit[0], edit[1], 1)
		if name == "no events" {
			// Probes report events too: without them, the probe would be the refusal, not the webhook.
			edited = strings.Replace(edited, `"probes": [{"name": "pull_requests", "interval": "5m", "eager": true}],`, ``, 1)
		}
		if _, err := Decode([]byte(edited)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
