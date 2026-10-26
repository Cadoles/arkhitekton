package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"

	dashhttp "forge.cadoles.com/vcarroy/arkhitekton/internal/http"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/installed"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// readingPlugin is an installable plugin whose runs are read by declaration.
func readingPlugin() fstest.MapFS {
	return fstest.MapFS{
		"arkhitekton-plugin.yaml": {Data: []byte(`apiVersion: arkhitekton.plugin/v2
id: junit
version: 1.0.0
sdk: "1"
label: JUnit
pipelines:
  - name: unit-tests
    label: Tests
    pipelineRef: junit-tests
    tekton: [pipeline.yaml]
    externalTasks: [run-tests]
    artifacts:
      - { name: results, param: results-upload-url, object: junit.xml, format: file,
          members: [{ match: junit.xml, parser: junit-xml, tool: junit }] }
    produces: [results]
    reading: reading.yaml
`)},
		"pipeline.yaml": {Data: []byte(`apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: junit-tests
spec:
  tasks:
    - name: run
      taskRef: { name: run-tests }
`)},
		"reading.yaml": {Data: []byte(`apiVersion: arkhitekton.reading/v1
tools:
  - tool: junit
    group: unit
    summary: tests
    cases:
      - { from: junit.cases, suite: "{path}", name: "{name}", status: passed }
`)},
	}
}

type installations struct {
	installation store.PluginInstallation
	completed    string
	failed       string
	audited      []string
}

func (i *installations) PluginInstallation(context.Context, int64) (store.PluginInstallation, error) {
	return i.installation, nil
}

func (i *installations) CompletePluginInstallation(_ context.Context, _ int64, pluginID, version, signedBy, object string) (bool, error) {
	i.completed = strings.Join([]string{pluginID, version, signedBy, object}, " ")
	return true, nil
}

func (i *installations) FailPluginInstallation(_ context.Context, _ int64, reason string) error {
	i.failed = reason
	return nil
}

func (i *installations) Audit(_ context.Context, actor, action, target string, _ map[string]any) error {
	i.audited = append(i.audited, actor+" "+action+" "+target)
	return nil
}

const pinnedDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func pulledPlugin(context.Context, string) (installed.Pulled, error) {
	return installed.Pulled{
		Reference: "registry.example/plugins/junit@" + pinnedDigest, Digest: pinnedDigest,
		SignedBy: "sha256:key", Layer: []byte("layer"), Files: readingPlugin(),
	}, nil
}

func TestAnInstallationPullsChecksAndStores(t *testing.T) {
	db := &installations{installation: store.PluginInstallation{ID: 4, State: store.InstallationPending, RequestedBy: "admin"}}
	objects := memoryObjects{}
	changed := 0
	installer := NewPluginInstaller(db, objects, pulledPlugin,
		func(context.Context, capability.Plugin) error { return nil },
		func(context.Context) { changed++ }, slog.New(slog.DiscardHandler))

	if err := installer.Run(context.Background(), store.Job{ID: 1, Params: map[string]string{ParamInstallation: "4"}}); err != nil {
		t.Fatal(err)
	}
	key := "plugins/junit/1.0.0/sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tar.gz"
	if string(objects[key]) != "layer" {
		t.Errorf("objects = %v", objects)
	}
	if db.completed != "junit 1.0.0 sha256:key "+key || db.failed != "" || changed != 1 {
		t.Errorf("completed %q, failed %q, changed %d", db.completed, db.failed, changed)
	}
	if len(db.audited) != 1 || db.audited[0] != "admin plugin.installed junit" {
		t.Errorf("audited = %v", db.audited)
	}
}

// A plugin that would not fit with the capabilities in force is refused, and the installation says
// why; nothing is stored.
func TestARefusedInstallationSaysWhy(t *testing.T) {
	db := &installations{installation: store.PluginInstallation{ID: 4, State: store.InstallationPending, RequestedBy: "admin",
		Reference: "registry.example/plugins/junit@" + pinnedDigest}}
	objects := memoryObjects{}
	installer := NewPluginInstaller(db, objects, pulledPlugin,
		func(context.Context, capability.Plugin) error { return errors.New(`plugin "junit" declared twice`) },
		nil, slog.New(slog.DiscardHandler))

	err := installer.Run(context.Background(), store.Job{ID: 1, Params: map[string]string{ParamInstallation: "4"}})
	if err == nil || !strings.Contains(db.failed, "declared twice") || len(objects) != 0 || db.completed != "" {
		t.Errorf("err %v, failed %q, objects %v", err, db.failed, objects)
	}
	if len(db.audited) != 1 || !strings.HasPrefix(db.audited[0], "admin plugin.installation.failed ") {
		t.Errorf("audited = %v", db.audited)
	}

	db.installation.State = store.InstallationInstalled
	if err := installer.Run(context.Background(), store.Job{ID: 2, Params: map[string]string{ParamInstallation: "4"}}); err == nil {
		t.Error("an installation carried out twice")
	}
}

// Written on both sides, as the release note's: diverging, a requested installation would stay
// queued with nothing to carry it out.
func TestTheInstallationJobIsTheSameOnBothSides(t *testing.T) {
	if KindPluginInstall != dashhttp.PluginInstallJobKind || ParamInstallation != dashhttp.PluginInstallationKey {
		t.Errorf("worker: %q %q, api: %q %q", KindPluginInstall, ParamInstallation,
			dashhttp.PluginInstallJobKind, dashhttp.PluginInstallationKey)
	}
}
