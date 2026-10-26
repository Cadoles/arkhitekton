package pipelines

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// testLibrary is a stack of one Pipeline and one Task, like the ones plugins ship.
func testLibrary() *Library {
	return NewLibrary(map[string][]capability.Manifest{"php": {
		{Kind: "Pipeline", Name: "php-unit-tests", YAML: []byte(
			"apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: php-unit-tests\nspec:\n  tasks: []\n")},
		{Kind: "Task", Name: "unit-test", YAML: []byte(
			"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: unit-test\nspec:\n  steps: []\n")},
	}})
}

// The manifest is installed **as it is**. Rewriting it on the fly would produce a definition
// nobody has reviewed, and which would diverge from what the repository shows.
func TestTheManifestIsReturnedAsItIs(t *testing.T) {
	resources, err := testLibrary().For("php")
	if err != nil || len(resources) != 2 {
		t.Fatalf("resources = %+v, err = %v", resources, err)
	}
	if resources[0].Name != "php-unit-tests" || !strings.Contains(string(resources[0].Manifest), "kind: Pipeline") {
		t.Errorf("first resource = %+v", resources[0])
	}
}

// An empty stack is perfectly valid: it is the project bringing its own pipelines, and refusing it
// would forbid it to exist.
func TestAnEmptyStackIsNotAnError(t *testing.T) {
	resources, err := testLibrary().For("")
	if err != nil {
		t.Fatalf("empty stack: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("%d resource(s) for an empty stack", len(resources))
	}
}

// An unknown stack is named at declaration, and the message says what to do: fork. That is what
// "installable and forkable" means.
func TestAnUnknownStackIsNamedWithThoseThatExist(t *testing.T) {
	_, err := testLibrary().For("node")

	var unknown ErrUnknownStack
	if !errors.As(err, &unknown) {
		t.Fatalf("error %v, expected ErrUnknownStack", err)
	}
	if !strings.Contains(err.Error(), "php") {
		t.Errorf("message %q: it must name the embedded stacks", err)
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("message %q: it must say that a project can write its own", err)
	}
}

// Without a library, every stack is unknown: a dashboard assembled with no plugin must refuse a
// stack rather than accept it and install nothing.
func TestWithoutLibraryEveryStackIsUnknown(t *testing.T) {
	var library *Library
	if _, err := library.For("php"); err == nil {
		t.Error("stack accepted without a library")
	}
}
