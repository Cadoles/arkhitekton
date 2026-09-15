package mse

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/flux"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/mse/storage"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// Store est ce que les routes du plugin attendent de la base.
type Store interface {
	dashhttp.ProjectResolver
	GetProject(ctx context.Context, id int64) (store.Project, error)
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	ListEnvironments(ctx context.Context) ([]storage.Environment, error)
	ListEnvironmentViews(ctx context.Context) ([]storage.EnvironmentView, error)
	LatestPromotions(ctx context.Context) (map[string]storage.EnvironmentPromotion, error)
	ListEnvironmentHistory(ctx context.Context, environmentID string, limit int) ([]storage.EnvironmentHistory, error)
	DeployedRelease(ctx context.Context, environmentID string) (storage.DeployedRelease, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	RecordPromotion(ctx context.Context, promotion storage.Promotion) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// Handler sert la promotion et les environnements.
type Handler struct {
	store      Store
	catalog    *catalog.Catalog
	authorizer *auth.Authorizer
	// deployer planifie une promotion sans toucher au cluster : il compose un tag
	// et refuse ce que la plage SemVer exclut (B7).
	deployer *flux.Deployer
	logger   *slog.Logger
}

func NewHandler(
	environments Store, pipelines *catalog.Catalog, authorizer *auth.Authorizer,
	deployer *flux.Deployer, logger *slog.Logger,
) *Handler {
	return &Handler{
		store: environments, catalog: pipelines, authorizer: authorizer,
		deployer: deployer, logger: logger,
	}
}

// Routes monte les routes du plugin. Chacune exige le module allumé : éteint,
// elles répondent 404 comme une route qui n'existe pas.
func (h *Handler) Routes(mux *http.ServeMux, m *dashhttp.Middleware) {
	module := m.RequireModule(plugin.ModuleMSE)

	// Les cibles d'une release dépendent de l'identité qui demande : le refus par
	// rôle se calcule ici plutôt que dans la page.
	mux.Handle("GET /projects/{project}/releases/{tag}/targets", dashhttp.Chain(http.HandlerFunc(h.Targets),
		m.Authenticate, module, m.RequireIn(auth.PermRead)))

	// L'environnement est dans le chemin et non dans le corps, pour que
	// l'autorisation — qui est par environnement — reste décidée par la route.
	mux.Handle("POST /projects/{project}/releases/{tag}/promote/{environment}", dashhttp.Chain(http.HandlerFunc(h.Promote),
		m.Authenticate, module, m.CSRF, m.RequireDeploy("environment")))

	// Savoir ce qui tourne où ne demande aucun privilège.
	mux.Handle("GET /environments", dashhttp.Chain(http.HandlerFunc(h.Environments),
		m.Authenticate, module, m.Require(auth.PermRead)))
	mux.Handle("GET /environments/{environment}", dashhttp.Chain(http.HandlerFunc(h.Environment),
		m.Authenticate, module, m.Require(auth.PermRead)))
	mux.Handle("GET /environments/{environment}/history", dashhttp.Chain(http.HandlerFunc(h.History),
		m.Authenticate, module, m.Require(auth.PermRead)))
}

// promotionTargetVw dit, environnement par environnement, si la release peut y
// aller — et sinon pourquoi.
//
// Le refus est calculé ici plutôt que laissé au clic : une promotion qu'aucune
// ImagePolicy ne retiendrait ne produit **aucune erreur**. Afficher un bouton
// actif dans ce cas revient à promettre un déploiement qui n'aura pas lieu (B7).
type promotionTargetVw struct {
	Environment string `json:"environment"`
	Label       string `json:"label"`
	Tag         string `json:"tag,omitempty"`
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
}

// release lit la release du chemin, ou écrit la réponse.
func (h *Handler) release(w http.ResponseWriter, r *http.Request) (store.Release, int64, bool) {
	projectID, ok := dashhttp.RequestProject(w, r, h.store)
	if !ok {
		return store.Release{}, 0, false
	}
	item, err := h.store.GetRelease(r.Context(), projectID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			dashhttp.WriteProblem(w, http.StatusNotFound, "Release introuvable", "")
			return store.Release{}, 0, false
		}
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return store.Release{}, 0, false
	}
	return item, projectID, true
}

// Targets rend les environnements vers lesquels une release peut être promue.
func (h *Handler) Targets(w http.ResponseWriter, r *http.Request) {
	item, _, ok := h.release(w, r)
	if !ok {
		return
	}

	environments, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}

	identity, _ := dashhttp.IdentityFrom(r.Context())
	targets := make([]promotionTargetVw, 0, len(environments))
	for _, environment := range environments {
		if !environment.Visible {
			continue
		}
		target := promotionTargetVw{Environment: environment.ID, Label: environment.Label, Allowed: true}
		switch {
		case item.State != string(release.StateReady):
			target.Allowed = false
			target.Reason = "l'image de cette release n'est pas construite"
		case !h.authorizer.Can(identity, auth.PermDeploy(environment.ID)):
			target.Allowed = false
			target.Reason = "votre rôle ne permet pas de promouvoir vers cet environnement"
		default:
			// La même question que la promotion elle-même, posée sans la faire.
			// `Name` est vide : la référence est la même pour toutes les images.
			plan, err := h.deployer.Plan(r.Context(), flux.Deployment{
				Target: targetOf(environment), Version: item.Tag, Artifact: item.ImageVersion,
			})
			if err != nil {
				target.Allowed = false
				target.Reason = err.Error()
			} else {
				target.Tag = plan.Reference
			}
		}
		targets = append(targets, target)
	}

	dashhttp.WriteJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

// Promote met en file la promotion d'une release vers un environnement.
//
// Le handler ne pousse rien : il met en file. Le re-tag est fait par la task
// `promote-image`, avec ses propres identifiants — le dashboard lit le registre,
// il n'y écrit jamais. Et la file, seule à connaître la classe
// `exclusive-registry`, évite deux re-tags concurrents sur le même dépôt.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	identity, ok := dashhttp.IdentityFrom(r.Context())
	if !ok {
		dashhttp.WriteProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	item, projectID, ok := h.release(w, r)
	if !ok {
		return
	}
	if item.State != string(release.StateReady) {
		dashhttp.WriteProblem(w, http.StatusConflict, "Release non promouvable",
			"l'image de cette release n'est pas construite")
		return
	}

	name := r.PathValue("environment")
	environments, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}
	var environment storage.Environment
	for _, candidate := range environments {
		if candidate.ID == name {
			environment = candidate
			break
		}
	}
	if environment.ID == "" {
		dashhttp.WriteProblem(w, http.StatusNotFound, "Environnement inconnu", name)
		return
	}

	project, err := h.store.GetProject(r.Context(), projectID)
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Images promues inconnues", err.Error())
		return
	}
	images := project.Promotable()

	// Une fois par image : le déployeur ne sait pas ce qu'une release contient.
	plans := make([]flux.Plan, 0, len(images))
	for _, image := range images {
		plan, err := h.deployer.Plan(r.Context(), flux.Deployment{
			Target: targetOf(environment), Version: item.Tag,
			Artifact: item.ImageVersion, Name: image.Image,
		})
		if err != nil {
			// 409 et non 400 : la requête est bien formée, c'est la combinaison
			// release/environnement qui n'a pas de sens.
			dashhttp.WriteProblem(w, http.StatusConflict, "Promotion impossible", err.Error())
			return
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		dashhttp.WriteProblem(w, http.StatusUnprocessableEntity, "Promotion impossible",
			"aucune image de cette release n'est promue")
		return
	}

	if _, found := h.catalog.Find(plans[0].Kind); !found {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Catalogue incomplet", plans[0].Kind)
		return
	}

	// Une exécution et une ligne de promotion par image : rien d'autre ne dirait
	// qu'un des re-tags a échoué.
	jobIDs := make([]int64, 0, len(plans))
	for index, plan := range plans {
		jobID, err := h.store.Enqueue(r.Context(), store.JobRequest{
			Kind: plan.Kind, Params: plan.Params, RequestedBy: identity.Subject,
			ReleaseID: &item.ID, Environment: &environment.ID,
		})
		if err != nil {
			dashhttp.WriteProblem(w, http.StatusInternalServerError, "Mise en file impossible", "")
			return
		}
		jobIDs = append(jobIDs, jobID)

		// La promotion **demandée**, sans digest : le re-tag n'a pas eu lieu.
		// C'est ce qui distingue « promu mais pas encore déployé » de « jamais
		// promu ».
		if err := h.store.RecordPromotion(r.Context(), storage.Promotion{
			ReleaseID: item.ID, EnvironmentID: environment.ID,
			ImageName: images[index].Image, Tag: plan.Reference, JobID: &jobID,
		}); err != nil {
			dashhttp.WriteProblem(w, http.StatusInternalServerError, "Enregistrement de la promotion impossible", "")
			return
		}
	}

	// Le journal est une trace, pas une transaction : son échec n'annule pas une
	// promotion déjà mise en file.
	if err := h.store.Audit(r.Context(), identity.Subject, store.AuditReleasePromote, item.Tag, map[string]any{
		"environment": environment.ID, "tag": plans[0].Reference,
		"from": item.ImageVersion, "job_ids": jobIDs,
	}); err != nil {
		h.logger.Error("journal d'audit", "action", store.AuditReleasePromote, "target", item.Tag, "err", err)
	}

	dashhttp.WriteJSON(w, http.StatusAccepted, map[string]any{
		"job_ids": jobIDs, "environment": environment.ID, "tag": plans[0].Reference,
	})
}

type environmentPayload struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Rank  int    `json:"rank"`
	// DeployerSettings est rendu tel quel : l'interface en affiche le suffixe de
	// promotion, qui ne se devine pas — Flux connaît `qualif` sous le nom
	// `testing`.
	DeployerSettings map[string]string `json:"deployer_settings,omitempty"`
	// Observable distingue « on ne peut pas savoir » de « on ne sait pas encore »
	// (B8).
	Observable bool `json:"observable"`
	// Sensitive déclenche la ressaisie de la version à la confirmation. Décidé par
	// l'autorisateur, seul à savoir ce qu'un environnement reçoit.
	Sensitive      bool           `json:"sensitive"`
	PortalVersion  string         `json:"portal_version,omitempty"`
	MigrationState string         `json:"migration_state,omitempty"`
	DeclaredState  string         `json:"declared_state,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	ObservedAt     string         `json:"observed_at,omitempty"`
	CanDeploy      bool           `json:"can_deploy"`
	LastPromotion  *lastPromotion `json:"last_promotion,omitempty"`
}

// lastPromotion est ce qui a été **demandé** pour un environnement, à confronter
// à ce que l'observation y constate.
type lastPromotion struct {
	Tag      string    `json:"tag"`
	Release  string    `json:"release"`
	PushedAt time.Time `json:"pushed_at"`
	JobID    *int64    `json:"job_id,omitempty"`
}

// Environments rend la vue d'ensemble des environnements.
func (h *Handler) Environments(w http.ResponseWriter, r *http.Request) {
	views, err := h.store.ListEnvironmentViews(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}

	promotions, err := h.store.LatestPromotions(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des promotions impossible", "")
		return
	}

	identity, _ := dashhttp.IdentityFrom(r.Context())
	payload := make([]environmentPayload, 0, len(views))
	for _, view := range views {
		entry := environmentPayload{
			ID:               view.Environment.ID,
			Label:            view.Environment.Label,
			Rank:             view.Environment.Rank,
			DeployerSettings: view.Environment.DeployerSettings,
			Observable:       view.Environment.Observable,
			Sensitive:        h.authorizer.Sensitive(view.Environment.ID),
			CanDeploy:        h.authorizer.Can(identity, auth.PermDeploy(view.Environment.ID)),
		}
		if view.Observation != nil {
			entry.PortalVersion = view.Observation.PortalVersion
			entry.MigrationState = view.Observation.MigrationState
			entry.DeclaredState = view.Observation.DeclaredState
			entry.Resolution = view.Observation.Resolution
		}
		if view.ObservedAt != nil {
			entry.ObservedAt = *view.ObservedAt
		}
		if promotion, found := promotions[view.Environment.ID]; found {
			entry.LastPromotion = &lastPromotion{
				Tag: promotion.Tag, Release: promotion.ReleaseTag,
				PushedAt: promotion.PushedAt, JobID: promotion.JobID,
			}
		}
		payload = append(payload, entry)
	}

	dashhttp.WriteJSON(w, http.StatusOK, map[string]any{"environments": payload})
}

type historyPayload struct {
	Release      string    `json:"release"`
	ImageVersion string    `json:"image_version"`
	Tag          string    `json:"tag"`
	PushedAt     time.Time `json:"pushed_at"`
	JobID        *int64    `json:"job_id,omitempty"`
	JobState     string    `json:"job_state,omitempty"`
	RequestedBy  string    `json:"requested_by,omitempty"`
}

// History rend la suite des promotions d'un environnement.
//
// La source est ce que le dashboard a demandé, et non l'historique des commits
// que Flux écrit dans `mse-kustom` : une promotion qu'aucune ImagePolicy ne
// retient n'apparaît nulle part côté Git alors qu'elle a bien été demandée.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	entries, err := h.store.ListEnvironmentHistory(r.Context(), r.PathValue("environment"), 50)
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture de l'historique impossible", "")
		return
	}

	payload := make([]historyPayload, 0, len(entries))
	for _, entry := range entries {
		payload = append(payload, historyPayload{
			Release: entry.ReleaseTag, ImageVersion: entry.ImageVersion, Tag: entry.Tag,
			PushedAt: entry.PushedAt, JobID: entry.JobID,
			JobState: entry.JobState, RequestedBy: entry.RequestedBy,
		})
	}
	dashhttp.WriteJSON(w, http.StatusOK, map[string]any{"history": payload})
}

type deployedPayload struct {
	Tag string `json:"tag"`
	// ImageVersion est ce qui a été rapproché de l'observation. Rendue parce que
	// l'écart avec le tag est la chose la plus contre-intuitive du dispositif.
	ImageVersion string     `json:"image_version"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	GitRef       string     `json:"git_ref,omitempty"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	PromotedAt   *time.Time `json:"promoted_at,omitempty"`
	PromotedTag  string     `json:"promoted_tag,omitempty"`
	// Ambiguous : plusieurs releases portent cette image et aucune promotion ne
	// tranche. Normal après un re-tag.
	Ambiguous bool `json:"ambiguous"`
}

// Environment rend l'état détaillé d'un environnement.
//
// Le rapprochement avec une release se fait sur `image_version` et jamais sur le
// tag : ce que le portail rapporte est la version bakée dans l'image, restée
// celle de la RC pour toute release finale produite par re-tag (B8, B17).
func (h *Handler) Environment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("environment")

	views, err := h.store.ListEnvironmentViews(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture de l'environnement impossible", "")
		return
	}

	var view storage.EnvironmentView
	for _, candidate := range views {
		if candidate.Environment.ID == name {
			view = candidate
			break
		}
	}
	if view.Environment.ID == "" {
		dashhttp.WriteProblem(w, http.StatusNotFound, "Environnement inconnu", name)
		return
	}

	identity, _ := dashhttp.IdentityFrom(r.Context())
	payload := map[string]any{
		"id":                view.Environment.ID,
		"label":             view.Environment.Label,
		"rank":              view.Environment.Rank,
		"deployer_settings": view.Environment.DeployerSettings,
		"namespace":         view.Environment.Namespace,
		"observable":        view.Environment.Observable,
		"sensitive":         h.authorizer.Sensitive(view.Environment.ID),
		"can_deploy":        h.authorizer.Can(identity, auth.PermDeploy(view.Environment.ID)),
	}

	if view.Observation != nil {
		payload["portal_version"] = view.Observation.PortalVersion
		payload["migration_state"] = view.Observation.MigrationState
		payload["declared_state"] = view.Observation.DeclaredState
		payload["resolution"] = view.Observation.Resolution
	}
	if view.ObservedAt != nil {
		payload["observed_at"] = *view.ObservedAt
	}

	// Le fil de promotion, rendu ici plutôt que reconstruit côté navigateur, où
	// l'ordre des rangs devrait être redeviné.
	flow := make([]map[string]any, 0, len(views))
	for _, candidate := range views {
		flow = append(flow, map[string]any{
			"id": candidate.Environment.ID, "label": candidate.Environment.Label,
			"rank":    candidate.Environment.Rank,
			"current": candidate.Environment.ID == name,
			"version": observedVersion(candidate),
		})
	}
	payload["flow"] = flow

	deployed, err := h.store.DeployedRelease(r.Context(), name)
	switch {
	case err == nil:
		payload["deployed"] = deployedPayload{
			Tag: deployed.Tag, ImageVersion: deployed.ImageVersion,
			Kind: deployed.Kind, State: deployed.State, GitRef: deployed.GitRef,
			CreatedBy: deployed.CreatedBy, CreatedAt: deployed.CreatedAt,
			PromotedAt: deployed.PromotedAt, PromotedTag: deployed.PromotedTag,
			Ambiguous: deployed.Ambiguous,
		}
	case errors.Is(err, storage.ErrNoDeployedRelease):
		// Cas normal : le dashboard ne connaît que les releases créées par lui.
		payload["deployed"] = nil
	default:
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Rapprochement impossible", "")
		return
	}

	promotions, err := h.store.LatestPromotions(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des promotions impossible", "")
		return
	}
	if promotion, found := promotions[name]; found {
		payload["last_promotion"] = lastPromotion{
			Tag: promotion.Tag, Release: promotion.ReleaseTag,
			PushedAt: promotion.PushedAt, JobID: promotion.JobID,
		}
	}

	dashhttp.WriteJSON(w, http.StatusOK, payload)
}

// observedVersion rend la version constatée sur un environnement, ou vide.
func observedVersion(view storage.EnvironmentView) string {
	if view.Observation == nil {
		return ""
	}
	return view.Observation.PortalVersion
}
