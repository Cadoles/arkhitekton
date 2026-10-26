package tekton

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline/pod"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
)

// Request describes the run to create.
type Request struct {
	// Name is decided by the scheduler before the call, and already recorded in the
	// database. That is what makes an orphaned run impossible.
	Name  string
	JobID int64
	// Instance identifies the database. Without it, a run left in the cluster after a
	// reset concludes the identically numbered job of the next database.
	Instance string
	Pipeline catalog.Pipeline
	Params   map[string]string
}

// Create materialises a PipelineRun.
//
// Everything composing it comes from the catalogue: workspaces, service account,
// timeout, image pull secrets. Nothing is hard-coded here, otherwise a
func (c *Client) Create(ctx context.Context, request Request) (*tektonv1.PipelineRun, error) {
	run := &tektonv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      request.Name,
			Namespace: c.Namespace,
			// The labels are the mark of paternity: they are how the observer tells a driven run
			// from one triggered by a webhook or a cron job, and how reconciliation finds the
			// dashboard's runs again after a restart.
			Labels: map[string]string{
				LabelJobID: fmt.Sprint(request.JobID),
				// A label value has no `/`: `php/unit-tests` reads `php.unit-tests`.
				LabelKind:     strings.ReplaceAll(request.Pipeline.Kind, "/", "."),
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
			return nil, fmt.Errorf("timeout %q of kind %q: %w", timeout, request.Pipeline.Kind, err)
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
			return nil, fmt.Errorf("workspace %q of kind %q: %w", workspace.Name, request.Pipeline.Kind, err)
		}
		run.Spec.Workspaces = append(run.Spec.Workspaces, binding)
	}

	created, err := c.Tekton.TektonV1().PipelineRuns(c.Namespace).Create(ctx, run, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating PipelineRun %q: %w", request.Name, err)
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
			return binding, fmt.Errorf("unreadable size %q: %w", workspace.VolumeClaimTemplate.Storage, err)
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
		// Catalogue validation already forbids it; repeating it here avoids an empty
		// binding, which Tekton would accept before failing at mount time.
		return binding, fmt.Errorf("no source declared")
	}

	return binding, nil
}

// Cancel asks for a run to stop.
//
// Cancellation is a request: setting `spec.status: Cancelled` makes Tekton stop the
// pods, and the informer observes the end. The dashboard returns at once but only
// displays `cancelled` once the cluster agrees — announcing otherwise would suggest a
// stop that did not happen.
//
// The patch is a strategic merge on the single field concerned: an Update would
// overwrite what Tekton wrote to the status meanwhile.
func (c *Client) Cancel(ctx context.Context, name string) error {
	patch := []byte(`{"spec":{"status":"Cancelled"}}`)

	_, err := c.Tekton.TektonV1().PipelineRuns(c.Namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("cancelling PipelineRun %q: %w", name, err)
	}
	return nil
}

// Pipelines returns the deployed Pipelines, for catalogue validation.
func (c *Client) Pipelines(ctx context.Context) (map[string]catalog.PipelineSpec, error) {
	list, err := c.Tekton.TektonV1().Pipelines(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing the Pipelines of %q: %w", c.Namespace, err)
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
			// A parameter with a default need not be supplied by the catalogue: requiring it
			// would mean copying values that already live in the Pipeline, and maintaining them
			// twice.
			spec.Params[param.Name] = param.Default != nil
		}
		deployed[pipeline.Name] = spec
	}
	return deployed, nil
}

// Tasks returns the names of the deployed Tasks, for catalogue validation.
func (c *Client) Tasks(ctx context.Context) (map[string]bool, error) {
	list, err := c.Tekton.TektonV1().Tasks(c.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing the Tasks of %q: %w", c.Namespace, err)
	}
	deployed := make(map[string]bool, len(list.Items))
	for _, task := range list.Items {
		deployed[task.Name] = true
	}
	return deployed, nil
}
