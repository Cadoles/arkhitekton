package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/declarative"
)

// The cases recorded for the built-in readings read as recorded: they are the examples a plugin
// author starts from, and must stay true.
func TestTheRecordedCasesReadAsExpected(t *testing.T) {
	readings := map[string]string{
		"php-static-tests":      "../../internal/plugins/php/readings/static-tests.yaml",
		"php-unit-tests":        "../../internal/plugins/php/readings/unit-tests.yaml",
		"mse-integration-tests": "../../internal/plugins/mse/readings/integration-tests.yaml",
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
	if checked < 5 {
		t.Fatalf("only %d cases checked", checked)
	}
}

// A reading that drifts from its recorded case is reported, and the check fails.
func TestADriftingReadingFailsTheCheck(t *testing.T) {
	reading, err := os.ReadFile("../../internal/plugins/php/readings/static-tests.yaml")
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
