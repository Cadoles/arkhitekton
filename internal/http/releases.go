package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// ReleaseStore est ce que les handlers de release attendent de la base.
type ReleaseStore interface {
	EnsureVersion(ctx context.Context, name string) (int64, error)
	CreateRelease(ctx context.Context, release store.Release) (int64, error)
	GetRelease(ctx context.Context, tag string) (store.Release, error)
	ListReleases(ctx context.Context, limit int) ([]store.Release, error)
	RecordPromotion(ctx context.Context, promotion store.Promotion) error
	ListPromotions(ctx context.Context, releaseID int64) ([]store.Promotion, error)
	ListEnvironments(ctx context.Context) ([]store.Environment, error)
	ListEnvironmentViews(ctx context.Context) ([]store.EnvironmentView, error)
	LatestPromotions(ctx context.Context) (map[string]store.EnvironmentPromotion, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
}

// ReleasesHandler expose les releases, leur création et leur promotion.
type ReleasesHandler struct {
	store      ReleaseStore
	catalog    *catalog.Catalog
	authorizer *auth.Authorizer
	// imageName est le dépôt promu. Une seule image aujourd'hui ; la table des
	// promotions en accepte plusieurs par release, ce qui évite d'avoir à migrer
	// le jour où le portail se scinde.
	imageName string
}

func NewReleasesHandler(
	releases ReleaseStore, pipelines *catalog.Catalog,
	authorizer *auth.Authorizer, imageName string,
) *ReleasesHandler {
	if imageName == "" {
		imageName = "portal"
	}
	return &ReleasesHandler{
		store: releases, catalog: pipelines, authorizer: authorizer, imageName: imageName,
	}
}

type releasePayload struct {
	Tag   string `json:"tag"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	// ImageVersion diffère du tag pour toute release finale produite par re-tag :
	// l'image porte la version de la RC validée, `version.txt` compris. C'est
	// cette valeur, et non le tag, qu'il faut comparer à ce que rapporte
	// PortalData (B17).
	ImageVersion string              `json:"image_version"`
	GitRef       string              `json:"git_ref,omitempty"`
	SourceTag    string              `json:"source_tag,omitempty"`
	CreatedBy    string              `json:"created_by"`
	CreatedAt    time.Time           `json:"created_at"`
	ReleasedAt   *time.Time          `json:"released_at,omitempty"`
	Promotions   []promotionPayload  `json:"promotions,omitempty"`
	Targets      []promotionTargetVw `json:"targets,omitempty"`
}

type promotionPayload struct {
	Environment string    `json:"environment"`
	Image       string    `json:"image"`
	Tag         string    `json:"tag"`
	Digest      string    `json:"digest,omitempty"`
	JobID       *int64    `json:"job_id,omitempty"`
	PushedAt    time.Time `json:"pushed_at"`
}

// promotionTargetVw dit, environnement par environnement, si la release peut y
// aller — et sinon pourquoi.
//
// Le refus est calculé ici plutôt que laissé au clic : une promotion qu'aucune
// ImagePolicy ne retiendrait ne produit **aucune erreur**. Le tag part, la task
// réussit, rien ne se déploie. Afficher un bouton actif dans ce cas revient à
// promettre un déploiement qui n'aura pas lieu (B7).
type promotionTargetVw struct {
	Environment string `json:"environment"`
	Label       string `json:"label"`
	Tag         string `json:"tag,omitempty"`
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
}

func toReleasePayload(release store.Release, sourceTag string) releasePayload {
	return releasePayload{
		Tag: release.Tag, Kind: release.Kind, State: release.State,
		ImageVersion: release.ImageVersion, GitRef: release.GitRef, SourceTag: sourceTag,
		CreatedBy: release.CreatedBy, CreatedAt: release.CreatedAt, ReleasedAt: release.ReleasedAt,
	}
}

// List rend les releases les plus récentes.
func (h *ReleasesHandler) List(w http.ResponseWriter, r *http.Request) {
	releases, err := h.store.ListReleases(r.Context(), 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des releases impossible", "")
		return
	}

	payload := make([]releasePayload, 0, len(releases))
	for _, item := range releases {
		payload = append(payload, toReleasePayload(item, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": payload})
}

// Get rend une release, ses promotions et ses cibles possibles.
func (h *ReleasesHandler) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	payload := toReleasePayload(item, "")

	promotions, err := h.store.ListPromotions(r.Context(), item.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des promotions impossible", "")
		return
	}
	for _, promotion := range promotions {
		payload.Promotions = append(payload.Promotions, promotionPayload{
			Environment: promotion.EnvironmentID, Image: promotion.ImageName,
			Tag: promotion.Tag, Digest: promotion.Digest,
			JobID: promotion.JobID, PushedAt: promotion.PushedAt,
		})
	}

	environments, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}
	identity, _ := IdentityFrom(r.Context())
	for _, environment := range environments {
		if !environment.Visible {
			continue
		}
		target := promotionTargetVw{
			Environment: environment.ID, Label: environment.Label, Allowed: true,
		}
		switch {
		case item.State != string(release.StateReady):
			target.Allowed = false
			target.Reason = "l'image de cette release n'est pas construite"
		case !h.authorizer.Can(identity, auth.PermDeploy(environment.ID)):
			target.Allowed = false
			target.Reason = "votre rôle ne permet pas de promouvoir vers cet environnement"
		default:
			tag, err := release.PromotionTag(item.Tag, targetOf(environment), "")
			if err != nil {
				target.Allowed = false
				target.Reason = err.Error()
			} else {
				target.Tag = tag
			}
		}
		payload.Targets = append(payload.Targets, target)
	}

	writeJSON(w, http.StatusOK, payload)
}

func targetOf(environment store.Environment) release.Target {
	return release.Target{
		ID:                environment.ID,
		Suffix:            environment.PromotionSuffix,
		AcceptsPrerelease: environment.AcceptsPrerelease,
	}
}

type createReleaseRequest struct {
	Tag  string `json:"tag"`
	Kind string `json:"kind"`
	// RedmineVersion est le nom de la version visée. Facultatif : il se déduit du
	// tag. Fourni, il est **vérifié** — c'est le seul moyen d'attraper une release
	// rattachée à la mauvaise version avant que la note de release ne compare des
	// périmètres sans rapport (B17).
	RedmineVersion string `json:"redmine_version,omitempty"`
	GitRef         string `json:"git_ref,omitempty"`
	// SourceTag est la pré-release re-taguée. Exigé pour une release finale.
	SourceTag string `json:"source_tag,omitempty"`
	// TestsGreen dit si les résultats de la RC sont au vert. Faux n'interdit pas
	// la release — c'est une décision qui appartient au client — mais elle est
	// signalée.
	TestsGreen bool `json:"tests_green,omitempty"`
}

// Create enregistre une release.
//
// La validation est faite par `internal/release`, paquet pur, et jamais ici :
// l'invariant de version se vérifie sans base ni cluster, et c'est ce qui permet
// de l'éprouver.
func (h *ReleasesHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request createReleaseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	version, err := release.ParseVersion(request.Tag)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Tag invalide", err.Error())
		return
	}

	kind := release.Kind(request.Kind)
	if err := release.CheckKind(version, kind); err != nil {
		writeProblem(w, http.StatusBadRequest, "Nature de release incohérente", err.Error())
		return
	}

	// Livrer une version finale est une décision d'une autre portée que produire
	// une RC : elle engage ce qui part chez l'utilisateur. La route exige
	// `release:create`, la nature exige davantage.
	if kind != release.KindPre && !h.authorizer.Can(identity, auth.PermReleaseFinal) {
		writeProblem(w, http.StatusForbidden,
			"Permission insuffisante", "la création d'une release finale exige le rôle releaser")
		return
	}

	if request.RedmineVersion != "" {
		if err := release.CheckRedmineVersion(version, request.RedmineVersion); err != nil {
			writeProblem(w, http.StatusBadRequest, "Version Redmine incohérente", err.Error())
			return
		}
	}

	record := store.Release{
		Tag: version.Raw, Kind: string(kind), GitRef: request.GitRef,
		CreatedBy: identity.Subject,
	}
	var warnings []string

	if kind == release.KindPre {
		// L'image n'existe pas encore : elle portera le tag lui-même, et c'est
		// `build-image` qui fera passer la release en `ready`.
		record.State = string(release.StateDraft)
		record.ImageVersion = version.Raw
	} else {
		source, err := h.store.GetRelease(r.Context(), request.SourceTag)
		if err != nil {
			if errors.Is(err, store.ErrReleaseNotFound) {
				writeProblem(w, http.StatusBadRequest,
					"Pré-release source introuvable", request.SourceTag)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Lecture de la source impossible", "")
			return
		}

		imageVersion, sourceWarnings, err := release.FinalFromSource(release.Source{
			Tag:          source.Tag,
			ImageVersion: source.ImageVersion,
			State:        release.State(source.State),
			TestsGreen:   request.TestsGreen,
		})
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Re-tag impossible", err.Error())
			return
		}

		// Re-tag : l'image est déjà construite et validée, donc la release est
		// promouvable immédiatement. C'est tout l'intérêt du procédé — ce qui part
		// en production est bit pour bit ce qui a été testé.
		record.State = string(release.StateReady)
		record.ImageVersion = imageVersion
		record.SourceReleaseID = &source.ID
		warnings = sourceWarnings
	}

	versionID, err := h.store.EnsureVersion(r.Context(), version.Base)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Version indisponible", "")
		return
	}
	record.VersionID = versionID

	releaseID, err := h.store.CreateRelease(r.Context(), record)
	if err != nil {
		writeProblem(w, http.StatusConflict, "Création impossible", "cette release existe peut-être déjà")
		return
	}

	response := map[string]any{
		"release": toReleasePayload(record, request.SourceTag), "warnings": warnings,
	}

	// Une release finale doit exister sous son propre tag dans le registre, pas
	// seulement sous celui de sa RC. Sans ce re-tag, `1.8.0` ne désigne rien :
	// aucune ImagePolicy ne peut le retenir, et la release n'est promouvable nulle
	// part alors que son image est prête (B17).
	if record.ImageVersion != record.Tag {
		jobID, err := h.enqueueRetag(r.Context(), releaseID, record, identity.Subject)
		if err != nil {
			// La release est créée : le dire, plutôt que de laisser croire à un
			// échec de création. Le re-tag se relance.
			response["warnings"] = append(warnings,
				"la release est créée mais le re-tag vers son propre tag n'a pas pu être mis en file")
		} else {
			response["retag_job_id"] = jobID
		}
	}

	writeJSON(w, http.StatusCreated, response)
}

// enqueueRetag met en file le re-tag d'une release vers son propre tag.
func (h *ReleasesHandler) enqueueRetag(
	ctx context.Context, releaseID int64, record store.Release, subject string,
) (int64, error) {
	pipeline, found := h.catalog.Find("promote")
	if !found {
		return 0, errors.New("catalogue sans pipeline promote")
	}
	return h.store.Enqueue(ctx, store.JobRequest{
		Kind:       pipeline.Kind,
		QueueClass: pipeline.QueueClass,
		Params: map[string]string{
			"old-tag": record.ImageVersion,
			"new-tag": record.Tag,
		},
		RequestedBy: subject,
		ReleaseID:   &releaseID,
	})
}

// Promote met en file la promotion d'une release vers un environnement.
//
// Le handler ne pousse rien : il met en file. Le re-tag est fait par la task
// `promote-image`, avec ses propres identifiants — le dashboard lit le registre,
// il n'y écrit jamais. Et la file, seule à connaître la classe
// `exclusive-registry`, évite deux re-tags concurrents sur le même dépôt.
func (h *ReleasesHandler) Promote(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	item, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	if item.State != string(release.StateReady) {
		writeProblem(w, http.StatusConflict, "Release non promouvable",
			"l'image de cette release n'est pas construite")
		return
	}

	name := r.PathValue("environment")
	environments, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}
	var environment store.Environment
	for _, candidate := range environments {
		if candidate.ID == name {
			environment = candidate
			break
		}
	}
	if environment.ID == "" {
		writeProblem(w, http.StatusNotFound, "Environnement inconnu", name)
		return
	}

	plan, err := release.PlanPromotion(item.Tag, item.ImageVersion, targetOf(environment), "")
	if err != nil {
		// 409 et non 400 : la requête est bien formée, c'est la combinaison
		// release/environnement qui n'a pas de sens.
		writeProblem(w, http.StatusConflict, "Promotion impossible", err.Error())
		return
	}

	pipeline, found := h.catalog.Find("promote")
	if !found {
		writeProblem(w, http.StatusInternalServerError, "Catalogue incomplet", "promote")
		return
	}

	jobID, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind:       pipeline.Kind,
		QueueClass: pipeline.QueueClass,
		Params: map[string]string{
			"old-tag": plan.OldTag,
			"new-tag": plan.NewTag,
		},
		RequestedBy: identity.Subject,
		ReleaseID:   &item.ID,
		Environment: &environment.ID,
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Mise en file impossible", "")
		return
	}

	// Enregistrer la promotion **demandée**, sans digest : le re-tag n'a pas eu
	// lieu. C'est ce qui permettra de distinguer « promu mais pas encore
	// déployé » de « jamais promu » quand l'observation contredira l'attendu.
	if err := h.store.RecordPromotion(r.Context(), store.Promotion{
		ReleaseID: item.ID, EnvironmentID: environment.ID,
		ImageName: h.imageName, Tag: plan.NewTag, JobID: &jobID,
	}); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Enregistrement de la promotion impossible", "")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":      jobID,
		"environment": environment.ID,
		"tag":         plan.NewTag,
	})
}

type environmentPayload struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Rank   int    `json:"rank"`
	Suffix string `json:"promotion_suffix"`
	// Observable distingue « on ne peut pas savoir » de « on ne sait pas encore ».
	// La production vit dans un cluster indépendant : ni PortalData, ni Deployment,
	// ni statut Flux n'y sont lisibles. Afficher « inconnu » y ferait croire à une
	// panne d'observation (B8).
	Observable     bool           `json:"observable"`
	PortalVersion  string         `json:"portal_version,omitempty"`
	MigrationState string         `json:"migration_state,omitempty"`
	FluxState      string         `json:"flux_state,omitempty"`
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
func (h *ReleasesHandler) Environments(w http.ResponseWriter, r *http.Request) {
	views, err := h.store.ListEnvironmentViews(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}

	promotions, err := h.store.LatestPromotions(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des promotions impossible", "")
		return
	}

	identity, _ := IdentityFrom(r.Context())
	payload := make([]environmentPayload, 0, len(views))
	for _, view := range views {
		entry := environmentPayload{
			ID:         view.Environment.ID,
			Label:      view.Environment.Label,
			Rank:       view.Environment.Rank,
			Suffix:     view.Environment.PromotionSuffix,
			Observable: view.Environment.Observable,
			CanDeploy:  h.authorizer.Can(identity, auth.PermDeploy(view.Environment.ID)),
		}
		if view.Observation != nil {
			entry.PortalVersion = view.Observation.PortalVersion
			entry.MigrationState = view.Observation.MigrationState
			entry.FluxState = view.Observation.FluxState
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

	writeJSON(w, http.StatusOK, map[string]any{"environments": payload})
}
