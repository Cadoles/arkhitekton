package tekton

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodLogs lit les journaux des pods du namespace des pipelines.
//
// C'est le seul accès du dashboard au contenu d'un pod. `pods/exec` est
// volontairement absent de la RBAC : lire un journal ne demande pas d'exécuter
// quoi que ce soit (doc 07 §7.3).
type PodLogs struct {
	client *Client
}

// NewPodLogs enveloppe le client pour le collecteur.
func NewPodLogs(client *Client) *PodLogs {
	return &PodLogs{client: client}
}

// Stream ouvre le journal d'un conteneur en mode suivi.
//
// `Timestamps: true` n'est pas cosmétique : c'est l'horodatage par ligne qui rend
// la reprise possible après un redémarrage du worker. Sans lui, il n'y aurait aucun
// moyen de savoir où l'on s'était arrêté, et il faudrait tout relire — ou tout
// perdre.
//
// `since` a une précision à la seconde côté API : la reprise renvoie donc des
// lignes déjà connues, que l'appelant écarte. Le compromis est volontaire —
// dupliquer se corrige, perdre ne se corrige pas.
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
		// Cas le plus fréquent : le conteneur n'a pas encore démarré. L'appelant
		// réessaie, il n'y a rien d'anormal à signaler ici.
		return nil, fmt.Errorf("journal de %s/%s: %w", pod, container, err)
	}
	return stream, nil
}
