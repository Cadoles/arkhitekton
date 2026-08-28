// Package flux lit ce que Flux publie : sélection d'image et rapprochement.
//
// Il ne lit **plus** l'état applicatif : celui-ci vit dans `internal/portaldata`
// depuis le lot D. Flux dit l'intention — le tag qu'une Kustomization a retenu —,
// le portail dit le constat, et les fondre dans un client faisait échouer le
// déployeur sur un cluster dépourvu du CRD du portail (doc 12 §12.9).
//
// Le dashboard n'écrit jamais rien ici. Il **demande** une promotion en poussant
// un tag sur le registre, et Flux décide ensuite ; tout ce paquet sert à constater
// ce que Flux a fait, pas à le piloter (B7).
package flux

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// Groupes lus par le dashboard.
const (
	imageGroup     = "image.toolkit.fluxcd.io"
	kustomizeGroup = "kustomize.toolkit.fluxcd.io"
)

// Client lit les objets Flux.
//
// Il emploie le client dynamique et **découvre la version servie** au lieu de la
// coder. La raison n'est pas théorique : `kustomize.toolkit.fluxcd.io/v1` est
// stable depuis Flux 2.0, tandis que `image.toolkit.fluxcd.io/v1` n'existe que
// depuis Flux 2.7 — une même installation sert donc `v1` d'un côté et `v1beta2`
// de l'autre. Coder l'une ou l'autre ferait échouer le dashboard sur la moitié des
// installations, avec un « the server could not find the requested resource » qui
// ne désigne pas la cause.
type Client struct {
	dynamic dynamic.Interface

	imagePolicies  schema.GroupVersionResource
	kustomizations schema.GroupVersionResource

	// Namespace des objets Flux. Distinct de celui des pipelines : Flux vit dans
	// `flux-system`.
	Namespace string
}

// Open construit le client et résout les versions servies.
func Open(config *rest.Config, namespace string) (*Client, error) {
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

	client := &Client{dynamic: dynamicClient, Namespace: namespace}

	for _, entry := range []struct {
		target *schema.GroupVersionResource
		group  string
		kind   string
	}{
		{&client.imagePolicies, imageGroup, "ImagePolicy"},
		{&client.kustomizations, kustomizeGroup, "Kustomization"},
	} {
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: entry.group, Kind: entry.kind})
		if err != nil {
			return nil, fmt.Errorf(
				"le CRD %s.%s est absent du cluster: %w", entry.kind, entry.group, err)
		}
		*entry.target = mapping.Resource
	}

	return client, nil
}

func resolver(discoveryClient discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		// La découverte remonte une erreur partielle quand un groupe d'API est
		// injoignable — un agrégateur en panne, par exemple. Ce qui a été obtenu
		// reste exploitable, et ce sont peut-être justement nos groupes.
		if len(resources) == 0 {
			return nil, fmt.Errorf("découverte des API du cluster: %w", err)
		}
	}
	return restmapper.NewDiscoveryRESTMapper(resources), nil
}

// Versions rend les versions retenues, pour le journal de démarrage.
//
// Les inscrire au journal n'est pas cosmétique : c'est la seule trace qui
// permette, devant un comportement inattendu, de savoir contre quelle version
// d'API le dashboard a parlé.
func (c *Client) Versions() map[string]string {
	return map[string]string{
		"imagepolicies":  c.imagePolicies.GroupVersion().String(),
		"kustomizations": c.kustomizations.GroupVersion().String(),
	}
}

func (c *Client) list(
	ctx context.Context, resource schema.GroupVersionResource, namespace string,
) ([]map[string]any, error) {
	list, err := c.dynamic.Resource(resource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("liste de %s dans %q: %w", resource.Resource, namespace, err)
	}

	items := make([]map[string]any, 0, len(list.Items))
	for _, item := range list.Items {
		items = append(items, item.Object)
	}
	return items, nil
}
