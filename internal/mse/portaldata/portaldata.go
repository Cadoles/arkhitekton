// Package portaldata lit ce que les applications du portail rapportent
// d'elles-mêmes, par le CRD `PortalData`.
//
// Séparé de `internal/mse/flux` par le lot D (doc 12 §12.9), et la séparation n'est
// pas un rangement. Les deux paquets répondaient à deux questions différentes
// depuis un seul client : Flux dit **l'intention** — le tag qu'une Kustomization
// a retenu —, le portail dit le **constat** — telle version tourne, et ses
// migrations ont abouti. C'est leur écart que l'écran des environnements existe
// pour montrer (B8).
//
// La conséquence concrète de la fusion était une panne : `flux.Open` résolvait
// les trois CRD d'un coup et **échouait si l'un manquait**. Un cluster sans le
// CRD du portail — c'est-à-dire tout cluster qui n'est pas celui du MSE — ne
// pouvait donc pas avoir de déployeur, alors que Flux y était parfaitement
// installé. La dépendance facultative faisait échouer la dépendance principale.
package portaldata

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
)

// group est le groupe d'API du CRD. Propre au portail MSE — c'est exactement ce
// qu'un plugin porterait le jour venu (doc 12 §12.5) — mais ce que le CRD
// rapporte, « telle version tourne et elle est exploitable », ne l'est pas.
const group = "mse.cnous.fr"

// Client lit le CRD `PortalData`.
//
// Comme le client Flux, il emploie le client dynamique et **découvre la version
// servie** au lieu de la coder : un CRD versionné par son auteur change de
// version sans nous prévenir, et coder `v1alpha1` ferait échouer la lecture avec
// un « the server could not find the requested resource » qui ne désigne pas la
// cause.
type Client struct {
	dynamic  dynamic.Interface
	resource schema.GroupVersionResource
}

// Open construit le client et résout la version servie.
//
// Rend une erreur nommée quand le CRD est absent : l'appelant doit pouvoir
// distinguer « ce cluster n'atteste rien », qui est une configuration valide,
// d'une panne d'accès.
func Open(config *rest.Config) (*Client, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("client dynamique: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("client de découverte: %w", err)
	}

	mapper, err := resolver(discoveryClient)
	if err != nil {
		return nil, err
	}

	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: group, Kind: "PortalData"})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAbsent, err)
	}

	return &Client{dynamic: dynamicClient, resource: mapping.Resource}, nil
}

// ErrAbsent signale que le cluster ne sert pas le CRD.
//
// Ce n'est pas une panne : un cluster sans portail est un cluster où rien
// n'atteste, et le dashboard y retombe sur ce que le déploiement déclare. Le
// dire ainsi évite de faire chercher un incident là où il n'y a qu'une absence.
var ErrAbsent = fmt.Errorf("le CRD PortalData.%s est absent du cluster", group)

func resolver(discoveryClient discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		// La découverte remonte une erreur partielle quand un groupe d'API est
		// injoignable — un agrégateur en panne, par exemple. Ce qui a été obtenu
		// reste exploitable, et c'est peut-être justement notre groupe.
		if len(resources) == 0 {
			return nil, fmt.Errorf("découverte des API du cluster: %w", err)
		}
	}
	return restmapper.NewDiscoveryRESTMapper(resources), nil
}

// Version rend la version retenue, pour le journal de démarrage.
//
// L'inscrire au journal n'est pas cosmétique : c'est la seule trace qui permette,
// devant un comportement inattendu, de savoir contre quelle version d'API le
// dashboard a parlé.
func (c *Client) Version() string {
	return c.resource.GroupVersion().String()
}

var _ provider.Attester = (*Client)(nil)

// Attestations lit ce que les applications d'un namespace rapportent d'elles-mêmes.
//
// Seule source issue de l'application **en exécution**, et la seule à dire si les
// migrations ont abouti : une image déployée dont les migrations ont échoué n'est
// pas une version livrée, et rien d'autre ne le signale (B8).
func (c *Client) Attestations(
	ctx context.Context, namespace string,
) ([]provider.Attestation, error) {
	list, err := c.dynamic.Resource(c.resource).Namespace(namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("liste des PortalData dans %q: %w", namespace, err)
	}

	states := make([]provider.Attestation, 0, len(list.Items))
	for _, item := range list.Items {
		object := item.Object

		state := provider.Attestation{Namespace: namespace, Name: item.GetName()}
		state.State, _, _ = unstructured.NestedString(object, "status", "phase")
		// `dataVersion` est la version de l'**image** ayant réalisé le dernier
		// bootstrap, non celle de la release. Une release finale réutilisant
		// l'image de sa RC, le rapprochement se fait sur `releases.image_version`
		// et jamais sur le tag (B17, B8).
		state.Version, _, _ = unstructured.NestedString(object, "status", "dataVersion")
		// `Ready` est le verdict, et il est rendu ici parce que le vocabulaire
		// qui le porte appartient au CRD. `status.schemaVersion` existe aussi
		// mais n'est pas lu : c'est un nom de classe Doctrine, et le confondre
		// avec une version de release est l'erreur que la relecture du CRD a
		// corrigée.
		state.Ready = state.State == "Ready"

		states = append(states, state)
	}
	return states, nil
}
