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

const namespace = "mse-tekton-pipelines"

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

func installerFor(t *testing.T, objects ...runtime.Object) (*dynamicfake.FakeDynamicClient, *Installer) {
	t.Helper()

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			pipelineGVR: "PipelineList",
			taskGVR:     "TaskList",
		}, objects...)
	return client, NewInstaller(testLibrary(), client, testMapper{}, namespace)
}

func existing(kind, name string, labels map[string]string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("tekton.dev/v1")
	object.SetKind(kind)
	object.SetName(name)
	object.SetNamespace(namespace)
	object.SetLabels(labels)
	// A recognisable mark: it is what will say whether the definition was overwritten.
	_ = unstructured.SetNestedField(object.Object, "mse-tekton's", "spec", "description")
	return object
}

// Installing on a fresh cluster: it is the case this mechanism exists to serve — an instance with
// no pre-existing pipeline repository.
func TestOnAFreshClusterTheLibraryIsInstalled(t *testing.T) {
	client, installer := installerFor(t)

	outcome, err := installer.Install(context.Background(), "php")
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
// On a cluster where `mse-tekton` has already installed its pipelines, the dashboard withdraws:
// overwriting a definition someone else maintains would make it diverge from its repository at
// every start-up, and the two would rewrite each other indefinitely.
func TestAThirdPartysDefinitionIsNeverOverwritten(t *testing.T) {
	client, installer := installerFor(t,
		existing("Pipeline", "php-unit-tests", map[string]string{"app": "mse-tekton"}))

	outcome, err := installer.Install(context.Background(), "php")
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
	if description != "mse-tekton's" {
		t.Error("a third party's definition was overwritten")
	}
}

// Ours, on the other hand, is replaced: that is what makes a dashboard update update its
// pipelines, as the migration Job updates its schema.
func TestADefinitionOfOursIsUpdated(t *testing.T) {
	client, installer := installerFor(t,
		existing("Pipeline", "php-unit-tests",
			map[string]string{"app.kubernetes.io/managed-by": ManagedBy}))

	outcome, err := installer.Install(context.Background(), "php")
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
	if description == "mse-tekton's" {
		t.Error("our own definition was not replaced")
	}
}

// Called twice, the installation produces nothing new: it runs at start-up and at every pass of
// the loop.
func TestTheInstallationIsIdempotent(t *testing.T) {
	_, installer := installerFor(t)
	ctx := context.Background()

	first, _ := installer.Install(ctx, "php")
	second, err := installer.Install(ctx, "php")
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

// A stack with no library is refused at installation as at declaration.
func TestAnUnknownStackInstallsNothing(t *testing.T) {
	client, installer := installerFor(t)

	if _, err := installer.Install(context.Background(), "node"); err == nil {
		t.Fatal("an unknown stack must be refused")
	}

	list, _ := client.Resource(pipelineGVR).Namespace(namespace).
		List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("%d resource(s) installed for an unknown stack", len(list.Items))
	}
}
