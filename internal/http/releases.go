package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	SetManualActions(ctx context.Context, tag, markdown, author string) error
	ListReleaseJobs(ctx context.Context, releaseID int64) ([]store.ReleaseJob, error)
	ListEnvironmentHistory(ctx context.Context, environmentID string, limit int) ([]store.EnvironmentHistory, error)
	DeployedRelease(ctx context.Context, environmentID string) (store.DeployedRelease, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	ListArtifacts(ctx context.Context, releaseID int64) ([]store.Artifact, error)
	GetArtifact(ctx context.Context, id int64) (store.Artifact, error)
	ListEnvironments(ctx context.Context) ([]store.Environment, error)
	ListEnvironmentViews(ctx context.Context) ([]store.EnvironmentView, error)
	LatestPromotions(ctx context.Context) (map[string]store.EnvironmentPromotion, error)
	Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	SetReleaseState(ctx context.Context, id int64, state string) error
	MarkReleased(ctx context.Context, id int64, from string) (bool, error)
}

// ReleasesHandler expose les releases, leur création et leur promotion.
type ReleasesHandler struct {
	store      ReleaseStore
	catalog    *catalog.Catalog
	authorizer *auth.Authorizer
	// images résout les digests. Nil quand le registre n'est pas configuré : le
	// dashboard reste utilisable sans lui, l'écran affiche alors les tags sans
	// leurs digests et le dit.
	images Images
	// artifacts sert le contenu des livrables. Le téléchargement transite par
	// l'api plutôt que par une URL pré-signée : ces objets portent la preuve
	// d'une livraison, et une URL pré-signée est transmissible.
	artifacts Artifacts
	// imageName est le dépôt promu. Une seule image aujourd'hui ; la table des
	// promotions en accepte plusieurs par release, ce qui évite d'avoir à migrer
	// le jour où le portail se scinde.
	imageName string
}

// Images est ce que le handler attend du registre : lire, jamais écrire. Le
// re-tag est fait par la Task `promote-image` avec ses propres identifiants.
type Images interface {
	Digest(ctx context.Context, repository, tag string) (string, error)
}

// Artifacts est ce que le handler attend du stockage objet.
type Artifacts interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Contrat avec l'ordonnanceur. Répétés ici plutôt qu'importés de `worker` :
// l'`api` ne dépend pas du worker, et l'inverse non plus — les deux se parlent
// par la base.
const (
	exportJobKind     = "export"
	exportParamKind   = "export-kind"
	exportParamFormat = "export-format"
)

func NewReleasesHandler(
	releases ReleaseStore, pipelines *catalog.Catalog,
	authorizer *auth.Authorizer, images Images, artifacts Artifacts, imageName string,
) *ReleasesHandler {
	if imageName == "" {
		imageName = "portal"
	}
	return &ReleasesHandler{
		store: releases, catalog: pipelines, authorizer: authorizer,
		images: images, artifacts: artifacts, imageName: imageName,
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
	Jobs         []releaseJobPayload `json:"jobs,omitempty"`
	Images       []imagePayload      `json:"images,omitempty"`
	// ImagesError dit pourquoi les digests manquent. L'absence de digest et
	// l'impossibilité de le lire ne se ressemblent pas : la première signale une
	// image qui n'existe pas, la seconde un registre injoignable ou non
	// configuré. Les confondre ferait chercher une panne de build.
	ImagesError  string          `json:"images_error,omitempty"`
	ManualAction *manualActionVw `json:"manual_actions,omitempty"`
}

type releaseJobPayload struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Environment string     `json:"environment,omitempty"`
	RunName     string     `json:"run_name,omitempty"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type imagePayload struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest,omitempty"`
}

type manualActionVw struct {
	Markdown  string     `json:"markdown"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
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

	jobs, err := h.store.ListReleaseJobs(r.Context(), item.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des exécutions impossible", "")
		return
	}
	for _, job := range jobs {
		payload.Jobs = append(payload.Jobs, releaseJobPayload{
			ID: job.ID, Kind: job.Kind, State: job.State, Environment: job.EnvironmentID,
			RunName: job.RunName, RequestedBy: job.RequestedBy,
			RequestedAt: job.RequestedAt, FinishedAt: job.FinishedAt,
		})
	}

	if item.ManualActions != "" || item.ManualActionsUpdatedAt != nil {
		payload.ManualAction = &manualActionVw{
			Markdown:  item.ManualActions,
			UpdatedBy: item.ManualActionsUpdatedBy,
			UpdatedAt: item.ManualActionsUpdatedAt,
		}
	}

	payload.Images, payload.ImagesError = h.resolveImages(r.Context(), item, promotions)

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

// resolveImages liste les tags de la release et leurs digests.
//
// Le digest est ce qui prouve qu'une promotion n'a pas reconstruit : le tag
// suffixé et le tag d'origine doivent porter le **même** digest. Un écart signale
// une image reconstruite entre-temps, c'est-à-dire une livraison qui n'est plus
// celle qui a été validée (B17).
//
// L'échec de résolution est rendu comme un message et non comme une erreur HTTP :
// un registre injoignable ne doit pas rendre la page de release illisible. Ce qui
// est en base reste affichable.
func (h *ReleasesHandler) resolveImages(
	ctx context.Context, item store.Release, promotions []store.Promotion,
) ([]imagePayload, string) {
	// L'ordre importe : la version d'image d'abord, c'est-à-dire ce qui a été
	// construit, puis le tag de la release, puis les tags de promotion.
	tags := []string{item.ImageVersion}
	if item.Tag != item.ImageVersion {
		tags = append(tags, item.Tag)
	}
	for _, promotion := range promotions {
		tags = append(tags, promotion.Tag)
	}

	images := make([]imagePayload, 0, len(tags))
	for _, tag := range tags {
		images = append(images, imagePayload{Repository: h.imageName, Tag: tag})
	}

	if h.images == nil {
		return images, "Le registre n'est pas configuré : les digests ne peuvent pas être lus."
	}

	var failures int
	for index, image := range images {
		digest, err := h.images.Digest(ctx, image.Repository, image.Tag)
		if err != nil {
			// Un tag absent du registre n'est pas une anomalie de lecture : une
			// promotion mise en file n'a pas encore poussé son tag.
			failures++
			continue
		}
		images[index].Digest = digest
	}
	if failures == len(images) && failures > 0 {
		return images, "Aucun digest n'a pu être lu : le registre est peut-être injoignable."
	}
	return images, ""
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
	} else if kind == release.KindPatch {
		// Un patch se construit comme une pré-release : il corrige une version
		// livrée, donc il y a du code nouveau à compiler. Le distinguer d'une
		// finale — qui, elle, ne fait que re-taguer — est ce qui évite de lui
		// réclamer une RC source qui n'existe pas.
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

	h.audit(r, store.AuditReleaseCreate, record.Tag, map[string]any{
		"kind": record.Kind, "image_version": record.ImageVersion, "source": request.SourceTag,
	})

	response := map[string]any{
		"release": toReleasePayload(record, request.SourceTag), "warnings": warnings,
	}

	// Une release construite enchaîne sur `build-image`. Sans cet enchaînement,
	// elle restait `draft` indéfiniment : le pipeline existait dans le cluster,
	// mais rien ne le déclenchait, et l'écran affichait un brouillon que personne
	// ne savait faire avancer.
	if record.State == string(release.StateDraft) && kind != release.KindFinal {
		jobID, buildErr := h.enqueueBuild(r.Context(), releaseID, record, request.GitRef, identity.Subject)
		switch {
		case buildErr != nil:
			// La release est créée : le dire, plutôt que de laisser croire à un
			// échec de création. La construction se relance depuis la file.
			warnings = append(warnings,
				"la release est créée mais la construction n'a pas pu être mise en file: "+buildErr.Error())
			response["warnings"] = warnings
		default:
			response["build_job_id"] = jobID
			// L'état suit la mise en file et non la fin du pipeline : `building`
			// signifie « construction demandée », ce qui est exactement ce qu'on
			// sait à cet instant.
			if err := h.store.SetReleaseState(r.Context(), releaseID, string(release.StateBuilding)); err != nil {
				warnings = append(warnings,
					"construction mise en file, mais l'état de la release n'a pas pu être avancé")
				response["warnings"] = warnings
			} else {
				record.State = string(release.StateBuilding)
				response["release"] = toReleasePayload(record, request.SourceTag)
			}
		}
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

// Release valide une release et l'autorise à partir en environnement.
//
// C'est la décision humaine du cycle documenté (doc 04 §4.2) : les tests ont
// conclu, quelqu'un tranche. Rien ne l'automatise, et c'est le point — un
// pipeline vert dit que le code fonctionne, pas qu'on veut le livrer.
//
// `release:create:final` et non `release:edit` : valider engage ce qui partira
// chez l'utilisateur, au même titre que produire une release finale.
func (h *ReleasesHandler) Release(w http.ResponseWriter, r *http.Request) {
	if _, ok := IdentityFrom(r.Context()); !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	tag := r.PathValue("tag")
	record, err := h.store.GetRelease(r.Context(), tag)
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", tag)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	// La transition est vérifiée avant l'écriture, et par le paquet qui en porte
	// la table : une validation depuis `building` ou depuis `cancelled` n'a pas de
	// sens, et le message doit dire lequel des deux cas s'applique.
	from := release.State(record.State)
	if !release.CanTransition(from, release.StateReleased) {
		writeProblem(w, http.StatusConflict, "Validation impossible",
			fmt.Sprintf("la release %q est dans l'état %q: seule une release %q se valide",
				tag, record.State, release.StateReady))
		return
	}

	validated, err := h.store.MarkReleased(r.Context(), record.ID, record.State)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Validation impossible", "")
		return
	}
	if !validated {
		// La release a bougé entre la lecture et l'écriture. Un 409 plutôt qu'un
		// succès muet : l'appelant a pris sa décision sur un état qui n'est plus
		// celui de la base.
		writeProblem(w, http.StatusConflict, "Validation impossible",
			"l'état de la release a changé entre-temps, rechargez la page")
		return
	}

	h.audit(r, store.AuditReleaseRelease, tag, map[string]any{
		"from": record.State, "image_version": record.ImageVersion,
	})

	record.State = string(release.StateReleased)
	writeJSON(w, http.StatusOK, map[string]any{
		"release": toReleasePayload(record, ""),
	})
}

// enqueueBuild met en file la construction des images d'une release.
//
// `version` porte le tag SemVer et prime sur `tag-name` : la Task
// `generate-image-tag` s'en sert tel quel plutôt que de le dériver de la branche,
// et le même tag part en `--build-arg VERSION` pour être inscrit dans
// `/app/version.txt`. C'est ce qui rend l'image identifiable par ce qu'elle
// rapporte d'elle-même (B17, P10).
//
// Une référence Git vide est refusée ici plutôt que transmise : le clone
// échouerait en cours de pipeline, plusieurs minutes plus tard, sur un message
// qui ne désignerait pas la cause.
func (h *ReleasesHandler) enqueueBuild(
	ctx context.Context, releaseID int64, record store.Release, gitRef, subject string,
) (int64, error) {
	pipeline, found := h.catalog.Find("build")
	if !found {
		return 0, errors.New("catalogue sans pipeline build")
	}
	if strings.TrimSpace(gitRef) == "" {
		return 0, errors.New("aucune référence Git à construire")
	}

	return h.store.Enqueue(ctx, store.JobRequest{
		Kind:       pipeline.Kind,
		QueueClass: pipeline.QueueClass,
		Params: map[string]string{
			"revision": gitRef,
			"version":  record.Tag,
			"tag-name": record.Tag,
		},
		RequestedBy: subject,
		ReleaseID:   &releaseID,
	})
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

	// Consigné après l'enregistrement : ce qui compte est que le tag ait été
	// demandé, et le tag part dès que le job est pris.
	h.audit(r, store.AuditReleasePromote, item.Tag, map[string]any{
		"environment": environment.ID, "tag": plan.NewTag,
		"from": plan.OldTag, "job_id": jobID,
	})

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
	Observable bool `json:"observable"`
	// Sensitive déclenche la ressaisie de la version à la confirmation. Décidé par
	// l'autorisateur, seul à savoir ce qu'un environnement reçoit — le déduire de
	// l'observabilité côté navigateur traiterait une coïncidence comme une règle.
	Sensitive      bool           `json:"sensitive"`
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
			Sensitive:  h.authorizer.Sensitive(view.Environment.ID),
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

type manualActionsRequest struct {
	Markdown string `json:"markdown"`
}

// UpdateActions enregistre le récit des gestes manuels d'une livraison.
//
// Champ libre et non liste de cases à cocher : les gestes manuels d'une
// livraison ne se répètent pas assez d'une version à l'autre pour qu'une
// checklist les couvre, et une checklist incomplète se remplit quand même — elle
// donnerait l'impression d'un contrôle qui n'a pas eu lieu.
func (h *ReleasesHandler) UpdateActions(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request manualActionsRequest
	// 256 Kio : le texte est libre, mais il finit dans un PDF de livraison.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	tag := r.PathValue("tag")
	if err := h.store.SetManualActions(r.Context(), tag, request.Markdown, identity.Subject); err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	h.audit(r, store.AuditReleaseActions, tag, map[string]any{"length": len(request.Markdown)})

	// La release est relue plutôt que reconstruite : l'horodatage est posé par la
	// base, et le renvoyer depuis une valeur calculée ici les ferait diverger.
	item, err := h.store.GetRelease(r.Context(), tag)
	if err != nil {
		writeJSON(w, http.StatusNoContent, nil)
		return
	}
	writeJSON(w, http.StatusOK, manualActionVw{
		Markdown:  item.ManualActions,
		UpdatedBy: item.ManualActionsUpdatedBy,
		UpdatedAt: item.ManualActionsUpdatedAt,
	})
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
// que Flux écrit dans `mse-kustom`. Les deux diffèrent, et c'est l'écart qui
// intéresse : une promotion qu'aucune ImagePolicy ne retient n'apparaît nulle
// part côté Git alors qu'elle a bien été demandée.
func (h *ReleasesHandler) History(w http.ResponseWriter, r *http.Request) {
	entries, err := h.store.ListEnvironmentHistory(r.Context(), r.PathValue("environment"), 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'historique impossible", "")
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
	writeJSON(w, http.StatusOK, map[string]any{"history": payload})
}

// audit consigne une action sortante, sans jamais faire échouer celle-ci.
//
// Le journal est une trace, pas une transaction : refuser une promotion parce que
// son enregistrement a échoué serait une régression de disponibilité pour un gain
// nul — la promotion, elle, a bien eu lieu. L'échec part au journal applicatif.
func (h *ReleasesHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}

type artifactPayload struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	Format      string    `json:"format"`
	SizeBytes   int64     `json:"size_bytes"`
	GeneratedBy string    `json:"generated_by"`
	GeneratedAt time.Time `json:"generated_at"`
}

// Exports rend les livrables déjà produits pour une release.
func (h *ReleasesHandler) Exports(w http.ResponseWriter, r *http.Request) {
	item, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	artifacts, err := h.store.ListArtifacts(r.Context(), item.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des livrables impossible", "")
		return
	}

	payload := make([]artifactPayload, 0, len(artifacts))
	for _, artifact := range artifacts {
		payload = append(payload, artifactPayload{
			ID: artifact.ID, Kind: artifact.Kind, Format: artifact.Format,
			SizeBytes: artifact.SizeBytes, GeneratedBy: artifact.GeneratedBy,
			GeneratedAt: artifact.GeneratedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": payload})
}

type exportRequest struct {
	Kind   string `json:"kind"`
	Format string `json:"format"`
}

// Export met en file la génération d'un livrable.
//
// Asynchrone bien que la génération soit rapide : elle passe par la file comme
// tout le reste, ce qui lui donne l'ordonnancement, la trace et la visibilité. Un
// export apparaît alors dans la file avec les autres jobs, et son échec se lit au
// même endroit que celui d'un pipeline.
func (h *ReleasesHandler) Export(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request exportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&request); err != nil && err != io.EOF {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}
	if request.Kind == "" {
		request.Kind = store.ArtifactDeliveryNote
	}
	if request.Format == "" {
		request.Format = "pdf"
	}
	if request.Format != "pdf" && request.Format != "json" {
		writeProblem(w, http.StatusBadRequest, "Format inconnu", request.Format)
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

	jobID, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind: exportJobKind,
		// `parallel` : un export ne touche ni le registre ni le cluster, rien ne
		// justifie de le sérialiser avec des pipelines.
		QueueClass: "parallel",
		Params: map[string]string{
			exportParamKind:   request.Kind,
			exportParamFormat: request.Format,
		},
		RequestedBy: identity.Subject,
		ReleaseID:   &item.ID,
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Mise en file impossible", "")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": jobID, "kind": request.Kind, "format": request.Format,
	})
}

// Download sert un livrable.
//
// Le contenu transite par l'api plutôt que par une URL pré-signée : ces objets
// portent la preuve d'une livraison, et une URL pré-signée est transmissible —
// elle échapperait au contrôle de rôle dès le premier copier-coller.
func (h *ReleasesHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("artifact"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Identifiant invalide", "")
		return
	}

	artifact, err := h.store.GetArtifact(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrArtifactNotFound) {
			writeProblem(w, http.StatusNotFound, "Livrable introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du livrable impossible", "")
		return
	}

	item, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Release introuvable", "")
		return
	}
	// Le livrable doit appartenir à la release du chemin : sans ce contrôle, un
	// identifiant deviné donnerait le bon de livraison d'une autre release.
	if artifact.ReleaseID != item.ID {
		writeProblem(w, http.StatusNotFound, "Livrable introuvable", "")
		return
	}

	if h.artifacts == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Stockage indisponible", "")
		return
	}

	content, err := h.artifacts.Get(r.Context(), artifact.Object)
	if err != nil {
		// L'objet est référencé mais absent : la ligne survit à une purge du
		// bucket, et le dire vaut mieux qu'un fichier vide.
		writeProblem(w, http.StatusNotFound, "Contenu du livrable introuvable", "")
		return
	}
	defer content.Close()

	w.Header().Set("Content-Type", artifactContentType(artifact.Format))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s.%s"`, artifact.Kind, item.Tag, artifact.Format))
	// Un livrable est immuable : chaque génération écrit une nouvelle ligne.
	w.Header().Set("Cache-Control", "private, max-age=300")

	if _, err := io.Copy(w, content); err != nil {
		// L'en-tête est déjà parti : rien à annoncer au client, seulement à
		// consigner.
		slog.Default().Error("téléchargement interrompu", "artefact", artifact.ID, "err", err)
	}
}

func artifactContentType(format string) string {
	switch format {
	case "pdf":
		return "application/pdf"
	case "json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
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
	// tranche. Normal après un re-tag, et le taire ferait afficher l'une pour
	// l'autre.
	Ambiguous bool `json:"ambiguous"`
}

// Environment rend l'état détaillé d'un environnement.
//
// L'écran est une jointure : l'environnement porte une version, la release porte
// le contenu. Le rapprochement se fait sur `image_version` et jamais sur le tag —
// ce que le portail rapporte est la version bakée dans l'image, restée celle de
// la RC pour toute release finale produite par re-tag (B8, B17).
func (h *ReleasesHandler) Environment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("environment")

	views, err := h.store.ListEnvironmentViews(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'environnement impossible", "")
		return
	}

	var view store.EnvironmentView
	for _, candidate := range views {
		if candidate.Environment.ID == name {
			view = candidate
			break
		}
	}
	if view.Environment.ID == "" {
		writeProblem(w, http.StatusNotFound, "Environnement inconnu", name)
		return
	}

	identity, _ := IdentityFrom(r.Context())
	payload := map[string]any{
		"id":               view.Environment.ID,
		"label":            view.Environment.Label,
		"rank":             view.Environment.Rank,
		"promotion_suffix": view.Environment.PromotionSuffix,
		"namespace":        view.Environment.Namespace,
		"observable":       view.Environment.Observable,
		"sensitive":        h.authorizer.Sensitive(view.Environment.ID),
		"can_deploy":       h.authorizer.Can(identity, auth.PermDeploy(view.Environment.ID)),
	}

	if view.Observation != nil {
		payload["portal_version"] = view.Observation.PortalVersion
		payload["migration_state"] = view.Observation.MigrationState
		payload["flux_state"] = view.Observation.FluxState
		payload["resolution"] = view.Observation.Resolution
	}
	if view.ObservedAt != nil {
		payload["observed_at"] = *view.ObservedAt
	}

	// Le fil de promotion : la position de cet environnement dans le chemin
	// qu'une version parcourt. Rendu ici plutôt que reconstruit côté navigateur,
	// où l'ordre des rangs devrait être redeviné.
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
	case errors.Is(err, store.ErrNoDeployedRelease):
		// Cas normal : l'application est déployée depuis des années, et le
		// dashboard ne connaît que les releases créées par lui. Le dire plutôt
		// que présenter un écran vide.
		payload["deployed"] = nil
	default:
		writeProblem(w, http.StatusInternalServerError, "Rapprochement impossible", "")
		return
	}

	promotions, err := h.store.LatestPromotions(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des promotions impossible", "")
		return
	}
	if promotion, found := promotions[name]; found {
		payload["last_promotion"] = lastPromotion{
			Tag: promotion.Tag, Release: promotion.ReleaseTag,
			PushedAt: promotion.PushedAt, JobID: promotion.JobID,
		}
	}

	writeJSON(w, http.StatusOK, payload)
}

// observedVersion rend la version constatée sur un environnement, ou vide.
func observedVersion(view store.EnvironmentView) string {
	if view.Observation == nil {
		return ""
	}
	return view.Observation.PortalVersion
}
