package wasm_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// A pipeline bringing a module reads its runs with it, and cannot also read them another way.
func TestAModuleBecomesThePipelinesInterpreter(t *testing.T) {
	out := filepath.Join(t.TempDir(), "echo.wasm")
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./testdata/guests/echo")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	module, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	pipeline := capability.Pipeline{Name: "unit", Module: module, Spec: capability.PipelineSpec{
		PipelineRef: "go-unit", Produces: []capability.Product{capability.ProductResults},
	}}
	plugin := capability.Plugin{ID: "go", Version: "1", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{pipeline}}
	registry, err := capabilities.New(plugin)
	if err != nil {
		t.Fatal(err)
	}
	interpreter, err := registry.Interpreter(capability.Ref{Plugin: "go", Name: "unit"}, "1")
	if err != nil {
		t.Fatal(err)
	}
	sets, err := interpreter.Interpret(context.Background(), capability.RunState{Status: "succeeded"},
		[]parse.Record{{Tool: "gotest", Parser: parse.Raw, Payload: parse.Bytes("ok")}})
	if err != nil || len(sets) != 1 || sets[0].Cases[0].Name != "ok" {
		t.Errorf("sets %+v, err %v", sets, err)
	}

	plugin.Pipelines[0].Reading = []byte("apiVersion: arkhitekton.reading/v1\n")
	if _, err := capabilities.New(plugin); err == nil {
		t.Error("a pipeline reading its runs two ways accepted")
	}
}
