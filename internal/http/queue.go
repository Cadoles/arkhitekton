package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	imagenames "forge.cadoles.com/vcarroy/arkhitekton/internal/images"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// QueueStore is what the handlers need from the database.
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

// Releases is what the launch form needs from the releases.
//
// It is only about populating lists: a parameter naming an existing release leaves an
// empty field that forces going to another screen to copy it — with the typo that comes
// with it.
type Releases interface {
	ListReleases(ctx context.Context, projectID int64, limit int) ([]store.Release, error)
	// The two ways of naming a project: by the request's path, or failing that by the one
	// being built. See internal/http/project.go.
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
}

// QueueHandler exposes the queue and the launching.
type QueueHandler struct {
	queue   QueueStore
	catalog *catalog.Catalog
	// releases populates the tag lists. Nil-safe: without it the fields concerned stay
	// free-text, which is the previous behaviour.
	releases Releases
	// registryProject prefixes the images offered. Empty, no image is suggested: a
	// truncated name would look like a pullable reference without being one.
	registryProject string
	logger          *slog.Logger
	// environments returns the environments offered. Nil: none, the field stays free-text.
	// Supplied by the MSE plugin.
	environments func(ctx context.Context, projectSlug string) []Suggestion
}

func NewQueueHandler(queue QueueStore, pipelines *catalog.Catalog) *QueueHandler {
	return &QueueHandler{queue: queue, catalog: pipelines}
}

// WithSuggestions wires the lists the launch form offers.
func (h *QueueHandler) WithSuggestions(
	releases Releases, registryProject string, logger *slog.Logger,
) *QueueHandler {
	h.releases = releases
	h.registryProject = registryProject
	h.logger = logger
	return h
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
	// QueueReason explains the waiting. A queue that does not explain it systematically
	// produces needless retries.
	QueueReason string `json:"queue_reason,omitempty"`
	RunName     string `json:"run_name,omitempty"`
}

// inputPayload describes a field of the launch form.
type inputPayload struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	// Suggest names the list to offer. Empty, the field stays free — the case of a parameter
	// whose values the dashboard cannot enumerate.
	Suggest string `json:"suggest,omitempty"`
}

type kindPayload struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Inputs carries every typed parameter, not only the required ones: an optional
	// parameter nobody sees is a parameter nobody ever fills in.
	Inputs []inputPayload `json:"inputs"`
}

// option is an offered value.
//
// A value and a label, kept distinct: an environment is sent under its identifier and
// read under its name.
type option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Suggestion is a value the launch form offers.
type Suggestion = option

// WithEnvironmentSuggestions wires the list of environments offered.
func (h *QueueHandler) WithEnvironmentSuggestions(environments func(ctx context.Context, projectSlug string) []Suggestion) *QueueHandler {
	h.environments = environments
	return h
}

// queuePayload is one queue: its ceiling, its occupancy, its jobs.
//
// One queue per kind, including empty ones. Returning only the busy queues would make
// half the settings disappear from the screen, and it is precisely when a queue is
// empty that one wants to check what it allows.
type queuePayload struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// MaxConcurrent is the effective ceiling: the setting if there is one, the catalogue's
	// otherwise.
	MaxConcurrent int `json:"max_concurrent"`
	// DefaultMax is the catalogue's. Returned beside the effective one so the screen can
	// say "set to 2, default 5" rather than a bare number, and offer a return to the
	// default.
	DefaultMax int  `json:"default_max_concurrent"`
	Overridden bool `json:"overridden"`
	// Known tells a catalogue queue from an observed one: local tasks — the delivery note,
	// the release note — do not become PipelineRuns and are absent from it, but they do
	// occupy a queue.
	Known   bool         `json:"known"`
	Running int          `json:"running"`
	Waiting int          `json:"waiting"`
	Jobs    []jobPayload `json:"jobs"`
}

// List returns the queues, their occupancy and the catalogue.
//
// All three together because the screen needs all three: showing jobs without saying
// what can be launched forces a second request to populate the smallest button, and
// without the ceilings one can neither explain a wait nor offer to lift it.
func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.queue.ListQueue(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the queue", "")
		return
	}

	who := resolveActors(r.Context(), h.queue, slog.Default())

	// The jobs are distributed into their queue keeping the order received, which is the
	// order of service. Re-sorting by kind would be enough to lie about who goes first.
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
		// An unreadable setting must not make the queue disappear: the catalogue's ceilings are
		// then shown, which is the behaviour before settings existed.
		slog.Default().Error("reading the queue caps", "error", err)
		overrides = map[string]int{}
	}

	queues := make([]queuePayload, 0, len(h.catalog.Pipelines)+len(order))
	for _, pipeline := range h.catalog.Pipelines {
		queues = append(queues, h.toQueuePayload(
			pipeline.Kind, pipelineLabel(pipeline), pipeline.Concurrency(),
			true, overrides, byKind))
		delete(byKind, pipeline.Kind)
	}
	// Observed queues outside the catalogue come next: they are the local tasks, which do
	// not become PipelineRuns but do occupy a queue.
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
		inputs := make([]inputPayload, 0, len(pipeline.Params))
		for _, input := range pipeline.Inputs() {
			inputs = append(inputs, inputPayload{
				Name: input.Name, Required: input.Required, Suggest: input.SuggestionKey(),
			})
		}
		kinds = append(kinds, kindPayload{
			Kind:   pipeline.Kind,
			Label:  pipelineLabel(pipeline),
			Inputs: inputs,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"queues":      queues,
		"kinds":       kinds,
		"suggestions": h.suggestions(r.Context(), r.URL.Query().Get("project")),
		// The global ceiling, across every queue. It prevails over the per-kind ones: without
		// it, seven queues of five places would allow thirty-five simultaneous runs.
		"max_concurrent": h.catalog.MaxConcurrentRuns,
	})
}

// suggestions builds the lists the launch form offers.
//
// Composed here and not by the client: the tags come from the database, the
// environments from the MSE plugin, and the images from a configuration prefix the
// browser does not know. Having the client assemble them would mean sending it that
// prefix, hence publishing it, for no gain.
//
// Git references are deliberately absent: they are read live from the forge, the call
// costs seconds, and the client already requests them for the release creation form.
// Reloading them here would make the queue pay for it on every refresh, every two
// seconds.
func (h *QueueHandler) suggestions(ctx context.Context, slug string) map[string][]option {
	proposed := map[string][]option{}

	if h.environments != nil {
		if environments := h.environments(ctx, slug); environments != nil {
			proposed[catalog.SuggestEnvironment] = environments
		}
	}

	if h.releases == nil {
		return proposed
	}

	// The most recent first, and not all of them: a list of two hundred entries is not
	// browsed, and what one re-runs concerns what has just come out.
	//
	// The queue is not project-specific: its ceilings are the cluster's, so its path
	// carries no project. The suggestions, though, list releases, which exist only within a
	// project — hence the project in the query.
	//
	// It used to be derived from the single buildable repository, which stopped being a
	// decidable question as soon as a second was declared: the suggestions then disappeared
	// entirely.
	//
	// Absent, the fields concerned stay free-text. A queue usable without suggestions beats
	// a queue in error.
	currentID, err := h.projectOf(ctx, slug)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("suggestions with no project", "project", slug, "error", err)
		}
		return proposed
	}

	releases, err := h.releases.ListReleases(ctx, currentID, suggestedReleases)
	if err != nil {
		// An unreadable list does not fail the queue: the fields concerned stay free-text,
		// which is the previous behaviour.
		if h.logger != nil {
			h.logger.Error("release suggestions", "error", err)
		}
		return proposed
	}

	tags := make([]option, 0, len(releases))
	for _, release := range releases {
		tags = append(tags, option{Value: release.Tag})
	}
	proposed[catalog.SuggestReleaseTag] = tags

	// One list per image declared by the catalogue, and not per image known to the code:
	// two parameters that both want a reference do not expect the same one, and publishing
	// one more image must not require a recompile.
	for _, name := range h.catalog.SuggestedImages() {
		references := make([]option, 0, len(releases))
		for _, release := range releases {
			reference := imagenames.Reference(h.registryProject, name, release.Tag)
			if reference == "" {
				continue
			}
			// The tag as the label: the full reference is long and identical but for a prefix, and
			// the only thing distinguishing two rows is what would be truncated.
			references = append(references, option{Value: reference, Label: release.Tag})
		}
		proposed[catalog.Input{Suggest: catalog.SuggestImage, Image: name}.SuggestionKey()] = references
	}

	return proposed
}

// projectOf resolves the project of the suggestions.
//
// The slug when the request carries one, the buildable repository otherwise — the latter
// stays right while there is only one, and returns an error as soon as there are
// several rather than picking one at random.
func (h *QueueHandler) projectOf(ctx context.Context, slug string) (int64, error) {
	if slug == "" {
		return buildableProject(ctx, h.releases)
	}
	project, err := h.releases.ProjectBySlug(ctx, slug)
	if err != nil {
		return 0, err
	}
	return project.ID, nil
}

// suggestedReleases bounds the list offered.
const suggestedReleases = 30

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
		// An empty array and not null: the interface iterates over it without having to
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

// limitRequest carries a new ceiling.
type limitRequest struct {
	MaxConcurrent int `json:"max_concurrent"`
}

// maxQueueLimit bounds what a setting may ask for.
//
// The global ceiling stays above it anyway, but accepting 10,000 in the form would
// suggest the number means something. Twenty is already more than the cluster holds.
const maxQueueLimit = 20

// SetLimit sets a queue's ceiling.
//
// Reserved to administration: raising a ceiling commits the cluster, and lowering it can
// immobilise a verification mid-way. The value lives in the database and not in the
// catalogue — a setting that requires rebuilding a ConfigMap is not a setting.
func (h *QueueHandler) SetLimit(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	kind := r.PathValue("kind")
	if kind == "" {
		writeProblem(w, http.StatusBadRequest, "Missing queue type", "")
		return
	}

	var request limitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}
	if request.MaxConcurrent < 1 || request.MaxConcurrent > maxQueueLimit {
		writeProblem(w, http.StatusBadRequest, "Invalid ceiling",
			fmt.Sprintf("expected between 1 and %d", maxQueueLimit))
		return
	}

	if err := h.queue.SetQueueLimit(r.Context(), kind, request.MaxConcurrent, identity.Subject); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot set", "")
		return
	}

	// Recorded like the other operational decisions: "who opened the queue to ten" must have
	// an answer without consulting the application logs.
	_ = h.queue.Audit(r.Context(), identity.Subject, store.AuditQueueLimit, kind,
		map[string]any{"max_concurrent": request.MaxConcurrent})

	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "max_concurrent": request.MaxConcurrent})
}

// ClearLimit returns a queue to its catalogue ceiling.
func (h *QueueHandler) ClearLimit(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	kind := r.PathValue("kind")
	if err := h.queue.ClearQueueLimit(r.Context(), kind); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot set", "")
		return
	}

	_ = h.queue.Audit(r.Context(), identity.Subject, store.AuditQueueLimit, kind,
		map[string]any{"max_concurrent": nil})

	writeJSON(w, http.StatusOK, map[string]any{"kind": kind})
}

// maxLaunchParams bounds what a launch may carry.
//
// The body is already bounded in bytes; this ceiling bounds the count, because a
// thousand one-byte parameters would pass the first bound and produce an unmanageable
// PipelineRun spec.
const maxLaunchParams = 40

// paramName bounds the shape of a parameter name.
//
// Tekton is more permissive, but these names also travel in labels and messages:
// sticking to what an identifier can carry avoids having to worry about it everywhere
// else.
var paramName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

type launchRequest struct {
	Kind     string            `json:"kind"`
	Params   map[string]string `json:"params"`
	Priority int               `json:"priority"`
}

// Launch puts a job in the queue.
//
// Launching is never direct: it goes through the queue, the only thing that knows the
// exclusion classes. Creating a PipelineRun from the handler would start a second
// end-to-end campaign during the first, which the whole mechanism exists to prevent.
func (h *QueueHandler) Launch(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	var request launchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Unreadable request", "")
		return
	}

	pipeline, found := h.catalog.Find(request.Kind)
	if !found {
		writeProblem(w, http.StatusBadRequest, "Unknown run type", request.Kind)
		return
	}

	// The required inputs are checked here and not only at scheduling time: a job accepted
	// then rejected two seconds later leaves the user in front of a queue that refuses
	// without saying so.
	for _, name := range pipeline.RequiredInputs() {
		if request.Params[name] == "" {
			writeProblem(w, http.StatusBadRequest, "Missing required parameter", name)
			return
		}
	}

	// Extra parameters — those the catalogue does not declare — are passed to the PipelineRun
	// as they are. That is what allows a customised run, and it is also what requires
	// bounding the shape of names: they land in a Kubernetes object's spec, where a fanciful
	// name produces an unreadable validation error rather than a clear refusal.
	//
	// The separation of sources is not weakened by it: the scheduler takes a supplied
	// parameter only when the catalogue ignores it, so a value sent under the name of a
	// configuration parameter has no effect.
	if len(request.Params) > maxLaunchParams {
		writeProblem(w, http.StatusBadRequest, "Too many parameters",
			fmt.Sprintf("%d at most", maxLaunchParams))
		return
	}
	for name := range request.Params {
		if !paramName.MatchString(name) {
			writeProblem(w, http.StatusBadRequest, "Invalid parameter name", name)
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
		writeProblem(w, http.StatusInternalServerError, "Cannot enqueue", "")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "kind": pipeline.Kind})
}

// Cancel cancels a job.
//
// Two paths depending on the state, and the distinction is not cosmetic: a queued job is
// cancelled in the database, a running job asks the cluster to stop the pods.
//
// That second path goes through an intent recorded in the database, which the scheduler
// carries out: the api does not talk to Kubernetes, its service account does not even
// mount a token. The dashboard therefore returns at once, but will only display
// `cancelled` once the cluster agrees — announcing otherwise would suggest a stop that did
// not happen.
func (h *QueueHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid identifier", "")
		return
	}

	cancelled, err := h.queue.CancelQueuedJob(r.Context(), id, "cancellation requested")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot cancel", "")
		return
	}
	if cancelled {
		writeJSON(w, http.StatusOK, map[string]string{"state": store.JobCancelled})
		return
	}

	job, err := h.queue.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			writeProblem(w, http.StatusNotFound, "Job not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot cancel", "")
		return
	}

	if job.State != store.JobRunning || job.RunName == "" {
		// Already finished: not an error, there is simply nothing to cancel.
		writeJSON(w, http.StatusOK, map[string]string{"state": job.State})
		return
	}

	if _, err := h.queue.RequestCancel(r.Context(), id, "cancellation requested"); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot cancel", "")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"state": "cancelling"})
}
