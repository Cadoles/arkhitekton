package tekton

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodLogs reads the logs of pods in the pipelines' namespace.
//
// This is the dashboard's only access to a pod's content. `pods/exec` is
// deliberately absent from the RBAC: reading a log does not require executing
// anything.
type PodLogs struct {
	client *Client
}

// NewPodLogs wraps the client for the collector.
func NewPodLogs(client *Client) *PodLogs {
	return &PodLogs{client: client}
}

// Stream opens a container's log in follow mode.
//
// `Timestamps: true` is not cosmetic: the per-line timestamp is what makes resuming
// possible after the worker restarts. Without it there would be no way to know where
// one stopped, and everything would have to be re-read — or lost.
//
// `since` has second precision on the API side: resuming therefore returns lines
// already known, which the caller discards. The trade is deliberate — duplicating is
// fixable, losing is not.
func (p *PodLogs) Stream(
	ctx context.Context, pod, container string, since *time.Time,
) (io.ReadCloser, error) {
	options := &corev1.PodLogOptions{
		Container:  container,
		Follow:     true,
		Timestamps: true,
	}
	if since != nil {
		sinceTime := metav1.NewTime(*since)
		options.SinceTime = &sinceTime
	}

	stream, err := p.client.Kube.CoreV1().
		Pods(p.client.Namespace).
		GetLogs(pod, options).
		Stream(ctx)
	if err != nil {
		// The most frequent case: the container has not started. The caller retries, and
		// there is nothing abnormal to report here.
		return nil, fmt.Errorf("log of %s/%s: %w", pod, container, err)
	}
	return stream, nil
}
