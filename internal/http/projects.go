package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/pipelines"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/version"
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
	RequestKeyRotation(ctx context.Context, projectID int64) error
	ActivateDeployKey(ctx context.Context, projectID int64, active bool) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// ProjectProber vérifie qu'un dépôt est atteignable.
//
// Extrait en interface pour que le handler s'éprouve sans forge : ce qui mérite
// d'être testé ici est la traduction d'un échec en constat lisible, pas le client
// HTTP qui le produit.
type ProjectProber interface {
	Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error)
}

// ProjectsHandler expose la déclaration des dépôts.
type ProjectsHandler struct {
	store ProjectStore
	// prober teste l'accès. Nil quand la forge n'est pas configurée : le test
	// répond alors qu'il ne peut pas conclure, ce qui vaut mieux qu'un échec
	// qu'on prendrait pour un défaut d'accès.
	prober ProjectProber
	// deployer sert à refuser un schéma de version qu'il ne saurait pas
	// acheminer. Nil quand aucun déployeur n'est configuré : tout schéma est
	// alors acceptable, puisque rien ne promeut.
	deployer provider.Deployer
	forgeURL string
	// imageRegistry est le registre proposé par défaut à la déclaration. Celui de
	// la configuration : c'est là que publient les projets existants, et le
	// resaisir à chaque déclaration inviterait la faute de frappe.
	imageRegistry string
}

func NewProjectsHandler(projects ProjectStore) *ProjectsHandler {
	return &ProjectsHandler{store: projects}
}

// WithDeployer branche le refus des schémas incompatibles.
func (h *ProjectsHandler) WithDeployer(deployer provider.Deployer) *ProjectsHandler {
	h.deployer = deployer
	return h
}

// WithProbe branche le test d'accès.
func (h *ProjectsHandler) WithProbe(prober ProjectProber) *ProjectsHandler {
	h.prober = prober
	return h
}

// WithForgeURL déclare la forge proposée par défaut à la déclaration.
//
// Remplace `WithDeployKey`, qui portait aussi la moitié publique d'une clé
// **partagée** par tous les projets. Elle n'existe plus : chaque projet a la
// sienne, posée par le worker, et sa présence dans le dépôt est constatée par le
// test d'accès. Une clé unique ajoutée à plusieurs dépôts faisait de sa
// révocation une panne pour tout le monde, et ne disait rien de qui clonait quoi.
func (h *ProjectsHandler) WithForgeURL(forgeURL string) *ProjectsHandler {
	h.forgeURL = strings.TrimRight(forgeURL, "/")
	return h
}

// WithImageRegistry déclare le registre proposé par défaut.
func (h *ProjectsHandler) WithImageRegistry(registry string) *ProjectsHandler {
	h.imageRegistry = strings.Trim(strings.TrimSpace(registry), "/")
	return h
}

type targetPayload struct {
	Stage string `json:"stage"`
	Image string `json:"image"`
	// Dockerfile propre à cette image, quand il diffère de celui du projet.
	Dockerfile string `json:"dockerfile,omitempty"`
	// Promote dit si un re-tag propage cette image. Toujours rendu, jamais
	// omis : un booléen absent se lit `false` côté formulaire, ce qui dirait
	// « non promue » d'une étape sur laquelle personne ne s'est prononcé.
	Promote bool `json:"promote"`
	// Reference est la référence complète, registre compris. Composée par le
	// serveur : l'écran l'affiche pour qu'on la reconnaisse, et la recomposer
	// côté navigateur la ferait diverger de ce que le pipeline recevra.
	Reference string `json:"reference,omitempty"`
}

type projectPayload struct {
	ID           int64  `json:"id"`
	Repository   string `json:"repository"`
	Label        string `json:"label"`
	ForgeURL     string `json:"forge_url"`
	URL          string `json:"url"`
	Dockerfile   string `json:"dockerfile"`
	BuildContext string `json:"build_context"`
	// Slug nomme le projet dans les URL : `/projects/mse/releases/1.8.0`. Rendu
	// et modifiable — ces adresses finissent en favori, et un nom qu'on ne peut
	// pas corriger est un nom qu'on subit.
	Slug          string `json:"slug"`
	ImageRegistry string `json:"image_registry"`
	// RegistrySecret nomme le secret autorisant à pousser vers ce registre. Vide
	// vaut celui du déploiement.
	//
	// **Un nom, jamais un identifiant.** Le secret lui-même est provisionné par
	// le déploiement — par Vault en production —, et le dashboard se contente de
	// le désigner : faire entrer un mot de passe de registre par ce formulaire le
	// ferait transiter par le navigateur et l'API (doc 07 §7.3, doc 12 §12.7).
	RegistrySecret string `json:"registry_secret"`
	// VersionScheme nomme la forme des tags. Un schéma opaque reste utilisable :
	// il perd la suggestion de version suivante et la sélection par plage, et
	// l'écran doit le dire plutôt que de calculer n'importe quoi.
	VersionScheme string `json:"version_scheme"`
	// Stack nomme la pile dont la bibliothèque de référence est posée. Vide :
	// le projet apporte ses propres pipelines, ce qui est parfaitement valide —
	// le normatif est le contrat, pas la bibliothèque.
	Stack string `json:"stack"`
	// TrackerProject nomme ce projet dans l'outil de suivi. Vide : aucun suivi,
	// et ses versions se déclarent à la main.
	TrackerProject string          `json:"tracker_project"`
	MultiStage     bool            `json:"multi_stage"`
	Targets        []targetPayload `json:"targets"`
	// CheckedAt absent : aucun test n'a eu lieu. État distinct d'un test qui a
	// échoué, et qui ne doit pas se lire comme lui.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	CheckOK   *bool      `json:"check_ok,omitempty"`
	CheckNote string     `json:"check_note,omitempty"`
	// DeployKey est la moitié **publique** de la clé propre au projet. Vide tant
	// que le worker ne l'a pas posée.
	//
	// La moitié privée n'apparaît jamais ici, ni ailleurs. Elle est écrite une
	// fois dans un Secret et n'en ressort plus (doc 07 §7.3).
	DeployKey          string     `json:"deploy_key,omitempty"`
	DeployKeyCreatedAt *time.Time `json:"deploy_key_created_at,omitempty"`
	// DeployKeyActive dit que la clé ouvre vraiment le dépôt : sa moitié publique
	// figure dans les clés de déploiement de la forge.
	//
	// **Constaté** par le test d'accès, non déclaré par un humain : une
	// confirmation manuelle restait vraie après qu'on avait retiré la clé du
	// dépôt, et les pipelines échouaient alors sur une erreur SSH que rien ne
	// rattachait à l'écran qui affirmait le contraire (B8).
	//
	// Faux, **rien ne construit**. Il n'y a plus de clé partagée sur laquelle
	// retomber : elle faisait de sa révocation une panne pour tous les projets à
	// la fois, et ne disait rien de qui clonait quoi.
	DeployKeyActive bool   `json:"deploy_key_active"`
	CreatedBy       string `json:"created_by"`
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
		"projects":  payload,
		"forge_url": h.forgeURL,
		// Proposé à la déclaration : c'est là que publient les projets existants.
		"image_registry": h.imageRegistry,
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
		targets = append(targets, targetPayload{
			Stage: target.Stage, Image: target.Image,
			Dockerfile: target.Dockerfile, Promote: target.Promoted(),
			Reference: project.Reference(target),
		})
	}

	return projectPayload{
		ID: project.ID, Repository: project.Repository, Label: project.Label,
		ForgeURL: project.ForgeURL, URL: repositoryURL(project.ForgeURL, project.Repository),
		Dockerfile: project.Dockerfile, BuildContext: project.BuildContext,
		Slug:           project.Slug,
		ImageRegistry:  project.ImageRegistry,
		RegistrySecret: project.RegistrySecret,
		VersionScheme:  project.VersionScheme,
		Stack:          project.Stack,
		TrackerProject: project.TrackerProject,
		MultiStage:     project.MultiStage, Targets: targets,
		CheckedAt: project.CheckedAt, CheckOK: project.CheckOK,
		CheckNote: project.CheckNote, CreatedBy: project.CreatedBy,
		DeployKey: project.DeployKeyPublic, DeployKeyCreatedAt: project.DeployKeyCreatedAt,
		DeployKeyActive: project.DeployKeyActive,
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
	Repository   string `json:"repository"`
	Label        string `json:"label"`
	ForgeURL     string `json:"forge_url"`
	Dockerfile   string `json:"dockerfile"`
	BuildContext string `json:"build_context"`
	// Slug nomme le projet dans les URL : `/projects/mse/releases/1.8.0`. Rendu
	// et modifiable — ces adresses finissent en favori, et un nom qu'on ne peut
	// pas corriger est un nom qu'on subit.
	Slug           string `json:"slug"`
	ImageRegistry  string `json:"image_registry"`
	RegistrySecret string `json:"registry_secret"`
	// VersionScheme nomme la forme des tags de ce projet. Vide vaut `semver`.
	VersionScheme string `json:"version_scheme"`
	// Stack nomme la pile dont la bibliothèque de référence sera posée. Vide :
	// le projet apporte ses propres pipelines.
	Stack string `json:"stack"`
	// TrackerProject nomme ce projet dans l'outil de suivi. Vide : aucun suivi,
	// et ses versions se déclarent à la main.
	//
	// **Absent de ce formulaire jusqu'ici**, alors que la colonne existe depuis
	// le lot C : la seule façon de la renseigner était l'amorçage, qui ne passe
	// qu'une fois. Pire, `UpdateProject` l'écrivait quand même — donc à la
	// valeur vide —, et enregistrer le libellé d'un projet effaçait sa
	// déclaration de suivi. La synchronisation s'arrêtait alors sans un mot.
	TrackerProject string          `json:"tracker_project"`
	MultiStage     bool            `json:"multi_stage"`
	Targets        []targetPayload `json:"targets"`
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
	// Les barres de fin sont retirées : composer la référence les redoublerait,
	// et `reg.example.fr/projet//portal` n'est pas la même image que
	// `reg.example.fr/projet/portal` pour un registre.
	request.ImageRegistry = strings.Trim(strings.TrimSpace(request.ImageRegistry), "/")
	if request.ImageRegistry == "" {
		request.ImageRegistry = h.imageRegistry
	}

	// Le couplage schéma × déployeur est refusé **ici**, à la configuration, et
	// non découvert au premier déploiement qui ne part pas.
	//
	// C'est le mode de défaillance que doc 12 §12.4 désigne : Flux sélectionne
	// par plage SemVer, et avec un schéma opaque le tag de promotion serait
	// poussé, accepté par le registre, ignoré par Flux — la promotion paraîtrait
	// réussie et rien ne bougerait.
	request.VersionScheme = strings.TrimSpace(request.VersionScheme)
	scheme, err := version.For(request.VersionScheme)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Schéma de version inconnu", err.Error())
		return request, false
	}
	if h.deployer != nil {
		if err := h.deployer.CheckScheme(scheme); err != nil {
			writeProblem(w, http.StatusBadRequest,
				"Schéma de version incompatible avec le déployeur", err.Error())
			return request, false
		}
	}

	// La pile est refusée à la déclaration si aucune bibliothèque ne la couvre.
	// L'apprendre ici plutôt qu'au premier lancement évite de constater qu'un
	// pipeline est introuvable sans savoir pourquoi — et le message dit quoi
	// faire : un projet dont la pile n'est pas couverte écrit les siens.
	request.Stack = strings.TrimSpace(request.Stack)
	if _, err := pipelines.For(request.Stack); err != nil {
		writeProblem(w, http.StatusBadRequest, "Pile inconnue", err.Error())
		return request, false
	}

	return request, true
}

func targetsOf(request projectRequest) []store.Target {
	targets := make([]store.Target, 0, len(request.Targets))
	for _, target := range request.Targets {
		// Écrit explicitement, y compris quand il vaut vrai : le formulaire a
		// tranché, et laisser l'entrée muette la rendrait indiscernable de celles
		// que la migration a rattrapées.
		promote := target.Promote
		targets = append(targets, store.Target{
			Stage:      strings.TrimSpace(target.Stage),
			Image:      strings.TrimSpace(target.Image),
			Dockerfile: strings.TrimSpace(target.Dockerfile),
			Promote:    &promote,
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
		Slug:           strings.TrimSpace(request.Slug),
		ImageRegistry:  request.ImageRegistry,
		RegistrySecret: strings.TrimSpace(request.RegistrySecret),
		VersionScheme:  strings.TrimSpace(request.VersionScheme),
		Stack:          strings.TrimSpace(request.Stack),
		TrackerProject: strings.TrimSpace(request.TrackerProject),
		MultiStage:     request.MultiStage, Targets: targetsOf(request),
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

	// La clé du projet est posée par le worker, qui seul parle au cluster. Le
	// projet est utilisable immédiatement : en attendant, ses pipelines montent
	// la clé partagée du déploiement.
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// RotateDeployKey demande le remplacement de la clé d'un projet.
//
// Une **demande**, réalisée par le worker. L'API tourne sans jeton de cluster —
// `automountServiceAccountToken: false`, délibérément, parce que c'est la
// surface exposée — et lui en accorder un pour la commodité d'une pose synchrone
// serait un recul de sécurité déguisé en fonctionnalité (doc 12 §12.7).
//
// L'ancienne clé reste valable jusqu'à ce que la nouvelle soit posée : la
// rotation ne casse donc pas les clones en cours, et l'écran dit qu'elle est
// demandée plutôt que faite.
func (h *ProjectsHandler) RotateDeployKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	id, ok := projectID(w, r)
	if !ok {
		return
	}

	if err := h.store.RequestKeyRotation(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Demande impossible", "")
		return
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectSet,
		strconv.FormatInt(id, 10), map[string]any{"deploy_key": "rotation demandée"})

	// 202 et non 200 : la clé n'est pas encore remplacée, et répondre 200
	// laisserait croire qu'on peut déjà la coller dans la forge.
	writeJSON(w, http.StatusAccepted, map[string]any{"requested": true})
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

	// Le dépôt est modifiable, et c'est le cas d'usage principal : les projets
	// amorcés portent celui que la configuration a nommé, et le corriger est
	// exactement ce qu'on vient faire. Le changer efface le constat d'accès —
	// il portait sur ce qu'on ne pilote plus.
	err := h.store.UpdateProject(r.Context(), store.Project{
		ID: id, Repository: request.Repository, Label: request.Label,
		ForgeURL: request.ForgeURL, Dockerfile: request.Dockerfile,
		BuildContext: request.BuildContext, ImageRegistry: request.ImageRegistry,
		RegistrySecret: strings.TrimSpace(request.RegistrySecret),
		VersionScheme:  strings.TrimSpace(request.VersionScheme),
		Stack:          strings.TrimSpace(request.Stack),
		TrackerProject: strings.TrimSpace(request.TrackerProject),
		Slug:           strings.TrimSpace(request.Slug),
		MultiStage:     request.MultiStage, Targets: targetsOf(request),
	})
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet introuvable", "")
			return
		}
		if errors.Is(err, store.ErrProjectExists) {
			writeProblem(w, http.StatusConflict, "Dépôt déjà déclaré", request.Repository)
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

	probe, probeErr := h.prober.Probe(r.Context(), project.Repository, project.DeployKeyPublic)
	note := ""
	if probeErr != nil {
		note = probeErr.Error()
	} else {
		note = "branche par défaut : " + probe.DefaultBranch
	}

	if err := h.store.RecordProjectCheck(r.Context(), id, probeErr == nil, note); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Constat impossible", "")
		return
	}

	// Le sort de la clé, **constaté** et non déclaré.
	//
	// Il était confirmé à la main : quelqu'un collait la moitié publique dans les
	// réglages du dépôt, puis venait le dire. Une intention, donc, avec le défaut
	// des intentions (B8) — elle restait vraie après qu'on avait retiré la clé du
	// dépôt, et les pipelines échouaient sur une erreur SSH que rien ne rattachait
	// à l'écran qui affirmait le contraire.
	//
	// Le silence est respecté : quand la question n'a pas pu être posée, l'état
	// précédent est **laissé tel quel**. L'écraser à faux couperait les
	// constructions d'un projet dont la clé est peut-être en place, pour la seule
	// raison que le jeton n'a pas le droit de lire les clés du dépôt.
	if probe.KeyPresent != nil {
		if err := h.store.ActivateDeployKey(r.Context(), id, *probe.KeyPresent); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Constat de la clé impossible", "")
			return
		}
	}

	_ = h.store.Audit(r.Context(), identity.Subject, store.AuditProjectProbe, project.Repository,
		map[string]any{"ok": probeErr == nil, "cle_presente": probe.KeyPresent})

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   probeErr == nil,
		"note": note,
		// Trois états rendus tels quels : `null` veut dire « la question n'a pas
		// pu être posée », ce que l'écran doit dire autrement qu'une absence.
		"key_present": probe.KeyPresent,
		"key_note":    probe.KeyNote,
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
