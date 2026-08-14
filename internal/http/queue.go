package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// QueueStore est ce que les handlers attendent de la base.
type QueueStore interface {
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	ListQueue(ctx context.Context) ([]store.Job, error)
	GetJob(ctx context.Context, id int64) (store.Job, error)
	CancelQueuedJob(ctx context.Context, id int64, reason string) (bool, error)
	RequestCancel(ctx context.Context, id int64, reason string) (bool, error)
}

// QueueHandler expose la file et le lancement.
type QueueHandler struct {
	queue   QueueStore
	catalog *catalog.Catalog
}

func NewQueueHandler(queue QueueStore, pipelines *catalog.Catalog) *QueueHandler {
	return &QueueHandler{queue: queue, catalog: pipelines}
}

type jobPayload struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Label       string     `json:"label"`
	QueueClass  string     `json:"queue_class"`
	Priority    int        `json:"priority"`
	State       string     `json:"state"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	Attempt     int        `json:"attempt"`
	// QueueReason explique l'attente. Une file qui ne l'explique pas engendre
	// systématiquement des relances inutiles.
	QueueReason string `json:"queue_reason,omitempty"`
	RunName     string `json:"run_name,omitempty"`
}

type kindPayload struct {
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	QueueClass string   `json:"queue_class"`
	Inputs     []string `json:"required_inputs"`
}

// List rend la file et le catalogue.
//
// Les deux ensemble parce que l'écran a besoin des deux : afficher les jobs sans
// dire ce qui est lançable oblige à une seconde requête pour peupler le moindre
// bouton.
func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.queue.ListQueue(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de la file impossible", "")
		return
	}

	payload := make([]jobPayload, 0, len(jobs))
	for _, job := range jobs {
		entry := jobPayload{
			ID:          job.ID,
			Kind:        job.Kind,
			Label:       job.Kind,
			QueueClass:  job.QueueClass,
			Priority:    job.Priority,
			State:       job.State,
			RequestedBy: job.RequestedBy,
			RequestedAt: job.RequestedAt,
			StartedAt:   job.StartedAt,
			Attempt:     job.Attempt,
			QueueReason: job.QueueReason,
			RunName:     job.RunName,
		}
		if pipeline, found := h.catalog.Find(job.Kind); found && pipeline.Label != "" {
			entry.Label = pipeline.Label
		}
		payload = append(payload, entry)
	}

	kinds := make([]kindPayload, 0, len(h.catalog.Pipelines))
	for _, pipeline := range h.catalog.Pipelines {
		label := pipeline.Label
		if label == "" {
			label = pipeline.Kind
		}
		kinds = append(kinds, kindPayload{
			Kind:       pipeline.Kind,
			Label:      label,
			QueueClass: pipeline.QueueClass,
			Inputs:     pipeline.RequiredInputs(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":           payload,
		"kinds":          kinds,
		"max_concurrent": h.catalog.MaxConcurrentRuns,
	})
}

type launchRequest struct {
	Kind     string            `json:"kind"`
	Params   map[string]string `json:"params"`
	Priority int               `json:"priority"`
}

// Launch met un job en file.
//
// Le lancement n'est jamais direct : il passe par la file, seule à connaître les
// classes d'exclusion. Créer un PipelineRun depuis le handler ferait démarrer une
// seconde campagne E2E pendant la première, ce que toute la mécanique existe pour
// empêcher.
func (h *QueueHandler) Launch(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request launchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	pipeline, found := h.catalog.Find(request.Kind)
	if !found {
		writeProblem(w, http.StatusBadRequest, "Type d'exécution inconnu", request.Kind)
		return
	}

	// Les entrées requises sont vérifiées ici et pas seulement à l'ordonnancement :
	// un job accepté puis rejeté deux secondes plus tard laisse l'utilisateur
	// devant une file qui refuse sans le dire.
	for _, name := range pipeline.RequiredInputs() {
		if request.Params[name] == "" {
			writeProblem(w, http.StatusBadRequest, "Paramètre requis manquant", name)
			return
		}
	}

	id, err := h.queue.Enqueue(r.Context(), store.JobRequest{
		Kind:        pipeline.Kind,
		QueueClass:  pipeline.QueueClass,
		Priority:    request.Priority,
		Params:      request.Params,
		RequestedBy: identity.Subject,
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Mise en file impossible", "")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "kind": pipeline.Kind})
}

// Cancel annule un job.
//
// Deux chemins selon l'état, et la distinction n'est pas cosmétique : un job en
// file s'annule en base, un job en cours demande au cluster d'arrêter les pods.
//
// Ce second chemin passe par une **intention** enregistrée en base, que
// l'ordonnanceur exécute : l'`api` ne parle pas à Kubernetes, son compte de
// service ne monte même pas de jeton. Le dashboard rend donc la main tout de
// suite, mais n'affichera `cancelled` qu'une fois le cluster d'accord — annoncer
// l'inverse ferait croire à un arrêt qui n'a pas eu lieu.
func (h *QueueHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
		return
	}

	cancelled, err := h.queue.CancelQueuedJob(r.Context(), id, "annulation demandée")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Annulation impossible", "")
		return
	}
	if cancelled {
		writeJSON(w, http.StatusOK, map[string]string{"state": store.JobCancelled})
		return
	}

	job, err := h.queue.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			writeProblem(w, http.StatusNotFound, "Job introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Annulation impossible", "")
		return
	}

	if job.State != store.JobRunning || job.RunName == "" {
		// Déjà terminé : ce n'est pas une erreur, il n'y a simplement rien à
		// annuler.
		writeJSON(w, http.StatusOK, map[string]string{"state": job.State})
		return
	}

	if _, err := h.queue.RequestCancel(r.Context(), id, "annulation demandée"); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Annulation impossible", "")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"state": "cancelling"})
}
