// Package portaldata reads what the portal's applications report about themselves, through the
// `PortalData` CRD.
//
// Separate from `internal/mse/flux`, and the separation is no tidying. Both packages answered two
// different questions from a single client: Flux says the **intention** — the tag a Kustomization
// kept — the portal says the **observation** — such a version is running, and its migrations
// completed. It is their gap that the environments screen exists to show.
//
// The concrete consequence of the merge was an outage: `flux.Open` resolved the three CRDs at
// once and **failed if one was missing**. A cluster without the portal's CRD — that is, any
// cluster that is not MSE's — could therefore have no deployer, although Flux was perfectly
// installed there. The optional dependency failed the main dependency.
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
)

// group is the CRD's API group. Specific to the MSE portal — exactly what a plugin would carry
// when the time comes — but what the CRD reports, "such a version is running and it is usable",
// is not.
const group = "mse.cnous.fr"

// Client reads the `PortalData` CRD.
//
// Like the Flux client, it uses the dynamic client and **discovers the version served** instead
// of hard-coding it: a CRD versioned by its author changes version without warning us, and
// hard-coding `v1alpha1` would fail the read with a "the server could not find the requested
// resource" that does not name the cause.
type Client struct {
	dynamic  dynamic.Interface
	resource schema.GroupVersionResource
}

// Open builds the client and resolves the version served.
//
// Returns a named error when the CRD is absent: the caller must be able to tell "this cluster
// attests nothing", which is a valid configuration, from an access failure.
func Open(config *rest.Config) (*Client, error) {
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

	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: group, Kind: "PortalData"})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAbsent, err)
	}

	return &Client{dynamic: dynamicClient, resource: mapping.Resource}, nil
}

// ErrAbsent reports that the cluster does not serve the CRD.
//
// It is not an outage: a cluster with no portal is a cluster where nothing attests, and the
// dashboard falls back there to what the deployment declares. Saying it this way avoids sending
// someone hunting for an incident where there is only an absence.
var ErrAbsent = fmt.Errorf("the CRD PortalData.%s is missing from the cluster", group)

func resolver(discoveryClient discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		// The discovery reports a partial error when a group of APIs is unreachable — a failed
		// aggregator, for instance. What was obtained stays usable, and it may be precisely our group.
		if len(resources) == 0 {
			return nil, fmt.Errorf("discovering the cluster's APIs: %w", err)
		}
	}
	return restmapper.NewDiscoveryRESTMapper(resources), nil
}

// Version returns the version kept, for the start-up log.
//
// Logging it is not cosmetic: it is the only trace that allows, faced with unexpected behaviour,
// knowing which API version the dashboard spoke against.
func (c *Client) Version() string {
	return c.resource.GroupVersion().String()
}

// Attestations reads what a namespace's applications report about themselves.
//
// The only source coming from the application **while running**, and the only one to say whether
// the migrations completed: a deployed image whose migrations failed is not a delivered version,
// and nothing else reports it.
func (c *Client) Attestations(
	ctx context.Context, namespace string,
) ([]Attestation, error) {
	list, err := c.dynamic.Resource(c.resource).Namespace(namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing the PortalData objects in %q: %w", namespace, err)
	}

	states := make([]Attestation, 0, len(list.Items))
	for _, item := range list.Items {
		object := item.Object

		state := Attestation{Namespace: namespace, Name: item.GetName()}
		state.State, _, _ = unstructured.NestedString(object, "status", "phase")
		// `dataVersion` is the version of the **image** that performed the last bootstrap, not that of
		// the release. A final release reusing its RC's image, the matching is done on
		// `releases.image_version` and never on the tag.
		state.Version, _, _ = unstructured.NestedString(object, "status", "dataVersion")
		// `Ready` is the verdict, and it is returned here because the vocabulary carrying it belongs to
		// the CRD. `status.schemaVersion` also exists but is not read: it is a Doctrine class name, and
		// confusing it with a release version is the mistake that re-reading the CRD fixed.
		state.Ready = state.State == "Ready"

		states = append(states, state)
	}
	return states, nil
}

// Attestation is what a running application reports about itself.
type Attestation struct {
	Namespace string
	Name      string
	// Version is that of the **image** running, not that of the release. A final release reusing its
	// pre-release's image, the matching is done on `releases.image_version` and never on the tag.
	Version string
	// State is the state reported, taken as it is for display.
	State string
	// Ready says that the migrations completed. It is the only case where a version can be asserted
	// **delivered**.
	Ready bool
}
