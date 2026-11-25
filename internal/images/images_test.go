package images

import "testing"

func TestAReferenceReadsBack(t *testing.T) {
	for reference, expected := range map[string]Parsed{
		"reg.example.fr/team/portal:1.2.0-rc.1": {Registry: "reg.example.fr/team", Name: "portal", Tag: "1.2.0-rc.1"},
		"localhost:5000/portal":                 {Registry: "localhost:5000", Name: "portal"},
		"registry-tekton:5000/team/portal-dev:2.0.0@sha256:ab": {
			Registry: "registry-tekton:5000/team", Name: "portal-dev", Tag: "2.0.0", Digest: "sha256:ab"},
		"portal": {Name: "portal"},
	} {
		if got := Parse(reference); got != expected {
			t.Errorf("%s: %+v, expected %+v", reference, got, expected)
		}
	}
}
