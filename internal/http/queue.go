package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	Directory(ctx context.Context) (map[string]string, error)
	QueueLimits(ctx context.Context) (map[string]int, error)
	SetQueueLimit(ctx context.Context, kind string, max int, by string) error
	ClearQueueLimit(ctx context.Context, kind string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
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
	Kind   string   `json:"kind"`
	Label  string   `json:"label"`
	Inputs []string `json:"required_inputs"`
}

// queuePayload est une file : son plafond, son occupation, ses jobs.
//
// Une file par type, y compris vide. Ne rendre que les files occupées ferait
// disparaître de l'écran la moitié des réglages, et c'est justement quand une
// file est vide qu'on veut vérifier ce qu'elle autorise.
type queuePayload struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// MaxConcurrent est le plafond effectif : le réglage s'il existe, celui du
	// catalogue sinon.
	MaxConcurrent int `json:"max_concurrent"`
	// DefaultMax est celui du catalogue. Rendu à côté de l'effectif pour que
	// l'écran puisse dire « réglé à 2, par défaut 5 » plutôt qu'un nombre seul,
	// et proposer le retour au défaut.
	DefaultMax int  `json:"default_max_concurrent"`
	Overridden bool `json:"overridden"`
	// Known distingue une file du catalogue d'une file constatée : les tâches
	// locales — bon de livraison, note de release — ne deviennent pas des
	// PipelineRun et n'y figurent pas, mais occupent bien une file.
	Known   bool         `json:"known"`
	Running int          `json:"running"`
	Waiting int          `json:"waiting"`
	Jobs    []jobPayload `json:"jobs"`
}

// List rend les files, leur occupation et le catalogue.
//
// Les trois ensemble parce que l'écran a besoin des trois : afficher des jobs
// sans dire ce qui est lançable oblige à une seconde requête pour peupler le
// moindre bouton, et sans les plafonds on ne peut ni expliquer une attente ni
// proposer de la lever.
func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.queue.ListQueue(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de la file impossible", "")
		return
	}

	who := resolveActors(r.Context(), h.queue, slog.Default())

	// Les jobs sont répartis dans leur file en conservant l'ordre reçu, qui est
	// celui du service. Retrier par type suffirait à mentir sur qui passe en
	// premier.
	byKind := map[string][]jobPayload{}
	order := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if _, seen := byKind[job.Kind]; !seen {
			order = append(order, job.Kind)
		}
		byKind[job.Kind] = append(byKind[job.Kind], h.toJobPayload(job, who))
	}

	overrides, err := h.queue.QueueLimits(r.Context())
	if err != nil {
		// Un réglage illisible ne doit pas faire disparaître la file : les
		// plafonds du catalogue s'affichent alors, ce qui est le comportement
		// d'avant les réglages.
		slog.Default().Error("lecture des plafonds de file", "error", err)
		overrides = map[string]int{}
	}

	queues := make([]queuePayload, 0, len(h.catalog.Pipelines)+len(order))
	for _, pipeline := range h.catalog.Pipelines {
		queues = append(queues, h.toQueuePayload(
			pipeline.Kind, pipelineLabel(pipeline), pipeline.Concurrency(),
			true, overrides, byKind))
		delete(byKind, pipeline.Kind)
	}
	// Les files constatées hors catalogue passent ensuite : ce sont les tâches
	// locales, qui ne deviennent pas des PipelineRun mais occupent bien une file.
	for _, kind := range order {
		if _, remaining := byKind[kind]; !remaining {
			continue
		}
		queues = append(queues, h.toQueuePayload(
			kind, kind, catalog.DefaultKindConcurrency, false, overrides, byKind))
		delete(byKind, kind)
	}

	kinds := make([]kindPayload, 0, len(h.catalog.Pipelines))
	for _, pipeline := range h.catalog.Pipelines {
		kinds = append(kinds, kindPayload{
			Kind:   pipeline.Kind,
			Label:  pipelineLabel(pipeline),
			Inputs: pipeline.RequiredInputs(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"queues": queues,
		"kinds":  kinds,
		// Le plafond global, toutes files confondues. Il prime sur les plafonds
		// par type : sans lui, sept files à cinq places autoriseraient
		// trente-cinq exécutions simultanées.
		"max_concurrent": h.catalog.MaxConcurrentRuns,
	})
}

func pipelineLabel(pipeline catalog.Pipeline) string {
	if pipeline.Label != "" {
		return pipeline.Label
	}
	return pipeline.Kind
}

func (h *QueueHandler) toJobPayload(job store.Job, who actors) jobPayload {
	entry := jobPayload{
		ID:          job.ID,
		Kind:        job.Kind,
		Label:       job.Kind,
		Priority:    job.Priority,
		State:       job.State,
		RequestedBy: who.name(job.RequestedBy),
		RequestedAt: job.RequestedAt,
		StartedAt:   job.StartedAt,
		Attempt:     job.Attempt,
		QueueReason: job.QueueReason,
		RunName:     job.RunName,
	}
	if pipeline, found := h.catalog.Find(job.Kind); found {
		entry.Label = pipelineLabel(pipeline)
	}
	return entry
}

func (h *QueueHandler) toQueuePayload(
	kind, label string, fromCatalog int, known bool,
	overrides map[string]int, byKind map[string][]jobPayload,
) queuePayload {
	queue := queuePayload{
		Kind: kind, Label: label,
		MaxConcurrent: fromCatalog, DefaultMax: fromCatalog, Known: known,
		Jobs: byKind[kind],
	}
	if limit, set := overrides[kind]; set {
		queue.MaxConcurrent = limit
		queue.Overridden = true
	}
	if queue.Jobs == nil {
		// Un tableau vide et non `null` : l'interface itère dessus sans avoir à
		// se prémunir de l'absence.
		queue.Jobs = []jobPayload{}
	}
	for _, job := range queue.Jobs {
		if job.State == store.JobRunning {
			queue.Running++
			continue
		}
		queue.Waiting++
	}
	return queue
}

// limitRequest porte un nouveau plafond.
type limitRequest struct {
	MaxConcurrent int `json:"max_concurrent"`
}

// maxQueueLimit borne ce qu'un réglage peut demander.
//
// Le plafond global reste au-dessus de toute façon, mais accepter 10 000 dans le
// formulaire laisserait croire que le nombre a un sens. Vingt est déjà plus que
// ce que le cluster tient.
const maxQueueLimit = 20

// SetLimit règle le plafond d'une file.
//
// Réservé à l'administration : relever un plafond engage le cluster, et le
// baisser peut immobiliser une vérification en cours de route. La valeur vit en
// base et non dans le catalogue — un réglage qui exige de reconstruire un
// ConfigMap n'est pas un réglage.
func (h *QueueHandler) SetLimit(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	kind := r.PathValue("kind")
	if kind == "" {
		writeProblem(w, http.StatusBadRequest, "Type de file manquant", "")
		return
	}

	var request limitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}
	if request.MaxConcurrent < 1 || request.MaxConcurrent > maxQueueLimit {
		writeProblem(w, http.StatusBadRequest, "Plafond invalide",
			fmt.Sprintf("attendu entre 1 et %d", maxQueueLimit))
		return
	}

	if err := h.queue.SetQueueLimit(r.Context(), kind, request.MaxConcurrent, identity.Subject); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Réglage impossible", "")
		return
	}

	// Consigné comme les autres décisions d'exploitation : « qui a ouvert la file
	// à dix » doit avoir une réponse sans consulter les journaux applicatifs.
	_ = h.queue.Audit(r.Context(), identity.Subject, store.AuditQueueLimit, kind,
		map[string]any{"max_concurrent": request.MaxConcurrent})

	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "max_concurrent": request.MaxConcurrent})
}

// ClearLimit rend une file à son plafond de catalogue.
func (h *QueueHandler) ClearLimit(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	kind := r.PathValue("kind")
	if err := h.queue.ClearQueueLimit(r.Context(), kind); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Réglage impossible", "")
		return
	}

	_ = h.queue.Audit(r.Context(), identity.Subject, store.AuditQueueLimit, kind,
		map[string]any{"max_concurrent": nil})

	writeJSON(w, http.StatusOK, map[string]any{"kind": kind})
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
