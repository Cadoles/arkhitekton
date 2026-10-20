package capability

import (
	"testing"
	"testing/fstest"
)

func TestARefReadsBackAsWritten(t *testing.T) {
	ref, err := ParseRef("php/unit-tests")
	if err != nil {
		t.Fatal(err)
	}
	if ref.String() != "php/unit-tests" || ref.Slug() != "php-unit-tests" || ref.LabelValue() != "php.unit-tests" {
		t.Errorf("ref = %+v", ref)
	}
}

func TestARefNeedsBothSegments(t *testing.T) {
	for _, raw := range []string{"unit-tests", "php/", "/unit-tests", "PHP/unit", "php/unit_tests", "a/b/c"} {
		if _, err := ParseRef(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestAManifestIsIdentifiedByItsMetadata(t *testing.T) {
	fsys := fstest.MapFS{
		"manifests/task.yaml": {Data: []byte("apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: phpstan\nspec:\n  params:\n    - name: other\n")},
		"manifests/notes.md":  {Data: []byte("ignored")},
	}
	manifests, err := LoadManifests(fsys, "manifests")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Kind != "Task" || manifests[0].Name != "phpstan" {
		t.Errorf("manifests = %+v", manifests)
	}
}
