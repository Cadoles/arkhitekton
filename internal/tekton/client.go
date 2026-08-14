// Package tekton porte l'accès au cluster : clients, informers, et traduction
// des objets Tekton vers le modèle du dashboard.
//
// Une règle tient tout le paquet : le dashboard **observe** Tekton. Il crée des
// PipelineRun qui référencent des Pipeline existants, il n'écrit jamais sur les
// Pipeline ni sur les Task — elles appartiennent à `mse-tekton` (doc 03 §3.6).
package tekton

import (
	"fmt"
	"time"

	tektonclient "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Client regroupe les accès au cluster.
type Client struct {
	// Kube sert aux pods et à leurs logs : la collecte passe par l'API des
	// pods, jamais par une exécution dans un conteneur.
	Kube kubernetes.Interface
	// Tekton sert aux PipelineRun et TaskRun.
	Tekton tektonclient.Interface
	// Namespace borne tout ce que fait ce paquet. Aucune opération n'est faite
	// à l'échelle du cluster : la RBAC accordée est une Role, pas une
	// ClusterRole (doc 07 §7.3).
	Namespace string
	// RESTConfig est réexposée pour les clients qui lisent d'autres API du même
	// cluster — Flux, PortalData — sans refaire la résolution in-cluster puis
	// kubeconfig, dont l'ordre est ce qui empêche de parler au mauvais cluster.
	RESTConfig *rest.Config
}

// Open construit les clients.
//
// La configuration in-cluster est essayée d'abord, puis le kubeconfig. L'ordre
// compte : en production le worker tourne dans un pod, et un kubeconfig traînant
// dans l'image le ferait parler au mauvais cluster sans que rien ne le signale.
func Open(cfg config.TektonConfig) (*Client, error) {
	restConfig, err := restConfig(cfg.KubeconfigPath)
	if err != nil {
		return nil, err
	}

	// Les valeurs par défaut de client-go — 5 requêtes par seconde — sont
	// dimensionnées pour un contrôleur qui écrit peu. La resynchronisation au
	// démarrage liste des centaines d'objets d'un coup et s'y ferait étrangler,
	// avec pour seul symptôme un démarrage anormalement lent.
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

	// Hors cluster : KUBECONFIG, puis ~/.kube/config. Les règles de chargement
	// de clientcmd sont reprises telles quelles pour que le worker vise le même
	// contexte que `kubectl` dans le même terminal — une divergence entre les
	// deux se paie en heures de diagnostic.
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
