package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/version"
)

// VersionStore est ce que les routes de versions attendent de la base.
type VersionStore interface {
	ListVersionSummaries(ctx context.Context, projectID int64) ([]store.VersionSummary, error)
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	GetVersionSummary(ctx context.Context, id int64) (store.VersionSummary, error)
	ListVersionIssues(ctx context.Context, versionID int64) ([]store.VersionIssue, error)
	ListVersionReleases(ctx context.Context, versionID int64) ([]store.VersionRelease, error)
	GetSyncState(ctx context.Context, source string) (store.SyncState, error)
	// EnsureVersion crée la version d'un projet, ou rend celle qui porte déjà ce
	// nom. Idempotente par nom, ce qui est le bon comportement pour une
	// déclaration manuelle : demander une version qui existe doit y mener.
	EnsureVersion(ctx context.Context, projectID int64, name string) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	// GetProject sert au tag proposé : c'est le projet qui déclare son schéma de
	// version et son cycle, donc ce qu'un formulaire doit proposer.
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// VersionsHandler sert les versions du suivi et leur avancement.
type VersionsHandler struct {
	store VersionStore
	// trackerURL sert à construire les liens sortants. Vide, les liens sont omis
	// plutôt que rendus cassés.
	trackerURL string
	// names normalise les noms de version. Le même normaliseur que le magasin :
	// c'est lui qui décide ce qu'est la ligne de version d'un nom comme
	// `MSE 3.1.0`, et l'interface en portait une copie approximative.
	names release.Normalizer
}

func NewVersionsHandler(versions VersionStore, trackerURL string) *VersionsHandler {
	return &VersionsHandler{store: versions, trackerURL: trackerURL}
}

// WithNormalizer déclare la convention de nommage des versions.
func (h *VersionsHandler) WithNormalizer(names release.Normalizer) *VersionsHandler {
	h.names = names
	return h
}

// completionVw porte les deux mesures de complétion (doc 04 §4.5).
//
// Les deux sont transmises parce qu'elles ne disent pas la même chose : par nombre de
// demandes, lisible mais une grosse demande vaut une petite ; pondérée par
// `done_ratio`, plus fine mais tributaire de la rigueur de saisie dans le suivi.
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
	// synchronisation n'a pas trouvé la version correspondante dans le suivi.
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
		if h.trackerURL != "" {
			view.URL = h.trackerURL + "/versions/" + summary.ExternalID
		}
	}
	return view
}

// List rend les versions et leur avancement.
func (h *VersionsHandler) List(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	summaries, err := h.store.ListVersionSummaries(r.Context(), currentID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des versions impossible", "")
		return
	}

	payload := make([]versionVw, 0, len(summaries))
	for _, summary := range summaries {
		payload = append(payload, h.view(summary))
	}

	// L'âge des données accompagne la liste : le suivi peut ne pas répondre, et un
	// écran qui affiche des chiffres sans dire de quand ils datent est trompeur
	// (doc 08 §8.2).
	state, err := h.store.GetSyncState(r.Context(), "redmine")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'état de synchronisation impossible", "")
		return
	}

	tracked := false
	if project, err := h.store.GetProject(r.Context(), currentID); err == nil {
		tracked = strings.TrimSpace(project.TrackerProject) != ""
	}

	sync := syncStateView(state)
	sync.Tracked = tracked

	writeJSON(w, http.StatusOK, map[string]any{
		"versions": payload,
		"sync":     sync,
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
		if h.trackerURL != "" {
			view.URL = h.trackerURL + "/issues/" + issue.ExternalID
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
		// Le tag proposé au formulaire de création, calculé **ici**.
		//
		// L'interface le composait elle-même : elle retirait les préfixes `v` et
		// `MSE ` — devenus configurables au chantier 5 — et suffixait `-rc<N>`,
		// la convention du MSE. Deux règles que le serveur possède, recopiées
		// dans un formulaire, et qui n'avaient plus aucune raison d'y être
		// justes.
		"suggested_tags": h.suggestedTags(r.Context(), summary, releases),
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
	// Tracked dit si le projet **déclare** un outil de suivi.
	//
	// Sans lui, l'interface ne pouvait pas distinguer « le suivi n'a pas encore
	// tourné » de « ce projet n'en a pas », et affichait « Aucune version
	// déclarée dans le projet Redmine » à un projet qui n'a jamais eu de
	// Redmine. C'est exactement le trou que le jalon de vérification interdit :
	// l'interface doit **le dire proprement** (doc 12 §12.9).
	Tracked bool `json:"tracked"`
}

func syncStateView(state store.SyncState) syncStateVw {
	return syncStateVw{
		LastRun: state.LastRun, LastOK: state.LastOK,
		LastError: state.LastError, EverRan: state.LastRun != nil,
	}
}

// suggestedTags propose un tag par nature déclarée.
//
// Vide quand le schéma ne sait pas répondre — un projet opaque n'a aucune
// « suivante » —, et l'interface propose alors une saisie libre plutôt qu'un
// numéro inventé.
func (h *VersionsHandler) suggestedTags(
	ctx context.Context, summary store.VersionSummary, releases []store.VersionRelease,
) map[string]string {
	project, err := h.store.GetProject(ctx, summary.ProjectID)
	if err != nil {
		return nil
	}
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		return nil
	}

	// La ligne de version, normalisée par la convention du projet : `MSE 3.1.0`
	// et `v3.1.0` désignent la même, et c'est le normaliseur qui le sait.
	base := h.names.Normalize(summary.Name)

	existing := make([]string, 0, len(releases))
	for _, item := range releases {
		existing = append(existing, item.Tag)
	}

	suggestions := map[string]string{}
	for _, spec := range project.ReleaseKinds {
		switch spec.Tag {
		case release.TagPrereleaseRequired:
			if next, err := scheme.NextPrerelease(base, existing); err == nil {
				suggestions[spec.ID] = next
			}
		default:
			// Les natures qui interdisent ou n'exigent rien reprennent la ligne
			// telle quelle : c'est ce qu'une finale porte, et le seul choix qui
			// n'invente rien pour les autres.
			if _, err := scheme.Parse(base); err == nil {
				suggestions[spec.ID] = base
			}
		}
	}
	return suggestions
}

// Create déclare une version à la main.
//
// # Pourquoi cette route existe
//
// Un projet **sans suivi** n'avait aucun moyen de démarrer. Ses versions
// naissaient de `EnsureVersion` à la création d'une release ; mais le formulaire
// de création vit sur l'écran **d'une version**, qui n'existait pas. Écran vide,
// aucun bouton, aucune issue — le jalon de vérification bloqué à sa première
// étape (doc 12 §12.9).
//
// # Pourquoi elle refuse sur un projet suivi
//
// Les versions d'un projet suivi appartiennent à son outil : c'est lui qui fait
// autorité, et le dashboard n'y écrit jamais. Une version créée ici serait une
// provisoire que la synchronisation chercherait à adopter, sans jamais y
// parvenir — elle resterait signalée « en attente » indéfiniment, ce qui est
// exactement l'anomalie que `PendingVersions` existe pour faire remonter.
func (h *VersionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	project, err := requestProjectRecord(w, r, h.store)
	if err != nil {
		return
	}
	if strings.TrimSpace(project.TrackerProject) != "" {
		writeProblem(w, http.StatusConflict, "Projet suivi",
			"les versions de ce projet viennent de son outil de suivi, qui en fait "+
				"autorité: en déclarer une ici resterait en attente d'une adoption "+
				"qui n'arriverait jamais")
		return
	}

	var request struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	name := strings.TrimSpace(request.Name)
	if name == "" {
		writeProblem(w, http.StatusBadRequest, "Nom de version vide",
			"une version se désigne par son numéro, par exemple 1.8.0")
		return
	}

	// La forme est vérifiée contre le schéma du projet. Un schéma opaque accepte
	// tout, ce qui est sa réponse : refuser ici imposerait une convention que le
	// projet n'a pas déclarée.
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Schéma de version illisible", "")
		return
	}
	if _, err := scheme.Parse(h.names.Normalize(name)); err != nil {
		writeProblem(w, http.StatusBadRequest, "Nom de version invalide", err.Error())
		return
	}

	id, err := h.store.EnsureVersion(r.Context(), project.ID, name)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Création impossible", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditVersionDeclare, name,
		map[string]any{"projet": project.Slug, "id": id})

	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name})
}
