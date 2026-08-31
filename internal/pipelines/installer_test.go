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

// testMapper résout les deux types de la bibliothèque, sans interroger de
// cluster : la découverte est éprouvée ailleurs, et la simuler ici n'apprendrait
// rien sur la règle de propriété.
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
	return client, NewInstaller(client, testMapper{}, namespace)
}

func existing(kind, name string, labels map[string]string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("tekton.dev/v1")
	object.SetKind(kind)
	object.SetName(name)
	object.SetNamespace(namespace)
	object.SetLabels(labels)
	// Une marque reconnaissable : c'est elle qui dira si la définition a été
	// écrasée.
	_ = unstructured.SetNestedField(object.Object, "celle de mse-tekton", "spec", "description")
	return object
}

// La pose sur un cluster neuf : c'est le cas que ce mécanisme existe pour
// servir — une instance qui n'a aucun dépôt de pipelines préexistant.
func TestSurUnClusterNeufLaBibliothequeEstPosee(t *testing.T) {
	client, installer := installerFor(t)

	outcome, err := installer.Install(context.Background(), "php")
	if err != nil {
		t.Fatalf("pose: %v", err)
	}
	if len(outcome.Installed) == 0 {
		t.Fatal("rien n'a été posé")
	}
	if len(outcome.Skipped) != 0 {
		t.Errorf("ressources écartées sur un cluster vide: %v", outcome.Skipped)
	}

	posed, err := client.Resource(pipelineGVR).Namespace(namespace).
		Get(context.Background(), "php-unit-tests", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if posed.GetLabels()["app.kubernetes.io/managed-by"] != ManagedBy {
		t.Errorf("étiquettes %v — la propriété doit être lisible", posed.GetLabels())
	}
}

// Le refus d'adopter, et c'est la règle centrale.
//
// Sur un cluster où `mse-tekton` a déjà posé ses pipelines, le dashboard se
// retire : écraser une définition que quelqu'un d'autre maintient la ferait
// diverger de son dépôt à chaque démarrage, et les deux se réécriraient
// indéfiniment.
func TestUneDefinitionDunTiersNestJamaisEcrasee(t *testing.T) {
	client, installer := installerFor(t,
		existing("Pipeline", "php-unit-tests", map[string]string{"app": "mse-tekton"}))

	outcome, err := installer.Install(context.Background(), "php")
	if err != nil {
		t.Fatalf("pose: %v", err)
	}

	var skipped bool
	for _, name := range outcome.Skipped {
		if name == "php-unit-tests" {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("écartées %v — la définition d'un tiers doit l'être", outcome.Skipped)
	}

	// Et surtout : la définition n'a pas bougé.
	after, _ := client.Resource(pipelineGVR).Namespace(namespace).
		Get(context.Background(), "php-unit-tests", metav1.GetOptions{})
	description, _, _ := unstructured.NestedString(after.Object, "spec", "description")
	if description != "celle de mse-tekton" {
		t.Error("la définition d'un tiers a été écrasée")
	}
}

// La nôtre, en revanche, est remplacée : c'est ce qui fait qu'une mise à jour du
// dashboard met à jour ses pipelines, comme le Job de migration met à jour son
// schéma.
func TestUneDefinitionQuiNousAppartientEstMiseAJour(t *testing.T) {
	client, installer := installerFor(t,
		existing("Pipeline", "php-unit-tests",
			map[string]string{"app.kubernetes.io/managed-by": ManagedBy}))

	outcome, err := installer.Install(context.Background(), "php")
	if err != nil {
		t.Fatalf("pose: %v", err)
	}

	var updated bool
	for _, name := range outcome.Updated {
		if name == "php-unit-tests" {
			updated = true
		}
	}
	if !updated {
		t.Errorf("mises à jour %v — la nôtre devait l'être", outcome.Updated)
	}

	after, _ := client.Resource(pipelineGVR).Namespace(namespace).
		Get(context.Background(), "php-unit-tests", metav1.GetOptions{})
	description, _, _ := unstructured.NestedString(after.Object, "spec", "description")
	if description == "celle de mse-tekton" {
		t.Error("notre propre définition n'a pas été remplacée")
	}
}

// Rappelée deux fois, la pose ne produit rien de neuf : elle tourne au démarrage
// et à chaque tour de boucle.
func TestLaPoseEstIdempotente(t *testing.T) {
	_, installer := installerFor(t)
	ctx := context.Background()

	first, _ := installer.Install(ctx, "php")
	second, err := installer.Install(ctx, "php")
	if err != nil {
		t.Fatalf("seconde pose: %v", err)
	}

	if len(second.Installed) != 0 {
		t.Errorf("posées au second tour: %v", second.Installed)
	}
	if len(second.Updated) != len(first.Installed) {
		t.Errorf("%d mises à jour pour %d ressources posées",
			len(second.Updated), len(first.Installed))
	}
}

// Une pile sans bibliothèque est refusée à la pose comme à la déclaration.
func TestUnePileInconnueNePoseRien(t *testing.T) {
	client, installer := installerFor(t)

	if _, err := installer.Install(context.Background(), "node"); err == nil {
		t.Fatal("une pile inconnue doit être refusée")
	}

	list, _ := client.Resource(pipelineGVR).Namespace(namespace).
		List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("%d ressource(s) posée(s) pour une pile inconnue", len(list.Items))
	}
}
