package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// VersionStore est ce que les routes de versions attendent de la base.
type VersionStore interface {
	ListVersionSummaries(ctx context.Context) ([]store.VersionSummary, error)
	GetVersionSummary(ctx context.Context, id int64) (store.VersionSummary, error)
	ListVersionIssues(ctx context.Context, versionID int64) ([]store.VersionIssue, error)
	ListVersionReleases(ctx context.Context, versionID int64) ([]store.VersionRelease, error)
	GetSyncState(ctx context.Context, source string) (store.SyncState, error)
}

// VersionsHandler sert les versions Redmine et leur avancement.
type VersionsHandler struct {
	store VersionStore
	// redmineURL sert à construire les liens sortants. Vide, les liens sont omis
	// plutôt que rendus cassés.
	redmineURL string
}

func NewVersionsHandler(versions VersionStore, redmineURL string) *VersionsHandler {
	return &VersionsHandler{store: versions, redmineURL: redmineURL}
}

// completionVw porte les deux mesures de complétion (doc 04 §4.5).
//
// Les deux sont transmises parce qu'elles ne disent pas la même chose : par nombre de
// demandes, lisible mais une grosse demande vaut une petite ; pondérée par
// `done_ratio`, plus fine mais tributaire de la rigueur de saisie dans Redmine.
//
// Le calcul est fait ici et non dans le navigateur : c'est une règle métier (doc 04),
// et la dupliquer côté client la ferait diverger — la même erreur que les seuils de
// santé, déjà corrigée.
type completionVw struct {
	// Counted est le dénominateur : les demandes hors `rejected`.
	Counted int `json:"counted"`
	Done    int `json:"done"`
	// Ratio vaut null quand rien n'est comptable : zéro pour cent laisserait croire
	// qu'une version vide est en retard.
	Ratio         *float64 `json:"ratio"`
	WeightedRatio *float64 `json:"weighted_ratio"`
}

func completionOf(summary store.VersionSummary) completionVw {
	view := completionVw{Counted: summary.Counted, Done: summary.Done}
	if summary.Counted == 0 {
		return view
	}
	ratio := float64(summary.Done) / float64(summary.Counted)
	weighted := float64(summary.WeightedDone) / float64(summary.Counted) / 100
	view.Ratio = &ratio
	view.WeightedRatio = &weighted
	return view
}

type versionVw struct {
	ID         int64      `json:"id"`
	ExternalID string     `json:"external_id,omitempty"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	DueDate    *time.Time `json:"due_date"`
	SyncedAt   time.Time  `json:"synced_at"`
	// Pending marque une version provisoire : une release s'y rattache, mais la
	// synchronisation n'a pas trouvé la version Redmine correspondante.
	Pending    bool         `json:"pending"`
	Completion completionVw `json:"completion"`
	// OpenAnomalies est compté à part et n'entre pas dans la complétion : une version
	// peut être fonctionnellement complète et en porter.
	OpenAnomalies int    `json:"open_anomalies"`
	Releases      int    `json:"releases"`
	URL           string `json:"url,omitempty"`
}

func (h *VersionsHandler) view(summary store.VersionSummary) versionVw {
	view := versionVw{
		ID: summary.ID, Name: summary.Name, Status: summary.Status,
		DueDate: summary.DueDate, SyncedAt: summary.SyncedAt, Pending: summary.Pending,
		Completion: completionOf(summary), OpenAnomalies: summary.OpenAnomalies,
		Releases: summary.Releases,
	}
	// Une provisoire, ou une version d'un projet sans suivi, n'a pas
	// d'identifiant externe : le client n'a alors aucun lien à construire.
	if !summary.Pending && summary.ExternalID != "" {
		view.ExternalID = summary.ExternalID
		if h.redmineURL != "" {
			view.URL = h.redmineURL + "/versions/" + summary.ExternalID
		}
	}
	return view
}

// List rend les versions et leur avancement.
func (h *VersionsHandler) List(w http.ResponseWriter, r *http.Request) {
	summaries, err := h.store.ListVersionSummaries(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des versions impossible", "")
		return
	}

	payload := make([]versionVw, 0, len(summaries))
	for _, summary := range summaries {
		payload = append(payload, h.view(summary))
	}

	// L'âge des données accompagne la liste : Redmine peut ne pas répondre, et un
	// écran qui affiche des chiffres sans dire de quand ils datent est trompeur
	// (doc 08 §8.2).
	state, err := h.store.GetSyncState(r.Context(), "redmine")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'état de synchronisation impossible", "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"versions": payload,
		"sync":     syncStateView(state),
	})
}

type issueVw struct {
	ExternalID string `json:"external_id"`
	Tracker    string `json:"tracker"`
	Subject    string `json:"subject"`
	StatusName string `json:"status_name"`
	Bucket     string `json:"bucket"`
	Priority   string `json:"priority,omitempty"`
	Assignee   string `json:"assignee,omitempty"`
	DoneRatio  int    `json:"done_ratio"`
	IsClosed   bool   `json:"is_closed"`
	Anomaly    bool   `json:"anomaly"`
	URL        string `json:"url,omitempty"`
}

// Get rend le périmètre d'une version.
//
// Les demandes sont réparties en trois seaux et les anomalies listées à part. Le
// regroupement est fait ici parce qu'il suit la règle de doc 04 §4.5, la même qui
// décide de la complétion : la séparer de son calcul les ferait diverger.
func (h *VersionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Identifiant de version invalide", "")
		return
	}

	summary, err := h.store.GetVersionSummary(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrVersionNotFoundByID) {
			writeProblem(w, http.StatusNotFound, "Version introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la version impossible", "")
		return
	}

	issues, err := h.store.ListVersionIssues(r.Context(), id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des demandes impossible", "")
		return
	}

	// Initialisé depuis la liste ordonnée du magasin : un seau ajouté là apparaît
	// ici sans retouche, et un seau oublié rendrait `null` là où l'interface itère.
	buckets := map[string][]issueVw{store.BucketRejected: {}}
	for _, bucket := range store.Buckets {
		buckets[bucket] = []issueVw{}
	}
	anomalies := make([]issueVw, 0)
	for _, issue := range issues {
		view := issueVw{
			ExternalID: issue.ExternalID, Tracker: issue.Tracker, Subject: issue.Subject,
			StatusName: issue.StatusName, Bucket: issue.Bucket, Priority: issue.Priority,
			Assignee: issue.Assignee, DoneRatio: issue.DoneRatio,
			IsClosed: issue.IsClosed, Anomaly: issue.Anomaly,
		}
		if h.redmineURL != "" {
			view.URL = h.redmineURL + "/issues/" + issue.ExternalID
		}

		if bucket, known := buckets[issue.Bucket]; known {
			buckets[issue.Bucket] = append(bucket, view)
		} else {
			// Un seau inconnu ne doit pas faire disparaître la demande de l'écran.
			// La contrainte de base l'interdit, mais une demande invisible serait
			// une perte silencieuse là où une demande mal rangée se voit.
			buckets[store.BucketTodo] = append(buckets[store.BucketTodo], view)
		}

		// Les anomalies sont **aussi** dans leur seau : elles font partie du
		// périmètre. La liste séparée est un second angle de lecture, pas un
		// déplacement — une anomalie retirée du périmètre serait invisible pour qui
		// regarde les colonnes.
		//
		// « Ouverte » se lit sur le seau projeté et non sur le drapeau `is_closed` de
		// Redmine. Les deux divergent — `Testé` y est déclaré non clos alors qu'il est
		// projeté en `done` — et employer deux notions de « clos » dans le même écran
		// faisait apparaître des anomalies terminées dans le bandeau des ouvertes.
		if issue.Anomaly && store.BucketIsOpen(issue.Bucket) {
			anomalies = append(anomalies, view)
		}
	}

	releases, err := h.store.ListVersionReleases(r.Context(), id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des releases impossible", "")
		return
	}
	releasePayload := make([]map[string]any, 0, len(releases))
	for _, item := range releases {
		releasePayload = append(releasePayload, map[string]any{
			"tag": item.Tag, "kind": item.Kind, "state": item.State,
			"image_version": item.ImageVersion, "created_at": item.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":   h.view(summary),
		"buckets":   buckets,
		"anomalies": anomalies,
		"releases":  releasePayload,
	})
}

type syncStateVw struct {
	LastRun *time.Time `json:"last_run"`
	LastOK  *time.Time `json:"last_ok"`
	// LastError est le message de la dernière tentative en échec. Non vide alors que
	// `LastOK` est renseigné signifie « données valides mais vieillissantes », ce qui
	// n'appelle pas la même réaction qu'une absence totale de données.
	LastError string `json:"last_error,omitempty"`
	// Configured dit si la synchronisation a jamais tourné. Faux avec une base vide
	// distingue « pas encore synchronisé » de « synchronisé, rien à afficher ».
	EverRan bool `json:"ever_ran"`
}

func syncStateView(state store.SyncState) syncStateVw {
	return syncStateVw{
		LastRun: state.LastRun, LastOK: state.LastOK,
		LastError: state.LastError, EverRan: state.LastRun != nil,
	}
}
