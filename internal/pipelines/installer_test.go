package pipelines

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const namespace = "mstore-pipelines"

var (
	pipelineGVR = schema.GroupVersionResource{
		Group: "tekton.dev", Version: "v1", Resource: "pipelines",
	}
	taskGVR = schema.GroupVersionResource{
		Group: "tekton.dev", Version: "v1", Resource: "tasks",
	}
)

// testMapper resolves the library's two kinds, without querying a cluster: discovery is exercised
// elsewhere, and simulating it here would teach nothing about the ownership rule.
type testMapper struct{ meta.RESTMapper }

func (testMapper) RESTMapping(gk schema.GroupKind, _ ...string) (*meta.RESTMapping, error) {
	resource := taskGVR
	if gk.Kind == "Pipeline" {
		resource = pipelineGVR
	}
	return &meta.RESTMapping{Resource: resource}, nil
}

// testResources are one Pipeline and one Task, like the ones plugins ship.
func testResources() []Resource {
	return []Resource{
		{Kind: "Pipeline", Name: "php-unit-tests", Manifest: []byte(
			"apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: php-unit-tests\nspec:\n  tasks: []\n")},
		{Kind: "Task", Name: "unit-test", Manifest: []byte(
			"apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: unit-test\nspec:\n  steps: []\n")},
	}
}

func installerFor(t *testing.T, objects ...runtime.Object) (*dynamicfake.FakeDynamicClient, *Installer) {
	t.Helper()

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			pipelineGVR: "PipelineList",
			taskGVR:     "TaskList",
		}, objects...)
	return client, NewInstaller(client, testMapper{}, namespace)
}

func existing(kind, name string, labels map[string]string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("tekton.dev/v1")
	object.SetKind(kind)
	object.SetName(name)
	object.SetNamespace(namespace)
	object.SetLabels(labels)
	// A recognisable mark: it is what will say whether the definition was overwritten.
	_ = unstructured.SetNestedField(object.Object, "mstore-pipelines's", "spec", "description")
	return object
}

// Installing on a fresh cluster: it is the case this mechanism exists to serve — an instance with
// no pre-existing pipeline repository.
func TestOnAFreshClusterTheLibraryIsInstalled(t *testing.T) {
	client, installer := installerFor(t)

	outcome, err := installer.InstallResources(context.Background(), testResources())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(outcome.Installed) == 0 {
		t.Fatal("nothing was installed")
	}
	if len(outcome.Skipped) != 0 {
		t.Errorf("resources set aside on an empty cluster: %v", outcome.Skipped)
	}

	posed, err := client.Resource(pipelineGVR).Namespace(namespace).
		Get(context.Background(), "php-unit-tests", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if posed.GetLabels()["app.kubernetes.io/managed-by"] != ManagedBy {
		t.Errorf("labels %v — ownership must be readable", posed.GetLabels())
	}
}

// The refusal to adopt, and it is the central rule.
//
// On a cluster where `mstore-pipelines` has already installed its pipelines, the dashboard withdraws:
// overwriting a definition someone else maintains would make it diverge from its repository at
// every start-up, and the two would rewrite each other indefinitely.
func TestAThirdPartysDefinitionIsNeverOverwritten(t *testing.T) {
	client, installer := installerFor(t,
		existing("Pipeline", "php-unit-tests", map[string]string{"app": "mstore-pipelines"}))

	outcome, err := installer.InstallResources(context.Background(), testResources())
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	var skipped bool
	for _, name := range outcome.Skipped {
		if name == "php-unit-tests" {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("set aside %v — a third party's definition must be", outcome.Skipped)
	}

	// And above all: the definition has not moved.
	after, _ := client.Resource(pipelineGVR).Namespace(namespace).
		Get(context.Background(), "php-unit-tests", metav1.GetOptions{})
	description, _, _ := unstructured.NestedString(after.Object, "spec", "description")
	if description != "mstore-pipelines's" {
		t.Error("a third party's definition was overwritten")
	}
}

// Ours, on the other hand, is replaced: that is what makes a dashboard update update its
// pipelines, as the migration Job updates its schema.
func TestADefinitionOfOursIsUpdated(t *testing.T) {
	client, installer := installerFor(t,
		existing("Pipeline", "php-unit-tests",
			map[string]string{"app.kubernetes.io/managed-by": ManagedBy}))

	outcome, err := installer.InstallResources(context.Background(), testResources())
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	var updated bool
	for _, name := range outcome.Updated {
		if name == "php-unit-tests" {
			updated = true
		}
	}
	if !updated {
		t.Errorf("updates %v — ours had to be", outcome.Updated)
	}

	after, _ := client.Resource(pipelineGVR).Namespace(namespace).
		Get(context.Background(), "php-unit-tests", metav1.GetOptions{})
	description, _, _ := unstructured.NestedString(after.Object, "spec", "description")
	if description == "mstore-pipelines's" {
		t.Error("our own definition was not replaced")
	}
}

// Called twice, the installation produces nothing new: it runs at start-up and at every pass of
// the loop.
func TestTheInstallationIsIdempotent(t *testing.T) {
	_, installer := installerFor(t)
	ctx := context.Background()

	first, _ := installer.InstallResources(ctx, testResources())
	second, err := installer.InstallResources(ctx, testResources())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}

	if len(second.Installed) != 0 {
		t.Errorf("installed on the second pass: %v", second.Installed)
	}
	if len(second.Updated) != len(first.Installed) {
		t.Errorf("%d updates for %d resources installed",
			len(second.Updated), len(first.Installed))
	}
}

// An uninstalled plugin's definitions are withdrawn; those of plugins still carried, and those
// someone else maintains, stay.
func TestAnUninstalledPluginsDefinitionsAreWithdrawn(t *testing.T) {
	ours := map[string]string{"app.kubernetes.io/managed-by": ManagedBy}
	labelled := func(plugin string) map[string]string {
		return map[string]string{"app.kubernetes.io/managed-by": ManagedBy, PluginLabel: plugin}
	}
	client, installer := installerFor(t,
		existing("Pipeline", "go-unit-tests", labelled("gotest")),
		existing("Task", "go-test", labelled("gotest")),
		existing("Pipeline", "php-unit-tests", labelled("php")),
		existing("Task", "unit-test", ours),
		existing("Task", "go-lint", map[string]string{PluginLabel: "gotest"}),
	)

	withdrawn, err := installer.Prune(context.Background(), func(plugin string) bool { return plugin == "php" })
	if err != nil {
		t.Fatal(err)
	}
	if len(withdrawn) != 2 {
		t.Errorf("withdrawn = %v", withdrawn)
	}
	for _, kept := range []struct {
		resource schema.GroupVersionResource
		name     string
	}{{pipelineGVR, "php-unit-tests"}, {taskGVR, "unit-test"}, {taskGVR, "go-lint"}} {
		if _, err := client.Resource(kept.resource).Namespace(namespace).Get(context.Background(), kept.name, metav1.GetOptions{}); err != nil {
			t.Errorf("%s withdrawn: %v", kept.name, err)
		}
	}
	if _, err := client.Resource(taskGVR).Namespace(namespace).Get(context.Background(), "go-test", metav1.GetOptions{}); err == nil {
		t.Error("go-test kept")
	}
}

// A definition carries the label of the plugin that ships it.
func TestADefinitionNamesItsPlugin(t *testing.T) {
	client, installer := installerFor(t)
	manifest := []byte("apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: go-test\nspec: {}\n")
	if _, err := installer.InstallResources(context.Background(), []Resource{{Name: "go-test", Kind: "Task", Manifest: manifest, Plugin: "gotest"}}); err != nil {
		t.Fatal(err)
	}
	posed, err := client.Resource(taskGVR).Namespace(namespace).Get(context.Background(), "go-test", metav1.GetOptions{})
	if err != nil || posed.GetLabels()[PluginLabel] != "gotest" {
		t.Errorf("labels %v, %v", posed.GetLabels(), err)
	}
}
