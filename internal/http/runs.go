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

// RunDetails est ce que le handler attend pour servir un run.
type RunDetails interface {
	GetRun(ctx context.Context, name string) (store.RunDetail, error)
}

// Runs décrit ce que le handler attend de la base.
type Runs interface {
	ListRuns(ctx context.Context, limit int) ([]store.RunSummary, error)
}

// RunsHandler expose les runs observés.
type RunsHandler struct {
	runs Runs
	// dashboardURL est la racine du dashboard Tekton. Vide, le lien est omis
	// plutôt que rendu mort : un lien qui mène nulle part coûte plus qu'il ne
	// rapporte.
	dashboardURL string
	// directory traduit les sujets OIDC en noms. Nil-safe : sans lui le sujet
	// s'affiche tel quel, ce qui reste identifiable.
	directory DirectoryReader
	logger    *slog.Logger
}

func NewRunsHandler(runs Runs) *RunsHandler {
	return &RunsHandler{runs: runs}
}

// WithTektonDashboard branche le lien vers l'exécution telle que Tekton la voit.
//
// Le dashboard de ce projet montre ce qu'il a retenu d'un run : statut, étapes,
// journaux collectés. Celui de Tekton montre l'objet lui-même — ses paramètres
// résolus, ses workspaces, ses résultats. Quand les deux divergent, c'est le
// second qui tranche, et il faut pouvoir y aller sans reconstruire l'URL à la
// main.
func (h *RunsHandler) WithTektonDashboard(url string) *RunsHandler {
	h.dashboardURL = strings.TrimRight(url, "/")
	return h
}

// WithDirectory branche la traduction des sujets en noms.
func (h *RunsHandler) WithDirectory(directory DirectoryReader, logger *slog.Logger) *RunsHandler {
	h.directory = directory
	h.logger = logger
	return h
}

// dashboardLink compose l'URL de l'exécution dans le dashboard Tekton.
//
// La forme `#/namespaces/<ns>/pipelineruns/<nom>` est celle du routeur du
// dashboard Tekton. Un namespace vide donnerait une URL qui s'ouvre sur une page
// d'erreur : mieux vaut alors ne pas proposer le lien.
func (h *RunsHandler) dashboardLink(run store.RunSummary) string {
	if h.dashboardURL == "" || run.Namespace == "" {
		return ""
	}
	return h.dashboardURL + "/#/namespaces/" + url.PathEscape(run.Namespace) +
		"/pipelineruns/" + url.PathEscape(run.Name)
}

// runPayload est la représentation transmise au frontend.
//
// Un type dédié plutôt que la structure de `store` : le contrat d'API ne doit pas
// suivre les évolutions du schéma, et les noms passent en snake_case, convention
// du reste de l'API.
type runPayload struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Pipeline    string     `json:"pipeline"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Observed distingue un run que le dashboard a lancé d'un run qu'il
	// constate : l'interface n'offre ni annulation ni relance sur le second.
	Observed bool `json:"observed"`

	// Origin dit d'où vient l'exécution : la release qu'elle sert, ou à défaut
	// la personne qui l'a demandée. Les deux champs sont omis quand ils sont
	// vides — un run observé n'a ni l'un ni l'autre, et afficher « — » à leur
	// place ferait croire à une donnée perdue.
	ReleaseTag  string `json:"release_tag,omitempty"`
	TriggeredBy string `json:"triggered_by,omitempty"`
	// DashboardURL pointe vers l'objet dans le dashboard Tekton.
	DashboardURL string `json:"dashboard_url,omitempty"`
}

// List rend les runs les plus récents.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
		// Une limite illisible est ignorée plutôt que refusée : le défaut est
		// sûr, et un 400 sur un paramètre d'affichage n'apprend rien.
	}

	runs, err := h.runs.ListRuns(r.Context(), limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des exécutions impossible", "")
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
	// HasLog dit s'il y a quelque chose à lire ; Sealed, s'il est inutile de
	// continuer à suivre.
	HasLog bool `json:"has_log"`
	Sealed bool `json:"log_sealed"`
}

// Get rend un run et ses tasks.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	details, ok := h.runs.(RunDetails)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "Détail indisponible", "")
		return
	}

	detail, err := details.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			// 404 et non 500 : un run purgé par les CronJobs de nettoyage est un
			// cas normal, et l'interface doit pouvoir le dire.
			writeProblem(w, http.StatusNotFound, "Exécution introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'exécution impossible", "")
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

// mergeTasks compose la liste des tâches d'un run : celles qui ont démarré, et
// celles que le pipeline déclare sans les avoir encore lancées.
//
// Tekton ne crée un TaskRun qu'au démarrage d'une tâche. Sans ce complément, un
// pipeline de huit étapes en montrait deux au début, et rien ne disait ce qu'il
// restait à faire — ni combien de temps il fallait encore attendre.
//
// L'ordre est celui du graphe, donné par la déclaration, et non celui des
// démarrages : une tâche non démarrée n'a pas de date, et la trier par ce
// qu'elle n'a pas l'aurait rejetée en fin de liste quelle que soit sa place
// réelle. Les tâches observées qui ne figurent pas dans la déclaration —
// pipeline modifié entre-temps, run projeté avant l'enregistrement des
// déclarations — sont conservées en tête : les taire perdrait une exécution
// réelle au profit d'une définition.
func mergeTasks(detail store.RunDetail) []taskPayload {
	// Le rapprochement se fait sur le nom de la **tâche du pipeline**, et c'est
	// tout le sujet : une tâche observée porte deux noms — celui de son TaskRun,
	// `mse-…-git-clone`, et celui de la tâche qu'elle exécute, `git-clone`. Seul
	// le second existe dans la déclaration. Les confondre affichait la même étape
	// deux fois : une fois exécutée sous son nom technique, une fois « en
	// attente » sous son libellé.
	//
	// Le champ qui le porte s'appelle `DisplayName` par accident d'histoire : il
	// reçoit le label `tekton.dev/pipelineTask`, qui est une identité, pas un
	// libellé. Le vrai libellé humain ne vit que dans la déclaration.
	observed := make(map[string]store.TaskSummary, len(detail.Tasks))
	for _, task := range detail.Tasks {
		observed[task.DisplayName] = task
	}

	tasks := make([]taskPayload, 0, len(detail.Tasks)+len(detail.Declared))

	// Ce qui a tourné sans être déclaré passe d'abord, dans son ordre d'origine.
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
			// Le libellé vient de la déclaration : « Clone mse » plutôt que
			// `git-clone`. Une tâche ne doit pas changer de nom en démarrant.
			tasks = append(tasks, toTaskPayload(started, task.DisplayName))
			continue
		}
		// Pas encore démarrée. `pending` est le même vocabulaire que celui d'un
		// run qui n'a pas commencé : l'interface sait déjà le rendre.
		tasks = append(tasks, taskPayload{
			Name: task.Name, DisplayName: taskLabel(task.DisplayName, task.Name),
			Status: "pending",
		})
	}
	return tasks
}

// taskLabel rend le libellé humain, ou le nom technique à défaut.
func taskLabel(displayName, fallback string) string {
	if displayName != "" {
		return displayName
	}
	return fallback
}

// toTaskPayload traduit une tâche observée.
//
// `label` est le libellé de la déclaration, vide quand la tâche n'y figure pas.
// `Name` reste le nom du TaskRun : c'est par lui que les journaux se lisent.
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

// actorName traduit un sujet OIDC en nom lisible.
func (h *RunsHandler) actorName(ctx context.Context, subject string) string {
	if h.directory == nil {
		return subject
	}
	return resolveActors(ctx, h.directory, h.logger).name(subject)
}

// RunEvents est ce que le flux d'état attend de la base.
type RunEvents interface {
	RunsUpdatedSince(ctx context.Context, since time.Time) ([]store.RunSummary, time.Time, error)
}

// runPollInterval espace deux relectures de la table des runs.
const runPollInterval = 2 * time.Second

// Stream pousse les changements d'état des runs.
//
// Le flux ne transmet que ce qui a changé : renvoyer la liste entière à chaque
// tour ferait retransmettre des centaines de runs figés pour un seul changement,
// et l'interface ne saurait pas lequel a bougé.
//
// L'identifiant de reprise est l'horodatage de la dernière modification vue. Un
// client qui se reconnecte ne redemande donc que la différence — mais le
// mécanisme n'a rien d'une garantie de livraison : ce flux accélère l'affichage,
// il ne le fonde pas. La liste reste la source de vérité, et l'interface s'en
// contente si le flux tombe.
func (h *RunsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	events, ok := h.runs.(RunEvents)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "Flux d'état indisponible", "")
		return
	}

	stream, streaming := newSSEWriter(w)
	if !streaming {
		writeProblem(w, http.StatusInternalServerError, "Flux non supporté", "")
		return
	}

	// Sans reprise, le flux part de maintenant : l'état initial vient de la liste,
	// que l'interface a déjà chargée. Rejouer tout l'historique doublerait ce
	// travail pour rien.
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
			_ = stream.event("error", "", "lecture des exécutions impossible")
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
