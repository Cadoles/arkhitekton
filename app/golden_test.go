package app

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update rewrites the golden files from what the code produces now: `go test ./app -update`. A
// change of reading or of resolution then shows as a diff to review.
var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares a value with its golden file, as indented JSON.
func golden(t *testing.T, name string, value any) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from its golden file (run with -update and review the diff):\n%s", name, got)
	}
}
