package installed

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

/*
 * The example plugin, sdk/examples/gotest, read as an administrator would install it.
 */

const example = "../../sdk/examples/gotest"

// module is the example's interpreter, built for the occasion.
var module []byte

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "arkhitekton-installed-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := filepath.Join(dir, "gotest.wasm")
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./examples/gotest")
	build.Dir = filepath.Join("..", "..", "sdk")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the example: %v\n%s", err, output)
		os.Exit(1)
	}
	module, _ = os.ReadFile(out)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// plugin is the example's directory in memory, its module built.
func plugin(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{"gotest.wasm": {Data: module}}
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
	if kinds["go-unit-tests"] != "Pipeline" || kinds["go-test"] != "Task" || len(pipeline.Module) == 0 {
		t.Errorf("definitions %v, module of %d bytes", kinds, len(pipeline.Module))
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
	if err := os.WriteFile(filepath.Join(dir, "gotest.wasm"), module, 0o644); err != nil {
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
			edit(t, files, manifest.FileName, "module: gotest.wasm", "module: ../gotest.wasm")
		},
		"a missing file": func(files fstest.MapFS) { delete(files, taskFile) },
		"a link": func(files fstest.MapFS) {
			files["tekton/link.yaml"] = &fstest.MapFile{Data: []byte("/etc/passwd"), Mode: fs.ModeSymlink}
		},
		"a broken module": func(files fstest.MapFS) {
			files["gotest.wasm"] = &fstest.MapFile{Data: []byte("not wasm")}
		},
	} {
		files := plugin(t)
		change(files)
		if _, err := Load(files); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAPluginIsBounded(t *testing.T) {
	if _, err := load(plugin(t), int64(len(module))); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("err = %v", err)
	}
}
