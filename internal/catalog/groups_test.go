package catalog

import "testing"

// The pipeline to re-run for a group is the first one declared under it.
func TestAGroupNamesItsFirstPipeline(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{
		{Kind: "php/unit-tests", Uploads: map[string]Upload{"results": {Group: "unit"}}},
		{Kind: "go/unit-tests", Uploads: map[string]Upload{"results": {Group: "unit"}}},
		{Kind: "php/static-tests", Uploads: map[string]Upload{"results": {Group: "static"}}},
	}}
	for group, expected := range map[string]string{"unit": "php/unit-tests", "static": "php/static-tests", "e2e": "", "": ""} {
		if got := pipelines.KindOfGroup(group); got != expected {
			t.Errorf("%q: %q, expected %q", group, got, expected)
		}
	}
	if (*Catalog)(nil).KindOfGroup("unit") != "" {
		t.Error("no catalogue names a pipeline")
	}
}
