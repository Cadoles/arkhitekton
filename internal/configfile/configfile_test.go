package configfile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const example = `apiVersion: arkhitekton.config/v1
kind: Instance
spec:
  plugins:
    redmine:
      config:
        url: https://redmine.example
        api_key: {env: ARK_REDMINE_API_KEY}
    deploy:
      install: {reference: "registry.example/deploy@sha256:abc"}
      config: {overlayLocation: overlays/pipeline}
  queueLimits: {deploy/integration-tests: 1}
---
apiVersion: arkhitekton.config/v1
kind: Project
metadata: {name: shop}
spec:
  repository: team/shop
  plugins:
    docker: {config: {dockerfile: ./misc/Dockerfile}}
    deploy: {config: {deployer: flux}}
  pipelines: {deploy/e2e-tests: false}
  trackerStatuses: {"En cours": doing}
  releaseAutomation:
    enabled: false
    stages: [{role: build, gestes: [{kind: pipeline, ref: docker/build}, {kind: pipeline, ref: docker/build, promote: false}]}]
  reactions:
    - name: Analyse à chaque push
      event: gitea.push
      plan: {stages: [{gestes: [{kind: pipeline, ref: php/static-tests}]}]}
  schedules:
    - name: Intégration
      cron: "0 8,12,16 * * 1-5"
      timezone: Europe/Paris
      ref: migration-symfony
      enabled: false
      plan: {stages: [{gestes: [{kind: pipeline, ref: deploy/integration-tests}]}]}
`

func TestAConfigurationIsReadWithItsDefaults(t *testing.T) {
	set, err := Read([]byte(example), "example.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if set.Instance == nil || len(set.Projects) != 1 {
		t.Fatalf("set = %+v", set)
	}
	redmine := set.Instance.Plugins["redmine"]
	if !IsOn(redmine.Enabled) {
		t.Error("a declared plugin is not on by default")
	}
	if key := redmine.Config["api_key"]; key.Env != "ARK_REDMINE_API_KEY" || key.Plain != "" {
		t.Errorf("api_key = %+v, want a reference to ARK_REDMINE_API_KEY", key)
	}
	if set.Instance.Plugins["deploy"].Install == nil {
		t.Error("the install reference was lost")
	}

	project := set.Projects[0]
	if project.Name != "shop" || project.Pipelines["deploy/e2e-tests"] {
		t.Errorf("project = %+v", project)
	}
	if project.Plugins["docker"].Config["dockerfile"].Plain != "./misc/Dockerfile" {
		t.Errorf("docker settings = %+v", project.Plugins["docker"])
	}
	if !IsOn(project.Reactions[0].Enabled) || IsOn(project.Schedules[0].Enabled) {
		t.Errorf("switches: reaction %v, schedule %v", project.Reactions[0].Enabled, project.Schedules[0].Enabled)
	}
	automation := project.ReleaseAutomation
	if automation == nil || IsOn(automation.Enabled) || len(automation.Stages) != 1 {
		t.Fatalf("release automation = %+v, want its stage, switched off", automation)
	}
	if gestes := automation.Stages[0].Gestes; !gestes[0].Promoted() || gestes[1].Promoted() {
		t.Errorf("promote = %+v", gestes)
	}
}

// Encoding then reading gives the same set back, and encoding twice gives the same bytes.
func TestAnEncodedConfigurationReadsBackIdentically(t *testing.T) {
	set, err := Read([]byte(example), "example.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	first, err := Encode(set)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	again, err := Read(first, "encoded")
	if err != nil {
		t.Fatalf("read back: %v\n%s", err, first)
	}
	if !reflect.DeepEqual(set, again) {
		t.Errorf("read back differs:\n%+v\n%+v", set, again)
	}
	second, err := Encode(again)
	if err != nil {
		t.Fatalf("encode again: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("two encodings differ:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(string(first), "metadata: {}") || strings.Contains(string(first), "null") {
		t.Errorf("empty values written:\n%s", first)
	}
}

// An exported directory reads back as the set it was written from.
func TestADirectoryReadsBackAsItsSet(t *testing.T) {
	set, err := Read([]byte(example), "example.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	dir := t.TempDir()
	if err := WriteDir(dir, set); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ProjectsDir, "shop.yaml")); err != nil {
		t.Fatalf("project file: %v", err)
	}
	again, err := ReadPath(dir, nil)
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	if !reflect.DeepEqual(set, again) {
		t.Errorf("directory differs:\n%+v\n%+v", set, again)
	}
}

func TestMistakesAreRefusedWithTheirPath(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want string
	}{
		"misspelled key": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: p}\nspec: {repository: a/b, stak: php}\n",
			want: "stak",
		},
		"unknown version": {
			yaml: "apiVersion: arkhitekton.config/v2\nkind: Instance\nspec: {}\n",
			want: "apiVersion",
		},
		"two sources for a secret": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Instance\nspec: {plugins: {redmine: {config: {api_key: {env: A, file: /b}}}}}\n",
			want: "exactly one",
		},
		"reaction declared twice": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: p}\nspec:\n  repository: a/b\n  reactions:\n" +
				"    - {name: Push, event: gitea.push, plan: {stages: [{gestes: [{kind: pipeline, ref: a/b}]}]}}\n" +
				"    - {name: ' push ', event: gitea.push, plan: {stages: [{gestes: [{kind: pipeline, ref: a/b}]}]}}\n",
			want: "spec.reactions[1]",
		},
		"invalid cron": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: p}\nspec:\n  repository: a/b\n  schedules:\n" +
				"    - {name: Nightly, cron: 'not cron', plan: {stages: [{gestes: [{kind: pipeline, ref: a/b}]}]}}\n",
			want: "spec.schedules[0]",
		},
		"misspelled key beside a release automation's plan": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: p}\nspec:\n  repository: a/b\n" +
				"  releaseAutomation: {enable: false, stages: []}\n",
			want: "enable",
		},
		"a key moved to a plugin": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: p}\nspec: {repository: a/b, dockerfile: Dockerfile}\n",
			want: "plugins.docker.config.dockerfile",
		},
		"a project's images": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: p}\nspec: {repository: a/b, targets: [{image: portal}]}\n",
			want: "promote: false",
		},
		"bad slug": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Project\nmetadata: {name: My_Project}\nspec: {repository: a/b}\n",
			want: "metadata.name",
		},
		"install by a name alone": {
			yaml: "apiVersion: arkhitekton.config/v1\nkind: Instance\nspec: {plugins: {deploy: {install: {reference: 'deploy'}}}}\n",
			want: "registry/repository",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Read([]byte(test.yaml), "case.yaml")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %v, want one naming %q", err, test.want)
			}
		})
	}
}

// A project the instance no longer has disappears from an exported directory: applying the
// directory again would otherwise bring it back.
func TestAnExportRemovesTheFilesOfProjectsThatAreGone(t *testing.T) {
	set, err := Read([]byte(example), "example.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	dir := t.TempDir()
	gone := filepath.Join(dir, ProjectsDir, "gone.yaml")
	if err := os.MkdirAll(filepath.Dir(gone), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gone, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteDir(dir, set); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("the file of a project that is gone is still there (%v)", err)
	}
}
