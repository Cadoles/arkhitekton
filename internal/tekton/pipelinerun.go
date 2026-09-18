package tekton

import (
	"context"
	"fmt"
	"time"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline/pod"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// Request décrit le run à créer.
type Request struct {
	// Name est décidé par l'ordonnanceur **avant** l'appel, et déjà inscrit en
	// base. C'est ce qui rend un run orphelin impossible (doc 06 §6.3).
	Name  string
	JobID int64
	// Instance identifie la base. Sans elle, un run resté dans le cluster après
	// une remise à zéro conclut le job homonyme de la base suivante.
	Instance string
	Pipeline catalog.Pipeline
	Params   map[string]string
}

// Create matérialise un PipelineRun.
//
// Tout ce qui le compose vient du catalogue : workspaces, compte de service,
// timeout, secrets de tirage d'image. Rien n'est codé ici, sans quoi une
// évolution de `mse-tekton` casserait le dashboard sans le dire.
func (c *Client) Create(ctx context.Context, request Request) (*tektonv1.PipelineRun, error) {
	run := &tektonv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      request.Name,
			Namespace: c.Namespace,
			// Les labels sont la marque de paternité : c'est par eux que
			// l'observateur distingue un run piloté d'un run déclenché par un
			// webhook ou un CronJob, et par eux que la réconciliation retrouve
			// les runs du dashboard après un redémarrage.
			Labels: map[string]string{
				LabelJobID:    fmt.Sprint(request.JobID),
				LabelKind:     request.Pipeline.Kind,
				LabelInstance: request.Instance,
			},
		},
		Spec: tektonv1.PipelineRunSpec{
			PipelineRef: &tektonv1.PipelineRef{Name: request.Pipeline.PipelineRef},
		},
	}

	for name, value := range request.Params {
		run.Spec.Params = append(run.Spec.Params, tektonv1.Param{
			Name:  name,
			Value: *tektonv1.NewStructuredValues(value),
		})
	}

	if account := request.Pipeline.ServiceAccountName; account != "" {
		run.Spec.TaskRunTemplate.ServiceAccountName = account
	}

	if timeout := request.Pipeline.Timeout; timeout != "" {
		parsed, err := time.ParseDuration(timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout %q du kind %q: %w", timeout, request.Pipeline.Kind, err)
		}
		run.Spec.Timeouts = &tektonv1.TimeoutFields{Pipeline: &metav1.Duration{Duration: parsed}}
	}

	if template := request.Pipeline.PodTemplate; template != nil && len(template.ImagePullSecrets) > 0 {
		secrets := make([]corev1.LocalObjectReference, 0, len(template.ImagePullSecrets))
		for _, name := range template.ImagePullSecrets {
			secrets = append(secrets, corev1.LocalObjectReference{Name: name})
		}
		run.Spec.TaskRunTemplate.PodTemplate = &pod.Template{ImagePullSecrets: secrets}
	}

	for _, workspace := range request.Pipeline.Workspaces {
		binding, err := workspaceBinding(workspace)
		if err != nil {
			return nil, fmt.Errorf("workspace %q du kind %q: %w", workspace.Name, request.Pipeline.Kind, err)
		}
		run.Spec.Workspaces = append(run.Spec.Workspaces, binding)
	}

	created, err := c.Tekton.TektonV1().PipelineRuns(c.Namespace).Create(ctx, run, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("création du PipelineRun %q: %w", request.Name, err)
	}
	return created, nil
}

func workspaceBinding(workspace catalog.Workspace) (tektonv1.WorkspaceBinding, error) {
	binding := tektonv1.WorkspaceBinding{Name: workspace.Name}

	switch {
	case workspace.Secret != nil:
		binding.Secret = &corev1.SecretVolumeSource{SecretName: workspace.Secret.SecretName}

	case workspace.EmptyDir:
		binding.EmptyDir = &corev1.EmptyDirVolumeSource{}

	case workspace.VolumeClaimTemplate != nil:
		quantity, err := resource.ParseQuantity(workspace.VolumeClaimTemplate.Storage)
		if err != nil {
			return binding, fmt.Errorf("taille %q illisible: %w", workspace.VolumeClaimTemplate.Storage, err)
		}

		modes := make([]corev1.PersistentVolumeAccessMode, 0, len(workspace.VolumeClaimTemplate.AccessModes))
		for _, mode := range workspace.VolumeClaimTemplate.AccessModes {
			modes = append(modes, corev1.PersistentVolumeAccessMode(mode))
		}

		claim := &corev1.PersistentVolumeClaim{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: modes,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
				},
			},
		}
		if class := workspace.VolumeClaimTemplate.StorageClassName; class != "" {
			claim.Spec.StorageClassName = &class
		}
		binding.VolumeClaimTemplate = claim

	default:
		// La validation du catalogue l'interdit déjà ; le répéter ici évite une
		// liaison vide, que Tekton accepterait avant d'échouer au montage.
		return binding, fmt.Errorf("aucune source déclarée")
	}

	return binding, nil
}

// Cancel demande l'arrêt d'un run.
//
// L'annulation est une **demande** : poser `spec.status: Cancelled` fait arrêter
// les pods par Tekton, et c'est l'informer qui constate la fin. Le dashboard rend
// la main tout de suite mais n'affiche `cancelled` qu'une fois le cluster
// d'accord — annoncer l'inverse ferait croire à un arrêt qui n'a pas eu lieu.
//
// Le patch est un merge stratégique sur le seul champ concerné : un `Update`
// écraserait ce que Tekton a écrit entre-temps dans le statut.
func (c *Client) Cancel(ctx context.Context, name string) error {
	patch := []byte(`{"spec":{"status":"Cancelled"}}`)

	_, err := c.Tekton.TektonV1().PipelineRuns(c.Namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("annulation du PipelineRun %q: %w", name, err)
	}
	return nil
}

// Pipelines rend les Pipeline déployés, pour la validation du catalogue.
func (c *Client) Pipelines(ctx context.Context) (map[string]catalog.PipelineSpec, error) {
	list, err := c.Tekton.TektonV1().Pipelines(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("liste des Pipeline de %q: %w", c.Namespace, err)
	}

	deployed := make(map[string]catalog.PipelineSpec, len(list.Items))
	for _, pipeline := range list.Items {
		spec := catalog.PipelineSpec{
			Name:       pipeline.Name,
			Params:     map[string]bool{},
			Workspaces: map[string]bool{},
		}
		for _, workspace := range pipeline.Spec.Workspaces {
			spec.Workspaces[workspace.Name] = workspace.Optional
		}
		for _, param := range pipeline.Spec.Params {
			// Un paramètre à valeur par défaut n'a pas à être fourni par le
			// catalogue : l'exiger obligerait à recopier des valeurs qui vivent
			// déjà dans `mse-tekton`, et à les maintenir en double.
			spec.Params[param.Name] = param.Default != nil
		}
		deployed[pipeline.Name] = spec
	}
	return deployed, nil
}
