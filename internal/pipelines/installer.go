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
 * La pose des ressources de référence.
 *
 * La même règle de propriété que les secrets du lot G, pour la même raison : le
 * dashboard ne pose que ce qu'il **nomme**, marque ce qu'il possède, et
 * n'**adopte jamais** ce qui existe déjà.
 *
 * Ici, la règle a une conséquence heureuse plutôt qu'une contrainte. Sur un
 * cluster où `mse-tekton` a déjà posé `php-unit-tests`, le dashboard constate que
 * le nom est pris et se retire : il n'écrase pas une définition que quelqu'un
 * d'autre maintient, et n'entre pas en conflit avec la kustomization qui la
 * réapplique.
 *
 * Ce mécanisme sert donc l'**autonomie** : une instance neuve, qui n'a aucun
 * dépôt de pipelines préexistant, reçoit de quoi construire et tester. Une
 * instance qui en a un garde le sien.
 */

// ManagedBy marque les ressources que le dashboard possède.
const ManagedBy = "arkhitekton"

// Outcome dit ce qu'une pose a produit, ressource par ressource.
type Outcome struct {
	// Installed nomme ce qui a été créé.
	Installed []string
	// Skipped nomme ce qui existait déjà et n'appartient pas au dashboard. Pas
	// une erreur : c'est le refus d'adopter, et l'exploitation doit pouvoir le
	// lire sans le prendre pour une panne.
	Skipped []string
	// Updated nomme ce qui a été remplacé — des ressources que le dashboard
	// avait posées, dont la définition a changé avec sa version.
	Updated []string
}

// Installer pose les ressources d'une bibliothèque dans le cluster.
type Installer struct {
	dynamic   dynamic.Interface
	mapper    meta.RESTMapper
	namespace string
}

func NewInstaller(client dynamic.Interface, mapper meta.RESTMapper, namespace string) *Installer {
	return &Installer{dynamic: client, mapper: mapper, namespace: namespace}
}

// Install pose la bibliothèque d'une pile.
//
// Idempotent : rappelé au démarrage et à chaque tour de la boucle, il ne
// réécrit que ce qui lui appartient et laisse le reste intact.
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
			// Une ressource en échec n'empêche pas les autres : une bibliothèque
			// à moitié posée vaut mieux qu'aucune, et le manque se lit dans le
			// journal plutôt que dans un pipeline introuvable.
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

// apply pose une ressource et dit ce qu'elle est devenue.
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
		return "", fmt.Errorf("pose de %s %q: %w", resource.Kind, resource.Name, err)
	}

	// Le nom est pris. Contrairement aux secrets, on peut ici **lire** pour
	// savoir à qui appartient l'objet : un Pipeline n'est pas un secret, et la
	// RBAC accorde déjà sa lecture pour valider le catalogue au démarrage.
	existing, err := client.Get(ctx, object.GetName(), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("lecture de %s %q: %w", resource.Kind, resource.Name, err)
	}
	if existing.GetLabels()["app.kubernetes.io/managed-by"] != ManagedBy {
		// Le refus d'adopter. `mse-tekton` maintient peut-être cette définition,
		// et l'écraser la ferait diverger de son dépôt à chaque démarrage.
		return "skipped", nil
	}

	// La nôtre : elle suit la version du binaire, et la remplacer est ce qui
	// fait qu'une mise à jour du dashboard met à jour ses pipelines.
	object.SetResourceVersion(existing.GetResourceVersion())
	if _, err := client.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("mise à jour de %s %q: %w", resource.Kind, resource.Name, err)
	}
	return "updated", nil
}

// resourceFor résout le type d'un objet vers son endpoint.
//
// Par découverte et non par une table codée : Tekton a servi `v1beta1` puis
// `v1`, et coder l'une ferait échouer la pose sur la moitié des installations
// avec un message qui ne désigne pas la cause — la même raison que pour le
// client Flux.
func (i *Installer) resourceFor(object *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := object.GroupVersionKind()
	mapping, err := i.mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind})
	if err != nil {
		return nil, fmt.Errorf("le CRD %s.%s est absent du cluster: %w", gvk.Kind, gvk.Group, err)
	}
	return i.dynamic.Resource(mapping.Resource).Namespace(i.namespace), nil
}
