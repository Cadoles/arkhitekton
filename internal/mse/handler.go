package mse

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/flux"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse/storage"
)

// Store is what the plugin's routes need from the database.
type Store interface {
	host.ProjectResolver
	GetProject(ctx context.Context, id int64) (host.Project, error)
	GetRelease(ctx context.Context, projectID int64, tag string) (host.Release, error)
	ListEnvironments(ctx context.Context, projectID int64) ([]storage.Environment, error)
	ListEnvironmentViews(ctx context.Context, projectID int64) ([]storage.EnvironmentView, error)
	LatestPromotions(ctx context.Context, projectID int64) (map[string]storage.EnvironmentPromotion, error)
	ListEnvironmentHistory(ctx context.Context, projectID int64, environmentID string, limit int) ([]storage.EnvironmentHistory, error)
	DeployedRelease(ctx context.Context, projectID int64, environmentID string) (storage.DeployedRelease, error)
	Enqueue(ctx context.Context, request host.JobRequest) (int64, error)
	RecordPromotion(ctx context.Context, promotion storage.Promotion) error
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// Handler serves promotion and the environments.
type Handler struct {
	store      Store
	catalog    *host.Catalog
	authorizer *host.Authorizer
	// deployer plans a promotion without touching the cluster: it composes a tag and refuses what
	// the SemVer range excludes.
	deployer *flux.Deployer
	logger   *slog.Logger
}

func NewHandler(
	environments Store, pipelines *host.Catalog, authorizer *host.Authorizer,
	deployer *flux.Deployer, logger *slog.Logger,
) *Handler {
	return &Handler{
		store: environments, catalog: pipelines, authorizer: authorizer,
		deployer: deployer, logger: logger,
	}
}

// Routes mounts the plugin's routes. Each requires the module switched on: switched off, they
// answer 404 like a route that does not exist.
func (h *Handler) Routes(mux *http.ServeMux, m *host.Middleware) {
	module := m.RequireModule(PluginID)

	// A release's targets depend on the identity asking: the refusal by role is computed here
	// rather than in the page.
	mux.Handle("GET /projects/{project}/releases/{tag}/targets", host.Chain(http.HandlerFunc(h.Targets),
		m.Authenticate, module, m.RequireIn(host.PermRead)))

	// The environment is in the path and not in the body, so that the authorization — which is per
	// environment — stays decided by the route.
	mux.Handle("POST /projects/{project}/releases/{tag}/promote/{environment}", host.Chain(http.HandlerFunc(h.Promote),
		m.Authenticate, module, m.CSRF, m.RequireDeploy("environment")))

	// Knowing what runs where needs no privilege. Under the project: each project deploys to its
	// own environments.
	mux.Handle("GET /projects/{project}/environments", host.Chain(http.HandlerFunc(h.Environments),
		m.Authenticate, module, m.RequireIn(host.PermRead)))
	mux.Handle("GET /projects/{project}/environments/{environment}", host.Chain(http.HandlerFunc(h.Environment),
		m.Authenticate, module, m.RequireIn(host.PermRead)))
	mux.Handle("GET /projects/{project}/environments/{environment}/history", host.Chain(http.HandlerFunc(h.History),
		m.Authenticate, module, m.RequireIn(host.PermRead)))
}

// promotionTargetVw says, environment by environment, whether the release can go there — and
// if not, why.
//
// The refusal is computed here rather than left to the click: a promotion no ImagePolicy would
// keep produces **no error**. Displaying an active button in that case amounts to promising a
// deployment that will not happen.
type promotionTargetVw struct {
	Environment string `json:"environment"`
	Label       string `json:"label"`
	Tag         string `json:"tag,omitempty"`
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
}

// release reads the release named in the path, or writes the response.
func (h *Handler) release(w http.ResponseWriter, r *http.Request) (host.Release, int64, bool) {
	projectID, ok := host.RequestProject(w, r, h.store)
	if !ok {
		return host.Release{}, 0, false
	}
	item, err := h.store.GetRelease(r.Context(), projectID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, host.ErrReleaseNotFound) {
			host.WriteProblem(w, http.StatusNotFound, "Release not found", "")
			return host.Release{}, 0, false
		}
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return host.Release{}, 0, false
	}
	return item, projectID, true
}

// Targets returns the environments a release can be promoted to.
func (h *Handler) Targets(w http.ResponseWriter, r *http.Request) {
	item, projectID, ok := h.release(w, r)
	if !ok {
		return
	}

	environments, err := h.store.ListEnvironments(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the environments", "")
		return
	}

	identity, _ := host.IdentityFrom(r.Context())
	targets := make([]promotionTargetVw, 0, len(environments))
	for _, environment := range environments {
		if !environment.Visible {
			continue
		}
		target := promotionTargetVw{Environment: environment.ID, Label: environment.Label, Allowed: true}
		switch {
		case item.State != string(host.ReleaseStateReady):
			target.Allowed = false
			target.Reason = "this release's image is not built"
		case !h.authorizer.CanDeploy(identity, projectID, r.PathValue("project"), environment.ID):
			target.Allowed = false
			target.Reason = "your role does not allow promoting to this environment"
		default:
			// The same question as the promotion itself, asked without doing it. `Name` is empty: the
			// reference is the same for every image.
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

	host.WriteJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

// Promote queues the promotion of a release to an environment.
//
// The handler pushes nothing: it queues. The re-tag is done by the `promote-image` task, with
// its own credentials — the dashboard reads the registry, it never writes to it. And the
// queue, alone in knowing the `exclusive-registry` class, avoids two concurrent re-tags on the
// same repository.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	identity, ok := host.IdentityFrom(r.Context())
	if !ok {
		host.WriteProblem(w, http.StatusUnauthorized, "Not authenticated", "")
		return
	}

	item, projectID, ok := h.release(w, r)
	if !ok {
		return
	}
	if item.State != string(host.ReleaseStateReady) {
		host.WriteProblem(w, http.StatusConflict, "Release not promotable",
			"this release's image is not built")
		return
	}

	name := r.PathValue("environment")
	environments, err := h.store.ListEnvironments(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the environments", "")
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
		host.WriteProblem(w, http.StatusNotFound, "Unknown environment", name)
		return
	}

	project, err := h.store.GetProject(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Unknown promoted images", err.Error())
		return
	}
	images := project.Promotable()

	// Once per image: the deployer does not know what a release contains.
	plans := make([]flux.Plan, 0, len(images))
	for _, image := range images {
		plan, err := h.deployer.Plan(r.Context(), flux.Deployment{
			Target: targetOf(environment), Version: item.Tag,
			Artifact: item.ImageVersion, Name: image.Image,
		})
		if err != nil {
			// 409 and not 400: the request is well formed, it is the release/environment combination that
			// makes no sense.
			host.WriteProblem(w, http.StatusConflict, "Cannot promote", err.Error())
			return
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		host.WriteProblem(w, http.StatusUnprocessableEntity, "Cannot promote",
			"no image of this release is promoted")
		return
	}

	if _, found := h.catalog.Find(plans[0].Kind); !found {
		host.WriteProblem(w, http.StatusInternalServerError, "Incomplete catalogue", plans[0].Kind)
		return
	}

	// One run and one promotion row per image: nothing else would say that one of the re-tags
	// failed.
	jobIDs := make([]int64, 0, len(plans))
	for index, plan := range plans {
		jobID, err := h.store.Enqueue(r.Context(), host.JobRequest{
			Kind: plan.Kind, Params: plan.Params, RequestedBy: identity.Subject,
			ReleaseID: &item.ID, Environment: &environment.ID,
		})
		if err != nil {
			host.WriteProblem(w, http.StatusInternalServerError, "Cannot enqueue", "")
			return
		}
		jobIDs = append(jobIDs, jobID)

		// The promotion **requested**, with no digest: the re-tag has not happened. That is what tells
		// "promoted but not deployed yet" from "never promoted".
		if err := h.store.RecordPromotion(r.Context(), storage.Promotion{
			ReleaseID: item.ID, EnvironmentID: environment.ID,
			ImageName: images[index].Image, Tag: plan.Reference, JobID: &jobID,
		}); err != nil {
			host.WriteProblem(w, http.StatusInternalServerError, "Cannot record the promotion", "")
			return
		}
	}

	// The log is a trace, not a transaction: its failure does not cancel a promotion already
	// queued.
	if err := h.store.Audit(r.Context(), identity.Subject, AuditReleasePromote, item.Tag, map[string]any{
		"environment": environment.ID, "tag": plans[0].Reference,
		"from": item.ImageVersion, "job_ids": jobIDs,
	}); err != nil {
		h.logger.Error("journal d'audit", "action", AuditReleasePromote, "target", item.Tag, "err", err)
	}

	host.WriteJSON(w, http.StatusAccepted, map[string]any{
		"job_ids": jobIDs, "environment": environment.ID, "tag": plans[0].Reference,
	})
}

type environmentPayload struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Rank  int    `json:"rank"`
	// DeployerSettings is returned as it is: the interface displays its promotion suffix, which
	// cannot be guessed — Flux knows `qualif` under the name `testing`.
	DeployerSettings map[string]string `json:"deployer_settings,omitempty"`
	// Observable tells "we cannot know" from "we do not know yet".
	Observable bool `json:"observable"`
	// Sensitive triggers retyping the version at confirmation. Decided by the authorizer, alone in
	// knowing what an environment receives.
	Sensitive      bool           `json:"sensitive"`
	PortalVersion  string         `json:"portal_version,omitempty"`
	MigrationState string         `json:"migration_state,omitempty"`
	DeclaredState  string         `json:"declared_state,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	ObservedAt     string         `json:"observed_at,omitempty"`
	CanDeploy      bool           `json:"can_deploy"`
	LastPromotion  *lastPromotion `json:"last_promotion,omitempty"`
}

// lastPromotion is what was **requested** for an environment, to be set against what
// observation finds there.
type lastPromotion struct {
	Tag      string    `json:"tag"`
	Release  string    `json:"release"`
	PushedAt time.Time `json:"pushed_at"`
	JobID    *int64    `json:"job_id,omitempty"`
}

// Environments returns the overview of the environments.
func (h *Handler) Environments(w http.ResponseWriter, r *http.Request) {
	projectID, ok := host.RequestProject(w, r, h.store)
	if !ok {
		return
	}

	views, err := h.store.ListEnvironmentViews(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the environments", "")
		return
	}

	promotions, err := h.store.LatestPromotions(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the promotions", "")
		return
	}

	identity, _ := host.IdentityFrom(r.Context())
	payload := make([]environmentPayload, 0, len(views))
	for _, view := range views {
		entry := environmentPayload{
			ID:               view.Environment.ID,
			Label:            view.Environment.Label,
			Rank:             view.Environment.Rank,
			DeployerSettings: view.Environment.DeployerSettings,
			Observable:       view.Environment.Observable,
			Sensitive:        h.authorizer.Sensitive(r.PathValue("project"), view.Environment.ID),
			CanDeploy:        h.authorizer.CanDeploy(identity, projectID, r.PathValue("project"), view.Environment.ID),
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

	host.WriteJSON(w, http.StatusOK, map[string]any{"environments": payload})
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

// History returns the sequence of an environment's promotions.
//
// The source is what the dashboard requested, and not the history of the commits Flux writes
// in `mse-kustom`: a promotion no ImagePolicy keeps appears nowhere on the Git side although it
// was indeed requested.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	projectID, ok := host.RequestProject(w, r, h.store)
	if !ok {
		return
	}
	entries, err := h.store.ListEnvironmentHistory(r.Context(), projectID, r.PathValue("environment"), 50)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the history", "")
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
	host.WriteJSON(w, http.StatusOK, map[string]any{"history": payload})
}

type deployedPayload struct {
	Tag string `json:"tag"`
	// ImageVersion is what was matched against the observation. Returned because the gap with the
	// tag is the most counter-intuitive part of the arrangement.
	ImageVersion string     `json:"image_version"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	GitRef       string     `json:"git_ref,omitempty"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	PromotedAt   *time.Time `json:"promoted_at,omitempty"`
	PromotedTag  string     `json:"promoted_tag,omitempty"`
	// Ambiguous: several releases carry this image and no promotion settles it. Normal after a
	// re-tag.
	Ambiguous bool `json:"ambiguous"`
}

// Environment returns an environment's detailed state.
//
// The matching with a release is done on `image_version` and never on the tag: what the portal
// reports is the version baked into the image, still the RC's for any final release produced by
// re-tag.
func (h *Handler) Environment(w http.ResponseWriter, r *http.Request) {
	projectID, ok := host.RequestProject(w, r, h.store)
	if !ok {
		return
	}
	name := r.PathValue("environment")

	views, err := h.store.ListEnvironmentViews(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the environment", "")
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
		host.WriteProblem(w, http.StatusNotFound, "Unknown environment", name)
		return
	}

	identity, _ := host.IdentityFrom(r.Context())
	payload := map[string]any{
		"id":                view.Environment.ID,
		"label":             view.Environment.Label,
		"rank":              view.Environment.Rank,
		"deployer_settings": view.Environment.DeployerSettings,
		"namespace":         view.Environment.Namespace,
		"observable":        view.Environment.Observable,
		"sensitive":         h.authorizer.Sensitive(r.PathValue("project"), view.Environment.ID),
		"can_deploy":        h.authorizer.CanDeploy(identity, projectID, r.PathValue("project"), view.Environment.ID),
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

	// The promotion thread, returned here rather than rebuilt in the browser, where the order of
	// the ranks would have to be guessed again.
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

	deployed, err := h.store.DeployedRelease(r.Context(), projectID, name)
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
		// A normal case: the dashboard knows only the releases it created.
		payload["deployed"] = nil
	default:
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot reconcile", "")
		return
	}

	promotions, err := h.store.LatestPromotions(r.Context(), projectID)
	if err != nil {
		host.WriteProblem(w, http.StatusInternalServerError, "Cannot read the promotions", "")
		return
	}
	if promotion, found := promotions[name]; found {
		payload["last_promotion"] = lastPromotion{
			Tag: promotion.Tag, Release: promotion.ReleaseTag,
			PushedAt: promotion.PushedAt, JobID: promotion.JobID,
		}
	}

	host.WriteJSON(w, http.StatusOK, payload)
}

// observedVersion returns the version observed on an environment, or empty.
func observedVersion(view storage.EnvironmentView) string {
	if view.Observation == nil {
		return ""
	}
	return view.Observation.PortalVersion
}
