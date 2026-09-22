package pipelines

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

/*
 * Installing the reference resources.
 *
 * The same ownership rule as the deploy-key secrets, for the same reason: the dashboard installs
 * only what it **names**, marks what it owns, and **never adopts** what already exists.
 *
 * Here the rule has a happy consequence rather than a constraint. On a cluster where `mse-tekton`
 * has already installed `php-unit-tests`, the dashboard sees that the name is taken and
 * withdraws: it does not overwrite a definition someone else maintains, and does not conflict
 * with the kustomization that reapplies it.
 *
 * This mechanism therefore serves **autonomy**: a fresh instance, with no pre-existing pipeline
 * repository, receives what it needs to build and test. An instance that has one keeps its own.
 */

// ManagedBy marks the resources the dashboard owns.
const ManagedBy = "arkhitekton"

// Outcome says what an installation produced, resource by resource.
type Outcome struct {
	// Installed names what was created.
	Installed []string
	// Skipped names what already existed and does not belong to the dashboard. Not an error: it is
	// the refusal to adopt, and operations must be able to read it without taking it for an outage.
	Skipped []string
	// Updated names what was replaced — resources the dashboard had installed, whose definition
	// changed with its version.
	Updated []string
}

// Installer puts a library's resources into the cluster.
type Installer struct {
	dynamic   dynamic.Interface
	mapper    meta.RESTMapper
	namespace string
}

func NewInstaller(client dynamic.Interface, mapper meta.RESTMapper, namespace string) *Installer {
	return &Installer{dynamic: client, mapper: mapper, namespace: namespace}
}

// Install puts a stack's library in place.
//
// Idempotent: called again at start-up and at every pass of the loop, it rewrites only what
// belongs to it and leaves the rest untouched.
func (i *Installer) Install(ctx context.Context, stack string) (Outcome, error) {
	resources, err := For(stack)
	if err != nil {
		return Outcome{}, err
	}

	var outcome Outcome
	var problems []error
	for _, resource := range resources {
		switch state, err := i.apply(ctx, resource); {
		case err != nil:
			// A failed resource does not stop the others: a half-installed library is better than none, and
			// what is missing is read in the log rather than in a pipeline that cannot be found.
			problems = append(problems, err)
		case state == "installed":
			outcome.Installed = append(outcome.Installed, resource.Name)
		case state == "updated":
			outcome.Updated = append(outcome.Updated, resource.Name)
		default:
			outcome.Skipped = append(outcome.Skipped, resource.Name)
		}
	}
	return outcome, errors.Join(problems...)
}

// apply puts a resource in place and says what became of it.
func (i *Installer) apply(ctx context.Context, resource Resource) (string, error) {
	object := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(resource.Manifest, object); err != nil {
		return "", fmt.Errorf("manifeste %q illisible: %w", resource.Name, err)
	}

	object.SetNamespace(i.namespace)
	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app.kubernetes.io/managed-by"] = ManagedBy
	object.SetLabels(labels)

	client, err := i.resourceFor(object)
	if err != nil {
		return "", err
	}

	_, err = client.Create(ctx, object, metav1.CreateOptions{})
	if err == nil {
		return "installed", nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("installing %s %q: %w", resource.Kind, resource.Name, err)
	}

	// The name is taken. Unlike secrets, here the object can be **read** to know whom it belongs to:
	// a Pipeline is not a secret, and the RBAC already grants reading it in order to validate the
	// catalogue at start-up.
	existing, err := client.Get(ctx, object.GetName(), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading %s %q: %w", resource.Kind, resource.Name, err)
	}
	if existing.GetLabels()["app.kubernetes.io/managed-by"] != ManagedBy {
		// The refusal to adopt. `mse-tekton` may maintain this definition, and overwriting it would make
		// it diverge from its repository at every start-up.
		return "skipped", nil
	}

	// Ours: it follows the binary's version, and replacing it is what makes a dashboard update update
	// its pipelines.
	object.SetResourceVersion(existing.GetResourceVersion())
	if _, err := client.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("updating %s %q: %w", resource.Kind, resource.Name, err)
	}
	return "updated", nil
}

// resourceFor resolves an object's kind to its endpoint.
//
// By discovery and not by a hard-coded table: Tekton served `v1beta1` then `v1`, and hard-coding
// one would fail the installation on half the installations with a message that does not name the
// cause — the same reason as for the Flux client.
func (i *Installer) resourceFor(object *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := object.GroupVersionKind()
	mapping, err := i.mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind})
	if err != nil {
		return nil, fmt.Errorf("the CRD %s.%s is missing from the cluster: %w", gvk.Kind, gvk.Group, err)
	}
	return i.dynamic.Resource(mapping.Resource).Namespace(i.namespace), nil
}
