package installed

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

/*
 * The example plugin, rpc/examples/gotest, read as an administrator would install it.
 */

const example = "../../rpc/examples/gotest"

// exampleProgram stands for the example's program: reading a plugin does not run it.
var exampleProgram = []byte("the example's program")

// plugin is the example's directory in memory, its program built.
func plugin(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{"bin/plugin": {Data: exampleProgram}}
	for _, name := range []string{manifest.FileName, "tekton/pipeline-go-unit-tests.yaml", "tekton/task-go-test.yaml"} {
		raw, err := os.ReadFile(filepath.Join(example, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	return files
}

func edit(t *testing.T, files fstest.MapFS, name, old, replacement string) {
	t.Helper()
	text := string(files[name].Data)
	if !strings.Contains(text, old) {
		t.Fatalf("%q not in %s", old, name)
	}
	files[name] = &fstest.MapFile{Data: []byte(strings.Replace(text, old, replacement, 1))}
}

func TestTheExamplePluginLoads(t *testing.T) {
	loaded, err := Load(plugin(t))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "gotest" || loaded.Version != "1.0.0" || len(loaded.Pipelines) != 1 {
		t.Fatalf("plugin = %+v", loaded)
	}
	pipeline := loaded.Pipelines[0]
	kinds := map[string]string{}
	for _, definition := range pipeline.Spec.Manifests {
		kinds[definition.Name] = definition.Kind
	}
	if kinds["go-unit-tests"] != "Pipeline" || kinds["go-test"] != "Task" || pipeline.Interpreter == nil || loaded.Native == nil {
		t.Errorf("definitions %v, interpreter %v, program %v", kinds, pipeline.Interpreter, loaded.Native)
	}

	registry, err := capabilities.New(loaded.Plugin)
	if err != nil {
		t.Fatal(err)
	}
	if ref, found := registry.ByPipelineRef("go-unit-tests"); !found || ref != (capability.Ref{Plugin: "gotest", Name: "unit-tests"}) {
		t.Errorf("ref = %v, %v", ref, found)
	}
}

// The directory on disk loads as the one in memory: what publish-plugin packs is what is tested.
func TestTheExampleDirectoryLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), exampleProgram, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
}

func TestAPluginReachesNothingBeyondItsRuns(t *testing.T) {
	const (
		pipelineFile = "tekton/pipeline-go-unit-tests.yaml"
		taskFile     = "tekton/task-go-test.yaml"
	)
	for name, change := range map[string]func(fstest.MapFS){
		"a service account in the manifest": func(files fstest.MapFS) {
			edit(t, files, manifest.FileName, "    group: unit\n", "    group: unit\n    serviceAccountName: admin\n")
		},
		"a service account in a definition": func(files fstest.MapFS) {
			edit(t, files, pipelineFile, "  tasks:\n", "  taskRunTemplate:\n    serviceAccountName: admin\n  tasks:\n")
		},
		"another kind": func(files fstest.MapFS) {
			edit(t, files, taskFile, "kind: Task", "kind: ClusterRole")
		},
		"another API": func(files fstest.MapFS) {
			edit(t, files, taskFile, "apiVersion: tekton.dev/v1", "apiVersion: rbac.authorization.k8s.io/v1")
		},
		"a second document": func(files fstest.MapFS) {
			edit(t, files, taskFile, "kind: Task", "kind: Task\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n---\nkind: Task")
		},
		"a node path": func(files fstest.MapFS) {
			edit(t, files, taskFile, "  steps:\n", "  volumes:\n    - name: root\n      hostPath: { path: / }\n  steps:\n")
		},
		"a privileged step": func(files fstest.MapFS) {
			edit(t, files, taskFile, "      image: golang:1.26\n", "      image: golang:1.26\n      securityContext: { privileged: true }\n")
		},
		"a remote Task": func(files fstest.MapFS) {
			edit(t, files, pipelineFile, "        name: go-test\n", "        resolver: git\n")
		},
		"an unshipped Task": func(files fstest.MapFS) {
			edit(t, files, pipelineFile, "name: go-test", "name: go-vet")
		},
		"another Pipeline": func(files fstest.MapFS) {
			edit(t, files, pipelineFile, "name: go-unit-tests", "name: go-tests")
		},
		"a path outside": func(files fstest.MapFS) {
			edit(t, files, manifest.FileName, "binary: bin/plugin", "binary: ../plugin")
		},
		"a missing file": func(files fstest.MapFS) { delete(files, taskFile) },
		"a link": func(files fstest.MapFS) {
			files["tekton/link.yaml"] = &fstest.MapFile{Data: []byte("/etc/passwd"), Mode: fs.ModeSymlink}
		},
		"a missing program": func(files fstest.MapFS) { delete(files, "bin/plugin") },
	} {
		files := plugin(t)
		change(files)
		if _, err := Load(files); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAPluginIsBounded(t *testing.T) {
	if _, err := load(plugin(t), int64(len(exampleProgram))); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("err = %v", err)
	}
}

// publishable is the example's directory as its author publishes it: the program built for a
// platform.
func publishable(t *testing.T) fstest.MapFS {
	t.Helper()
	files := plugin(t)
	files["bin/plugin-linux-"+runtime.GOARCH] = files["bin/plugin"]
	delete(files, "bin/plugin")
	return files
}
