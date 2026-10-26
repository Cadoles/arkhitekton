package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
)

// The cases recorded for the built-in readings read as recorded: they are the examples a plugin
// author starts from, and must stay true.
func TestTheRecordedCasesReadAsExpected(t *testing.T) {
	readings := map[string]string{
		"php-static-tests": "../internal/plugins/php/readings/static-tests.yaml",
		"php-unit-tests":   "../internal/plugins/php/readings/unit-tests.yaml",
	}
	checked := 0
	for capability, path := range readings {
		reading, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cases, _ := filepath.Glob(filepath.Join("testdata", "readings", capability, "*"))
		for _, dir := range cases {
			got, expected, err := declarative.Check(reading, dir)
			if err != nil {
				t.Errorf("%s: %v", dir, err)
				continue
			}
			if expected == nil || !declarative.Same(got, expected) {
				t.Errorf("%s reads differently:\n%s", dir, got)
			}
			checked++
		}
	}
	if checked < 4 {
		t.Fatalf("only %d cases checked", checked)
	}
}

// A reading that drifts from its recorded case is reported, and the check fails.
func TestADriftingReadingFailsTheCheck(t *testing.T) {
	reading, err := os.ReadFile("../internal/plugins/php/readings/static-tests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(string(reading), `name: "{identifier | or: message | firstline}"`, `name: "{message | firstline}"`, 1)
	if drifted == string(reading) {
		t.Fatal("the drift was not applied")
	}
	dir := filepath.Join("testdata", "readings", "php-static-tests", "violations")
	got, expected, err := declarative.Check([]byte(drifted), dir)
	if err != nil {
		t.Fatal(err)
	}
	if declarative.Same(got, expected) {
		t.Error("a drifting reading passed its check")
	}

	path := filepath.Join(t.TempDir(), "drifted.yaml")
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCheckReading([]string{path, dir}); err == nil {
		t.Error("check-reading succeeded on a drifting reading")
	}
}

// builtExample is a copy of the example plugin with its program built for this machine, as its
// author builds it before publishing.
func builtExample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "rpc", "examples", "gotest"))); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "bin", "plugin-"+runtime.GOOS+"-"+runtime.GOARCH)
	build := exec.Command("go", "build", "-o", program, "./examples/gotest")
	build.Dir = filepath.Join("..", "rpc")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	return dir
}

// The example plugin's program reads its recorded cases as expected, launched as the dashboard
// launches it.
func TestTheExampleProgramReadsItsCases(t *testing.T) {
	dir := builtExample(t)
	cases, _ := filepath.Glob(filepath.Join("testdata", "readings", "gotest", "*"))
	if len(cases) < 4 {
		t.Fatalf("only %d cases", len(cases))
	}
	if err := runCheckReading(append([]string{"--plugin", dir}, cases...)); err != nil {
		t.Error(err)
	}
}
