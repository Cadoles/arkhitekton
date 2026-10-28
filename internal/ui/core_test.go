package ui

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

var update = flag.Bool("update", false, "rewrites the golden interface")

var known = Options{KnownPermission: func(permission string) bool { return auth.Known(auth.Permission(permission)) }}

// The core's resources compose on their own, against the permissions roles actually grant.
func TestTheCoresInterfaceComposes(t *testing.T) {
	core, err := CoreResources()
	if err != nil {
		t.Fatal(err)
	}
	if _, refused := Compose(core, nil, known); len(refused) > 0 {
		t.Fatal(refused)
	}
}

// The core's interface, as a viewer with every permission gets it, is what plugins compose with: a
// change to it shows in review. `go test ./internal/ui -update` rewrites it.
func TestTheCoresInterfaceIsTheGoldenOne(t *testing.T) {
	composed, refused := Compose(Core(), nil, known)
	if len(refused) > 0 {
		t.Fatal(refused)
	}
	got, err := json.MarshalIndent(composed.For(Viewer{
		Active: func(string) bool { return true }, Can: func(string) bool { return true },
	}), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "testdata/core.json"
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expected) {
		t.Errorf("the core's interface changed: run go test ./internal/ui -update and read the diff")
	}
}
