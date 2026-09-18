// Package tekton carries cluster access: clients, informers, and the translation of
// Tekton objects into the dashboard's model.
//
// One rule holds the package together: the dashboard observes Tekton. It creates
// PipelineRuns referencing existing Pipelines; it never writes to Pipelines or
// Tasks — those belong to whoever maintains them.
package tekton

import (
	"fmt"
	"time"

	tektonclient "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Client gathers the cluster accesses.
type Client struct {
	// Kube serves pods and their logs: collection goes through the pods API, never
	// through executing anything in a container.
	Kube kubernetes.Interface
	// Tekton serves PipelineRuns and TaskRuns.
	Tekton tektonclient.Interface
	// Namespace bounds everything this package does. No operation is cluster-wide: the
	// RBAC granted is a Role, not a ClusterRole. See docs/09-security.md.
	Namespace string
	// RESTConfig is re-exposed for clients reading other APIs of the same cluster —
	// Flux, the application's own — without redoing the in-cluster then kubeconfig
	// resolution, whose order is what stops the process talking to the wrong cluster.
	RESTConfig *rest.Config
}

// Open builds the clients.
//
// The in-cluster configuration is tried first, then the kubeconfig. The order
// matters: in production the worker runs in a pod, and a kubeconfig left in the
// image would make it talk to the wrong cluster with nothing saying so.
func Open(cfg config.TektonConfig) (*Client, error) {
	restConfig, err := restConfig(cfg.KubeconfigPath)
	if err != nil {
		return nil, err
	}

	// client-go's defaults — five requests per second — are sized for a controller that
	// writes little. Resynchronising at start-up lists hundreds of objects at once and
	// would be throttled, with a slow start-up as the only symptom.
	restConfig.QPS = 50
	restConfig.Burst = 100
	restConfig.Timeout = 30 * time.Second

	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("client Kubernetes: %w", err)
	}

	tekton, err := tektonclient.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("client Tekton: %w", err)
	}

	return &Client{Kube: kube, Tekton: tekton, Namespace: cfg.Namespace, RESTConfig: restConfig}, nil
}

func restConfig(kubeconfigPath string) (*rest.Config, error) {
	if inCluster, err := rest.InClusterConfig(); err == nil {
		return inCluster, nil
	}

	// Outside a cluster: KUBECONFIG, then ~/.kube/config. clientcmd's loading rules are
	// taken as they are so the worker targets the same context as `kubectl` in the same
	// terminal — a divergence between the two costs hours of diagnosis.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("configuration Kubernetes (ni in-cluster, ni kubeconfig): %w", err)
	}
	return config, nil
}
