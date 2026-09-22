// Package flux reads what Flux publishes: image selection and reconciliation.
//
// It no **longer** reads the application's state: that lives in `internal/mse/portaldata`. Flux
// says the intention — the tag a Kustomization kept — the portal says the observation, and
// merging them into one client made the deployer fail on a cluster without the portal's CRD.
//
// The dashboard never writes anything here. It **requests** a promotion by pushing a tag to the
// registry, and Flux then decides; this whole package serves to observe what Flux did, not to
// drive it.
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

// Groups the dashboard reads.
const (
	imageGroup     = "image.toolkit.fluxcd.io"
	kustomizeGroup = "kustomize.toolkit.fluxcd.io"
)

// Client reads the Flux objects.
//
// It uses the dynamic client and **discovers the version served** instead of hard-coding it.
// The reason is not theoretical: `kustomize.toolkit.fluxcd.io/v1` has been stable since Flux
// 2.0, whereas `image.toolkit.fluxcd.io/v1` exists only since Flux 2.7 — one installation
// therefore serves `v1` on one side and `v1beta2` on the other. Hard-coding either would make
// the dashboard fail on half the installations, with a "the server could not find the requested
// resource" that does not name the cause.
type Client struct {
	dynamic dynamic.Interface

	imagePolicies  schema.GroupVersionResource
	kustomizations schema.GroupVersionResource

	// Namespace of the Flux objects. Distinct from the pipelines': Flux lives in `flux-system`.
	Namespace string
}

// Open builds the client and resolves the versions served.
func Open(config *rest.Config, namespace string) (*Client, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("client dynamique: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
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
				"the CRD %s.%s is missing from the cluster: %w", entry.kind, entry.group, err)
		}
		*entry.target = mapping.Resource
	}

	return client, nil
}

func resolver(discoveryClient discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		// The discovery reports a partial error when a group of APIs is unreachable — a failed
		// aggregator, for instance. What was obtained stays usable, and it may be precisely our groups.
		if len(resources) == 0 {
			return nil, fmt.Errorf("discovering the cluster's APIs: %w", err)
		}
	}
	return restmapper.NewDiscoveryRESTMapper(resources), nil
}

// Versions returns the versions kept, for the start-up log.
//
// Logging them is not cosmetic: it is the only trace that allows, faced with unexpected
// behaviour, knowing which API version the dashboard spoke against.
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
		return nil, fmt.Errorf("listing %s in %q: %w", resource.Resource, namespace, err)
	}

	items := make([]map[string]any, 0, len(list.Items))
	for _, item := range list.Items {
		items = append(items, item.Object)
	}
	return items, nil
}
