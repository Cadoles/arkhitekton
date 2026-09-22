package pipelines

import (
	"errors"
	"strings"
	"testing"
)

// The PHP library is extracted from what actually runs. This test says what it must contain: if a
// manifest disappears from the embedding — a file renamed, a directory moved — nothing else would
// report it before a pipeline could not be found in the cluster.
func TestThePhpLibraryCarriesItsPipelinesAndTasks(t *testing.T) {
	resources, err := For("php")
	if err != nil {
		t.Fatalf("php library: %v", err)
	}

	byName := map[string]Resource{}
	for _, resource := range resources {
		byName[resource.Name] = resource
	}

	for _, name := range []string{"php-unit-tests", "php-static-tests", "php-integration-test"} {
		resource, found := byName[name]
		if !found {
			t.Errorf("pipeline %q missing from the library", name)
			continue
		}
		if resource.Kind != "Pipeline" {
			t.Errorf("%q is a %q", name, resource.Kind)
		}
	}

	for _, name := range []string{"unit-test", "phpstan", "cs-fixer", "integration-test"} {
		resource, found := byName[name]
		if !found {
			t.Errorf("task %q missing from the library", name)
			continue
		}
		if resource.Kind != "Task" {
			t.Errorf("%q is a %q", name, resource.Kind)
		}
	}
}

// The manifest is installed **as it is**. Rewriting it on the fly would produce a definition
// nobody has reviewed, and which would diverge from what the repository shows.
func TestTheManifestIsReturnedAsItIs(t *testing.T) {
	resources, _ := For("php")
	if len(resources) == 0 {
		t.Fatal("empty library")
	}

	for _, resource := range resources {
		if !strings.Contains(string(resource.Manifest), "apiVersion: tekton.dev/") {
			t.Errorf("%q: the manifest does not look like a Tekton object", resource.Name)
		}
	}
}

// An empty stack is perfectly valid: it is the project bringing its own pipelines, and refusing it
// would forbid it to exist.
func TestAnEmptyStackIsNotAnError(t *testing.T) {
	resources, err := For("")
	if err != nil {
		t.Fatalf("pile vide: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("%d resource(s) for an empty stack", len(resources))
	}
}

// An unknown stack is named at declaration, and the message says what to do: fork. That is what
// "installable and forkable" means.
func TestAnUnknownStackIsNamedWithThoseThatExist(t *testing.T) {
	_, err := For("node")

	var unknown ErrUnknownStack
	if !errors.As(err, &unknown) {
		t.Fatalf("error %v, expected ErrUnknownStack", err)
	}
	if !strings.Contains(err.Error(), "php") {
		t.Errorf("message %q — it must name the embedded stacks", err)
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("message %q — it must say that a project can write its own", err)
	}
}

// The embedded stacks are discovered from the file system: adding one is only a directory to
// deposit, not a list to keep up to date.
func TestTheStacksAreDiscoveredFromTheEmbedding(t *testing.T) {
	stacks := Stacks()
	if len(stacks) == 0 {
		t.Fatal("no embedded stack")
	}
	if stacks[0] != "php" {
		t.Errorf("stacks %v — php must be there", stacks)
	}
}
