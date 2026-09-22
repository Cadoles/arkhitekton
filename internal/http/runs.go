package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// RunDetails is what the handler needs to serve a run.
type RunDetails interface {
	GetRun(ctx context.Context, name string) (store.RunDetail, error)
}

// Runs describes what the handler needs from the database.
type Runs interface {
	ListRuns(ctx context.Context, limit int) ([]store.RunSummary, error)
}

// RunsHandler exposes the observed runs.
type RunsHandler struct {
	runs Runs
	// dashboardURL is the root of the Tekton dashboard. Empty, the link is omitted rather
	// than rendered dead: a link that leads nowhere costs more than it returns.
	dashboardURL string
	// directory translates OIDC subjects into names. Nil-safe: without it the subject is
	// displayed as is, which stays identifiable.
	directory DirectoryReader
	logger    *slog.Logger
}

func NewRunsHandler(runs Runs) *RunsHandler {
	return &RunsHandler{runs: runs}
}

// WithTektonDashboard wires the link to the run as Tekton sees it.
//
// This dashboard shows what it kept of a run: status, steps, collected logs. Tekton's
// shows the object itself — its resolved parameters, its workspaces, its results. When the
// two diverge, the second settles it, and one must be able to get there without
// rebuilding the URL by hand.
func (h *RunsHandler) WithTektonDashboard(url string) *RunsHandler {
	h.dashboardURL = strings.TrimRight(url, "/")
	return h
}

// WithDirectory wires the translation of subjects into names.
func (h *RunsHandler) WithDirectory(directory DirectoryReader, logger *slog.Logger) *RunsHandler {
	h.directory = directory
	h.logger = logger
	return h
}

// dashboardLink composes the run's URL in the Tekton dashboard.
//
// The `#/namespaces/<ns>/pipelineruns/<name>` shape is that of the Tekton dashboard's
// router. An empty namespace would give a URL that opens on an error page: better not to
// offer the link then.
func (h *RunsHandler) dashboardLink(run store.RunSummary) string {
	if h.dashboardURL == "" || run.Namespace == "" {
		return ""
	}
	return h.dashboardURL + "/#/namespaces/" + url.PathEscape(run.Namespace) +
		"/pipelineruns/" + url.PathEscape(run.Name)
}

// runPayload is the representation sent to the front end.
//
// A dedicated type rather than the store's structure: the API contract must not follow
// the schema's evolution, and the names go to snake_case, the convention of the rest of
// the API.
type runPayload struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Pipeline    string     `json:"pipeline"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Observed tells a run the dashboard launched from one it merely observes: the interface
	// offers neither cancellation nor re-run on the second.
	Observed bool `json:"observed"`

	// Origin says where the run came from: the release it serves, or failing that the person
	// who asked for it. Both fields are omitted when empty — an observed run has neither, and
	// showing a dash in their place would suggest lost data.
	ReleaseTag  string `json:"release_tag,omitempty"`
	TriggeredBy string `json:"triggered_by,omitempty"`
	// DashboardURL points at the object in the Tekton dashboard.
	DashboardURL string `json:"dashboard_url,omitempty"`
}

// List returns the most recent runs.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
		// An unreadable limit is ignored rather than refused: the default is safe, and a 400 on a
		// display parameter teaches nothing.
	}

	runs, err := h.runs.ListRuns(r.Context(), limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the runs", "")
		return
	}

	payload := make([]runPayload, 0, len(runs))
	for _, run := range runs {
		payload = append(payload, toRunPayload(run))
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": payload})
}

type taskPayload struct {
	Name        string     `json:"name"`
	DisplayName string     `json:"display_name"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// HasLog says whether there is anything to read; Sealed, whether there is any point in
	// following it.
	HasLog bool `json:"has_log"`
	Sealed bool `json:"log_sealed"`
}

// Get returns a run and its tasks.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	details, ok := h.runs.(RunDetails)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "Detail unavailable", "")
		return
	}

	detail, err := details.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			// 404 and not 500: a run purged by clean-up cron jobs is a normal case, and the interface
			// must be able to say so.
			writeProblem(w, http.StatusNotFound, "Run not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the run", "")
		return
	}

	tasks := mergeTasks(detail)

	run := toRunPayload(detail.Run)
	run.ReleaseTag = detail.Run.ReleaseTag
	run.DashboardURL = h.dashboardLink(detail.Run)
	if detail.Run.RequestedBy != "" {
		run.TriggeredBy = h.actorName(r.Context(), detail.Run.RequestedBy)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run":   run,
		"tasks": tasks,
	})
}

// mergeTasks composes a run's task list: those that started, and those the pipeline
// declares without having launched them yet.
//
// Tekton creates a TaskRun only when a task starts. Without this completion, an
// eight-stage pipeline showed two at first, and nothing said what was left to do — nor how
// long there was still to wait.
//
// The order is the graph's, given by the declaration, and not the order of starts: an
// unstarted task has no date, and sorting it by what it does not have would have pushed
// it to the end of the list whatever its real place. Observed tasks absent from the
// declaration — a pipeline modified meanwhile, a run projected before the declarations
// were recorded — are kept at the head: hiding them would lose a real run in favour of a
// definition.
func mergeTasks(detail store.RunDetail) []taskPayload {
	// The matching is on the pipeline task's name, and that is the whole subject: an observed
	// task carries two names — its TaskRun's, and that of the task it runs. Only the second
	// exists in the declaration. Confusing them displayed the same step twice: once run under
	// its technical name, once "pending" under its label.
	//
	// The field carrying it is called DisplayName by an accident of history: it receives the
	// `tekton.dev/pipelineTask` label, which is an identity, not a label. The real human label
	// lives only in the declaration.
	observed := make(map[string]store.TaskSummary, len(detail.Tasks))
	for _, task := range detail.Tasks {
		observed[task.DisplayName] = task
	}

	tasks := make([]taskPayload, 0, len(detail.Tasks)+len(detail.Declared))

	// What ran without being declared comes first, in its original order.
	declared := make(map[string]bool, len(detail.Declared))
	for _, task := range detail.Declared {
		declared[task.Name] = true
	}
	for _, task := range detail.Tasks {
		if !declared[task.DisplayName] {
			tasks = append(tasks, toTaskPayload(task, ""))
		}
	}

	for _, task := range detail.Declared {
		if started, ok := observed[task.Name]; ok {
			// The label comes from the declaration rather than the technical name. A task must not
			// change name when it starts.
			tasks = append(tasks, toTaskPayload(started, task.DisplayName))
			continue
		}
		// Not started yet. `pending` is the same vocabulary as a run that has not begun: the
		// interface already knows how to render it.
		tasks = append(tasks, taskPayload{
			Name: task.Name, DisplayName: taskLabel(task.DisplayName, task.Name),
			Status: "pending",
		})
	}
	return tasks
}

// taskLabel returns the human label, or the technical name failing that.
func taskLabel(displayName, fallback string) string {
	if displayName != "" {
		return displayName
	}
	return fallback
}

// toTaskPayload translates an observed task.
//
// `label` is the declaration's label, empty when the task is not in it. Name stays the
// TaskRun's name: it is how the logs are read.
func toTaskPayload(task store.TaskSummary, label string) taskPayload {
	return taskPayload{
		Name:        task.Name,
		DisplayName: taskLabel(label, task.DisplayName),
		Status:      task.Status,
		StartedAt:   task.StartedAt,
		CompletedAt: task.CompletedAt,
		HasLog:      task.HasLog || task.PodName != "",
		Sealed:      task.LogSealed,
	}
}

// actorName translates an OIDC subject into a readable name.
func (h *RunsHandler) actorName(ctx context.Context, subject string) string {
	if h.directory == nil {
		return subject
	}
	return resolveActors(ctx, h.directory, h.logger).name(subject)
}

// RunEvents is what the state stream needs from the database.
type RunEvents interface {
	RunsUpdatedSince(ctx context.Context, since time.Time) ([]store.RunSummary, time.Time, error)
}

// runPollInterval separates two reads of the runs table.
const runPollInterval = 2 * time.Second

// Stream pushes the runs' state changes.
//
// The stream transmits only what changed: returning the whole list on every turn would
// retransmit hundreds of frozen runs for a single change, and the interface would not know
// which one moved.
//
// The resume identifier is the timestamp of the last modification seen. A client that
// reconnects therefore asks only for the difference — but the mechanism is no delivery
// guarantee: this stream speeds up display, it does not found it. The list stays the
// source of truth, and the interface makes do with it if the stream falls over.
func (h *RunsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	events, ok := h.runs.(RunEvents)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "State stream unavailable", "")
		return
	}

	stream, streaming := newSSEWriter(w)
	if !streaming {
		writeProblem(w, http.StatusInternalServerError, "Streaming not supported", "")
		return
	}

	// With no resume point, the stream starts from now: the initial state comes from the
	// list, which the interface has already loaded. Replaying the whole history would double
	// that work for nothing.
	since := time.Now()
	if resume := r.Header.Get("Last-Event-ID"); resume != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, resume); err == nil {
			since = parsed
		}
	}

	ticker := time.NewTicker(runPollInterval)
	defer ticker.Stop()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		updated, latest, err := events.RunsUpdatedSince(r.Context(), since)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			_ = stream.event("error", "", "cannot read the runs")
			return
		}
		since = latest

		for _, run := range updated {
			payload, err := json.Marshal(toRunPayload(run))
			if err != nil {
				continue
			}
			if err := stream.event("run", latest.Format(time.RFC3339Nano), string(payload)); err != nil {
				return
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := stream.comment("battement"); err != nil {
				return
			}
		case <-ticker.C:
		}
	}
}

func toRunPayload(run store.RunSummary) runPayload {
	return runPayload{
		ID:          run.ID,
		Name:        run.Name,
		Pipeline:    run.Pipeline,
		Status:      run.Status,
		Reason:      run.Reason,
		StartedAt:   run.StartedAt,
		CompletedAt: run.CompletedAt,
		Observed:    run.Observed,
	}
}
