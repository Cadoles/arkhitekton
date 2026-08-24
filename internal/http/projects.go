package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Les dépôts pilotés par le dashboard.
 *
 * Ils vivaient dans la configuration : en déclarer un de plus demandait de
 * modifier un ConfigMap, donc de reconstruire et de redéployer. Rien ne disait,
 * depuis l'interface, quels dépôts le dashboard croyait piloter — et surtout,
 * rien ne permettait de vérifier qu'il y avait accès. L'écart ne se découvrait
 * qu'au clone, en cours de pipeline, sur un message qui ne le désignait pas.
 *
 * La table décrit ; elle ne pilote pas encore. Les pipelines continuent de
 * recevoir leurs dépôts par configuration, et le raccordement demandera de
 * trancher ce que l'ordonnanceur accepte d'une demande.
 */

// ProjectStore est ce que le handler attend de la base.
type ProjectStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	CreateProject(ctx context.Context, project store.Project) (int64, error)
	UpdateProject(ctx context.Context, project store.Project) error
	DeleteProject(ctx context.Context, id int64) error
	RecordProjectCheck(ctx context.Context, id int64, ok bool, note string) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// ProjectProber vérifie qu'un dépôt est atteignable.
//
// Extrait en interface pour que le handler s'éprouve sans forge : ce qui mérite
// d'être testé ici est la traduction d'un échec en constat lisible, pas le client
// HTTP qui le produit.
type ProjectProber interface {
	Probe(ctx context.Context, repository string) (string, error)
}

// ProjectsHandler expose la déclaration des dépôts.
type ProjectsHandler struct {
	store ProjectStore
	// prober teste l'accès. Nil quand la forge n'est pas configurée : le test
	// répond alors qu'il ne peut pas conclure, ce qui vaut mieux qu'un échec
	// qu'on prendrait pour un défaut d'accès.
	prober ProjectProber
	// deployKey est la **moitié publique** de la clé que les pipelines
	// présentent au clone. Publique, donc affichable : c'est précisément ce
	// qu'on va coller dans les réglages du dépôt.
	//
	// Elle vient de la configuration et non du secret : le dashboard n'a pas
	// `secrets` dans sa RBAC et ne doit pas l'avoir (doc 07 §7.3). Une clé par
	// projet demanderait de les créer, donc ce droit ; une seule clé ajoutée à
	// plusieurs dépôts fait le même travail sans l'exiger.
	deployKey string
	forgeURL  string
}

func NewProjectsHandler(projects ProjectStore) *ProjectsHandler {
	return &ProjectsHandler{store: projects}
}

// WithProbe branche le test d'accès.
func (h *ProjectsHandler) WithProbe(prober ProjectProber) *ProjectsHandler {
	h.prober = prober
	return h
}

// WithDeployKey déclare la clé publique à présenter, et la forge par défaut.
func (h *ProjectsHandler) WithDeployKey(key, forgeURL string) *ProjectsHandler {
	h.deployKey = strings.TrimSpace(key)
	h.forgeURL = strings.TrimRight(forgeURL, "/")
	return h
}

type targetPayload struct {
	Stage string `json:"stage"`
	Image string `json:"image"`
}

type projectPayload struct {
	ID           int64           `json:"id"`
	Repository   string          `json:"repository"`
	Label        string          `json:"label"`
	ForgeURL     string          `json:"forge_url"`
	URL          string          `json:"url"`
	Dockerfile   string          `json:"dockerfile"`
	BuildContext string          `json:"build_context"`
	MultiStage   bool            `json:"multi_stage"`
	Targets      []targetPayload `json:"targets"`
	// CheckedAt absent : aucun test n'a eu lieu. État distinct d'un test qui a
	// échoué, et qui ne doit pas se lire comme lui.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	CheckOK   *bool      `json:"check_ok,omitempty"`
	CheckNote string     `json:"check_note,omitempty"`
	CreatedBy string     `json:"created_by"`
}

// List rend les projets, la clé de déploiement et la forge par défaut.
//
// Les trois ensemble : l'écran n'a pas d'usage de l'un sans les autres, et la
// clé est ce qu'on vient chercher au moment d'en déclarer un.
func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des projets impossible", "")
		return
	}

	payload := make([]projectPayload, 0, len(projects))
	for _, project := range projects {
		payload = append(payload, h.toPayload(project))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"projects": payload,
		// Vide quand la clé n'est pas configurée. L'écran le dit plutôt que
		// d'afficher un champ vide qu'on prendrait pour une clé absente du
		// trousseau.
		"deploy_key": h.deployKey,
		"forge_url":  h.forgeURL,
		// Le test ne prouve que l'accès **du dashboard**, par jeton et en HTTPS.
		// Les pipelines clonent en SSH avec la clé de déploiement, et rien ici ne
		// l'éprouve : le dire évite de lire un test vert comme une garantie de
		// clone.
		"probe_configured": h.prober != nil,
	})
}

func (h *ProjectsHandler) toPayload(project store.Project) projectPayload {
	targets := make([]targetPayload, 0, len(project.Targets))
	for _, target := range project.Targets {
		targets = append(targets, targetPayload{Stage: target.Stage, Image: target.Image})
	}

	return projectPayload{
		ID: project.ID, Repository: project.Repository, Label: project.Label,
		ForgeURL: project.ForgeURL, URL: repositoryURL(project.ForgeURL, project.Repository),
		Dockerfile: project.Dockerfile, BuildContext: project.BuildContext,
		MultiStage: project.MultiStage, Targets: targets,
		CheckedAt: project.CheckedAt, CheckOK: project.CheckOK,
		CheckNote: project.CheckNote, CreatedBy: project.CreatedBy,
	}
}

// repositoryURL compose le lien vers le dépôt. Vide si l'un des deux manque :
// un lien tronqué mène à la forge sans mener au dépôt.
func repositoryURL(forgeURL, repository string) string {
	forge := strings.TrimRight(forgeURL, "/")
	if forge == "" || repository == "" {
		return ""
	}
	return forge + "/" + repository
}

type projectRequest struct {
	Repository   string          `json:"repository"`
	Label        string          `json:"label"`
	ForgeURL     string          `json:"forge_url"`
	Dockerfile   string          `json:"dockerfile"`
	BuildContext string          `json:"build_context"`
	MultiStage   bool            `json:"multi_stage"`
	Targets      []targetPayload `json:"targets"`
}

// repositoryPattern borne la forme d'un dépôt.
//
// `owner/nom`, ce que l'API Gitea attend. Le vérifier ici plutôt qu'au premier
// appel évite un test d'accès qui échouerait sur une URL malformée et se lirait
// comme un défaut de droits.
const repositorySeparator = "/"

func (h *ProjectsHandler) decode(w http.ResponseWriter, r *http.Request) (projectRequest, bool) {
	var request projectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return request, false
	}

	request.Repository = strings.Trim(strings.TrimSpace(request.Repository), "/")
	owner, name, found := strings.Cut(request.Repository, repositorySeparator)
	if !found || owner == "" || name == "" || strings.Contains(name, repositorySeparator) {
		writeProblem(w, http.StatusBadRequest, "Dépôt invalide", "attendu owner/nom")
		return request, false
	}

	request.Label = strings.TrimSpace(request.Label)
	if request.Label == "" {
		// Le nom du dépôt fait un libellé acceptable, et l'exiger ferait buter
		// sur un champ dont la valeur est évidente.
		request.Label = name
	}

	request.ForgeURL = strings.TrimRight(strings.TrimSpace(request.ForgeURL), "/")
	if request.ForgeURL == "" {
		request.ForgeURL = h.forgeURL
	}

	request.BuildContext = strings.TrimSpace(request.BuildContext)
	if request.BuildContext == "" {
		request.BuildContext = "."
	}
	request.Dockerfile = strings.TrimSpace(request.Dockerfile)

	return request, true
}

func targetsOf(request projectRequest) []store.Target {
	targets := make([]store.Target, 0, len(request.Targets))
	for _, target := range request.Targets {
		targets = append(targets, store.Target{
			Stage: strings.TrimSpace(target.Stage),
			Image: strings.TrimSpace(target.Image),
		})
	}
	return targets
}

// Create déclare un dépôt.
func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	request, ok := h.decode(w, r)
	if !ok {
		return
	}

	id, err := h.store.CreateProject(r.Context(), store.Project{
		Repository: request.Repository, Label: request.Label, ForgeURL: request.ForgeURL,
		Dockerfile: request.Dockerfile, BuildContext: request.BuildContext,
		MultiStage: request.MultiStage, Targets: targetsOf(request),
		CreatedBy: identity.Subject,
	})
	if err != nil {
		if errors.Is(err, store.ErrProjectExists) {
			writeProblem(w, http.StatusConflict, "Dépôt déjà déclaré", request.Repository)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Déclaration impossible", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectSet, request.Repository,
		map[string]any{"label": request.Label})

	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// Update remplace la description d'un projet.
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	request, ok := h.decode(w, r)
	if !ok {
		return
	}

	err := h.store.UpdateProject(r.Context(), store.Project{
		ID: id, Label: request.Label, ForgeURL: request.ForgeURL,
		Dockerfile: request.Dockerfile, BuildContext: request.BuildContext,
		MultiStage: request.MultiStage, Targets: targetsOf(request),
	})
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Mise à jour impossible", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectSet, request.Repository,
		map[string]any{"label": request.Label, "id": id})

	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// Delete retire un dépôt de la liste.
func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	// Le dépôt est relu avant d'être retiré : le journal d'audit doit nommer ce
	// qui a disparu, et après la suppression il n'y a plus rien à nommer.
	project, err := h.store.GetProject(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrProjectNotFound) {
		writeProblem(w, http.StatusInternalServerError, "Suppression impossible", "")
		return
	}

	if err := h.store.DeleteProject(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Suppression impossible", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectRemove, project.Repository, nil)

	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// Probe vérifie que le dashboard atteint le dépôt.
//
// Ce que le test prouve, et rien de plus : le dashboard lit ce dépôt par l'API
// Gitea, avec son jeton, en HTTPS. Les pipelines, eux, clonent en SSH avec la clé
// de déploiement, depuis un pod qui n'est pas celui-ci. Un test vert ne garantit
// donc pas qu'un clone aboutira — c'est dit à l'écran, faute de quoi une clé
// oubliée se découvrirait au premier build.
func (h *ProjectsHandler) Probe(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	project, err := h.store.GetProject(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Test impossible", "")
		return
	}

	if h.prober == nil {
		// Ni vert ni rouge : le test n'a pas eu lieu. Le consigner en échec
		// ferait chercher un défaut d'accès là où il n'y a qu'un jeton absent.
		writeProblem(w, http.StatusServiceUnavailable, "Test indisponible",
			"la forge n'est pas configurée : GITEA_URL et GITEA_TOKEN sont requis")
		return
	}

	branch, probeErr := h.prober.Probe(r.Context(), project.Repository)
	note := ""
	if probeErr != nil {
		note = probeErr.Error()
	} else {
		note = "branche par défaut : " + branch
	}

	if err := h.store.RecordProjectCheck(r.Context(), id, probeErr == nil, note); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Constat impossible", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectProbe, project.Repository,
		map[string]any{"ok": probeErr == nil})

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   probeErr == nil,
		"note": note,
	})
}

func projectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
		return 0, false
	}
	return id, true
}

// GiteaProber teste un dépôt par l'API Gitea.
//
// Un client par appel : celui de `internal/gitea` est lié à un dépôt à sa
// construction, et le test porte justement sur un dépôt différent de celui de la
// configuration.
type GiteaProber struct {
	URL     string
	Token   string
	Timeout time.Duration
}

// Probe rend la branche par défaut du dépôt, ou l'erreur qui l'en empêche.
func (p GiteaProber) Probe(ctx context.Context, repository string) (string, error) {
	client, err := gitea.New(gitea.Options{
		URL: p.URL, Token: p.Token, Repository: repository, Timeout: p.Timeout,
	})
	if err != nil {
		return "", fmt.Errorf("client: %w", err)
	}

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	if branch == "" {
		// Un dépôt sans branche par défaut existe mais n'a jamais reçu de
		// commit : le clone d'un pipeline y échouerait, et le dire ici évite de
		// l'apprendre en cours de build.
		return "", errors.New("le dépôt est vide : aucune branche par défaut")
	}
	return branch, nil
}
