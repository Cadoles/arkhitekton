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
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
	imagenames "forge.cadoles.com/vcarroy/mse-dashboard/internal/images"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
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
	// BuildableProject dit quelles images une release construit et lesquelles un
	// re-tag propage. Lu à chaque mise en file plutôt que mémorisé au démarrage :
	// les étapes se modifient depuis l'administration, et une release lancée
	// après le changement doit construire ce qui est déclaré maintenant.
	BuildableProject(ctx context.Context) (store.Project, error)
	SetReleaseState(ctx context.Context, id int64, state string) error
	MarkReleased(ctx context.Context, id int64, from string) (bool, error)
	AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error)
	ReleaseBadgesByTag(ctx context.Context) (map[string]store.ReleaseBadges, error)
	ListNoteEntries(ctx context.Context, releaseID int64) ([]store.NoteEntry, error)
	ListReleaseVersionIssues(ctx context.Context, releaseID int64) ([]store.NoteEntry, error)
	LinkNoteIssue(ctx context.Context, releaseID, redmineID int64, source, pullRequest, actor string) error
	UnlinkNoteIssue(ctx context.Context, releaseID, redmineID int64) error
	Directory(ctx context.Context) (map[string]string, error)
	// Les deux lectures du score. Rendues ici parce que la liste des releases
	// affiche le même chiffre que l'écran d'une release, et que deux calculs
	// distincts finiraient par diverger.
	ListReleaseTestResults(ctx context.Context, releaseID int64) ([]store.TestResult, error)
	LatestSecurityAudit(ctx context.Context, releaseID int64) (store.SecurityAudit, error)
	ListReleaseLoadResults(ctx context.Context, releaseID int64) ([]store.LoadResult, error)
	ListResultMeasures(ctx context.Context, resultID int64) ([]store.BenchMeasure, error)
	Baselines(ctx context.Context, scenario string) (map[string]store.Baseline, error)
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
	// names rapproche le nom de version saisi de celui du suivi. Doit être le
	// même que celui du store, dont il compare le résultat à une colonne.
	names release.Normalizer
	// logger sert à dire ce qui échoue sans faire échouer l'écran. Nil-safe :
	// `slog.Default()` par défaut, pour que les tests n'aient pas à le fournir.
	logger *slog.Logger
	// redmineURL sert à construire les liens vers les demandes d'une note. Vide,
	// les liens sont omis plutôt que rendus morts.
	redmineURL string
	// registryProject préfixe les images publiées. Sert à nommer à l'audit celles
	// qu'il doit analyser lors d'une relance — il analyse une image **déjà
	// publiée**, jamais une reconstruction (B17).
	registryProject string
	// forge résout une référence en commit. Nil quand la forge n'est pas
	// configurée.
	forge CommitResolver
	// tagger pose le tag Git. Nil quand le jeton est en lecture seule.
	tagger Tagger
	// weights pondère le score. Même valeur que celle du gestionnaire des tests :
	// la pastille de la liste et l'écran d'une release doivent afficher le même
	// chiffre.
	weights results.Weights
}

// WithRedmineURL branche les liens vers les demandes.
func (h *ReleasesHandler) WithRedmineURL(url string) *ReleasesHandler {
	h.redmineURL = url
	return h
}

func (h *ReleasesHandler) issueURL(redmineID int64) string {
	if h.redmineURL == "" {
		return ""
	}
	return h.redmineURL + "/issues/" + strconv.FormatInt(redmineID, 10)
}

// WithWeights déclare la pondération du score.
func (h *ReleasesHandler) WithWeights(weights results.Weights) *ReleasesHandler {
	h.weights = weights
	return h
}

// WithForge branche la résolution de commits et la pose de tags.
//
// Les deux ensemble parce qu'ils sont un seul geste : résoudre une branche en
// commit puis poser le tag sur ce commit. Poser un tag sur une branche le
// placerait sur ce qu'elle désigne à cet instant, et deux releases créées à une
// minute d'intervalle pourraient viser des commits différents.
func (h *ReleasesHandler) WithForge(forge CommitResolver, tagger Tagger) *ReleasesHandler {
	h.forge = forge
	h.tagger = tagger
	return h
}

// Images est ce que le handler attend du registre : lire, jamais écrire. Le
// re-tag est fait par la Task `promote-image` avec ses propres identifiants.
type Images interface {
	Digest(ctx context.Context, repository, tag string) (string, error)
}

// CommitResolver est ce que le handler attend de la forge pour ancrer une release.
//
// Optionnelle : sans elle, la release est créée depuis la référence saisie et le
// dit. Faire échouer la création parce que la forge est injoignable priverait du
// reste de la chaîne pour un ancrage qu'on peut ajouter ensuite.
type CommitResolver interface {
	ResolveCommit(ctx context.Context, ref string) (string, error)
}

// Tagger pose le tag Git d'une release (B3).
//
// Séparée de [CommitResolver] : lire la forge et y écrire sont deux permissions, portées
// par deux clients. Nil quand le jeton ne peut pas écrire.
type Tagger interface {
	CreateTag(ctx context.Context, tag, commit, message string) error
}

// Artifacts est ce que le handler attend du stockage objet.
type Artifacts interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Contrat avec l'ordonnanceur. Répétés ici plutôt qu'importés de `worker` :
// l'`api` ne dépend pas du worker, et l'inverse non plus — les deux se parlent
// par la base.
const (
	exportJobKind = "export"
	// Écrit ici plutôt qu'importé de `worker` : ce paquet ne dépend pas de
	// l'ordonnanceur, et l'inverse est vrai — l'y faire dépendre créerait un
	// cycle avec les tests de `worker`, qui lisent ce paquet. Un test de
	// cohérence garde les deux alignés.
	ReleaseNotesJobKind = "release-notes"
	exportParamKind     = "export-kind"
	exportParamFormat   = "export-format"
)

// WithNormalizer déclare les préfixes retirés pour comparer deux noms de version.
//
// Sans appel, [release.DefaultNormalizer] s'applique. Le déploiement doit passer
// le **même** que celui du store : ce handler compare une saisie à ce que la base
// a stocké, et deux conventions divergentes rejetteraient une saisie correcte.
func (h *ReleasesHandler) WithNormalizer(names release.Normalizer) *ReleasesHandler {
	h.names = names
	return h
}

func NewReleasesHandler(
	releases ReleaseStore, pipelines *catalog.Catalog,
	authorizer *auth.Authorizer, images Images, artifacts Artifacts,
	imageName, registryProject string,
) *ReleasesHandler {
	return &ReleasesHandler{
		store: releases, catalog: pipelines, authorizer: authorizer,
		images: images, artifacts: artifacts, imageName: imageName,
		registryProject: registryProject, names: release.DefaultNormalizer,
		logger: slog.Default(),
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
	ImageVersion string `json:"image_version"`
	GitRef       string `json:"git_ref,omitempty"`
	// GitCommit est ce qui a réellement été construit. Distinct de `GitRef`, qui
	// dit seulement d'où la release est partie : une branche bouge, un commit non
	// (B17).
	GitCommit  string              `json:"git_commit,omitempty"`
	SourceTag  string              `json:"source_tag,omitempty"`
	CreatedBy  string              `json:"created_by"`
	CreatedAt  time.Time           `json:"created_at"`
	ReleasedAt *time.Time          `json:"released_at,omitempty"`
	Promotions []promotionPayload  `json:"promotions,omitempty"`
	Targets    []promotionTargetVw `json:"targets,omitempty"`
	Jobs       []releaseJobPayload `json:"jobs,omitempty"`
	Images     []imagePayload      `json:"images,omitempty"`
	// ImagesError dit pourquoi les digests manquent. L'absence de digest et
	// l'impossibilité de le lire ne se ressemblent pas : la première signale une
	// image qui n'existe pas, la seconde un registre injoignable ou non
	// configuré. Les confondre ferait chercher une panne de build.
	ImagesError  string          `json:"images_error,omitempty"`
	ManualAction *manualActionVw `json:"manual_actions,omitempty"`

	// Ce qu'un écran affiche à côté du nom, sans avoir à déplier la release.
	//
	// `BuildFailed` porte sur la **dernière** construction : une release relancée
	// après un échec d'infrastructure garderait sinon une pastille que plus rien
	// ne justifie.
	BuildFailed bool `json:"build_failed,omitempty"`
	// DeployedOn énumère les environnements qui font tourner cette release. Vide
	// dès qu'une autre y a été déployée depuis : la liste vient de ce que chaque
	// environnement rapporte, pas de l'historique des promotions.
	DeployedOn []string     `json:"deployed_on,omitempty"`
	Score      *scoreDigest `json:"score,omitempty"`
}

// scoreDigest est le score sans sa décomposition.
//
// Le détail vit sur l'écran d'une release ; une pastille n'a besoin que du
// chiffre et de son verdict. `Measured` distingue « non évalué » de « zéro », que
// le seul chiffre confondrait (B11).
type scoreDigest struct {
	Value    int    `json:"value"`
	Health   string `json:"health"`
	Measured bool   `json:"measured"`
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
	// Repository est le nom court du dépôt — `portal` — tel que le registre
	// l'attend : son client préfixe lui-même le projet.
	Repository string `json:"repository"`
	// Reference est le dépôt complet, registre et projet compris. C'est ce qu'on
	// copie pour tirer l'image ou l'inspecter, et le nom court seul ne suffit pas
	// à la désigner — deux projets du même registre portent un `portal`.
	Reference string `json:"reference,omitempty"`
	Tag       string `json:"tag"`
	Digest    string `json:"digest,omitempty"`
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

func toReleasePayload(release store.Release, sourceTag string, who actors) releasePayload {
	return releasePayload{
		Tag: release.Tag, Kind: release.Kind, State: release.State,
		ImageVersion: release.ImageVersion, GitRef: release.GitRef,
		GitCommit: release.GitCommit, SourceTag: sourceTag,
		CreatedBy: who.name(release.CreatedBy),
		CreatedAt: release.CreatedAt, ReleasedAt: release.ReleasedAt,
	}
}

// actorsFor charge l'annuaire pour la durée d'une requête.
func (h *ReleasesHandler) actorsFor(ctx context.Context) actors {
	return resolveActors(ctx, h.store, h.logger)
}

// List rend les releases les plus récentes.
func (h *ReleasesHandler) List(w http.ResponseWriter, r *http.Request) {
	releases, err := h.store.ListReleases(r.Context(), 50)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des releases impossible", "")
		return
	}

	badges, err := h.store.ReleaseBadgesByTag(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des releases impossible", "")
		return
	}

	who := h.actorsFor(r.Context())

	payload := make([]releasePayload, 0, len(releases))
	for _, item := range releases {
		entry := toReleasePayload(item, "", who)
		entry.BuildFailed = badges[item.Tag].BuildFailed
		entry.DeployedOn = badges[item.Tag].Deployed
		// Le score est calculé release par release, donc deux lectures chacune.
		// C'est assumé : la liste est bornée à cinquante entrées, les requêtes
		// sont indexées, et le même chiffre doit sortir ici et sur l'écran d'une
		// release — un calcul approché pour la liste les aurait fait diverger.
		entry.Score = h.scoreDigest(r.Context(), item.ID)
		payload = append(payload, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": payload})
}

// scoreDigest rend le score d'une release, ou rien.
//
// Un échec de lecture ne fait pas échouer l'écran : la liste reste utile sans
// pastille de score, et refuser de l'afficher pour un signal secondaire serait
// disproportionné. Le cas est silencieux parce qu'il n'appelle aucune action.
func (h *ReleasesHandler) scoreDigest(ctx context.Context, releaseID int64) *scoreDigest {
	reports, err := h.store.ListReleaseTestResults(ctx, releaseID)
	if err != nil {
		return nil
	}
	breakdown, err := computeScore(ctx, h.store, releaseID, reports, h.weights)
	if err != nil {
		return nil
	}
	return &scoreDigest{
		Value:    breakdown.Score,
		Health:   results.Health(breakdown),
		Measured: breakdown.Measured,
	}
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

	who := h.actorsFor(r.Context())
	payload := toReleasePayload(item, "", who)

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
			RunName: job.RunName, RequestedBy: who.name(job.RequestedBy),
			RequestedAt: job.RequestedAt, FinishedAt: job.FinishedAt,
		})
	}

	// Les mêmes pastilles que dans la liste, et par le même chemin : deux
	// dérivations de « la construction a-t-elle échoué » auraient fini par se
	// contredire d'un écran à l'autre.
	if badges, err := h.store.ReleaseBadgesByTag(r.Context()); err == nil {
		payload.BuildFailed = badges[item.Tag].BuildFailed
		payload.DeployedOn = badges[item.Tag].Deployed
	}
	payload.Score = h.scoreDigest(r.Context(), item.ID)

	if item.ManualActions != "" || item.ManualActionsUpdatedAt != nil {
		payload.ManualAction = &manualActionVw{
			Markdown:  item.ManualActions,
			UpdatedBy: who.name(item.ManualActionsUpdatedBy),
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
		images = append(images, imagePayload{
			Repository: h.imageName,
			Reference:  imageRepository(h.registryProject, h.imageName),
			Tag:        tag,
		})
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
		if err := h.names.CheckRedmineVersion(version, request.RedmineVersion); err != nil {
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

	// Ancrage sur un commit, puis pose du tag (B3, B17).
	//
	// L'ordre importe : on résout la référence en commit **avant** de créer la
	// release, pour que ce qui est enregistré désigne un point immuable. Poser le
	// tag ensuite donne à ce point un nom lisible dans la forge.
	//
	// Les deux sont facultatifs et le disent : sans forge configurée, ou sans
	// jeton capable d'écrire, la release existe quand même — avec une réserve.
	// Refuser la création priverait de toute la chaîne pour un ancrage qu'on peut
	// ajouter après coup.
	// `tagged` est une variable locale et non un champ du handler : celui-ci est
	// partagé par toutes les requêtes, et deux créations simultanées liraient le
	// drapeau l'une de l'autre.
	if kind != release.KindFinal {
		commit, resolveWarnings := h.anchorCommit(r.Context(), request.GitRef)
		record.GitCommit = commit
		warnings = append(warnings, resolveWarnings...)

		if commit != "" {
			// Le tag est posé pour les humains et pour la traçabilité ; la
			// construction, elle, part du commit. Son échec est donc une réserve
			// et n'empêche pas de construire.
			_, tagWarnings := h.tagRelease(r.Context(), version.Raw, commit)
			warnings = append(warnings, tagWarnings...)
		}
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

	who := h.actorsFor(r.Context())
	response := map[string]any{
		"release": toReleasePayload(record, request.SourceTag, who), "warnings": warnings,
	}

	// Une release construite enchaîne sur `build-image`. Sans cet enchaînement,
	// elle restait `draft` indéfiniment : le pipeline existait dans le cluster,
	// mais rien ne le déclenchait, et l'écran affichait un brouillon que personne
	// ne savait faire avancer.
	if record.State == string(release.StateDraft) && kind != release.KindFinal {
		// Jamais d'une branche quand le commit est connu : reconstruire la même
		// release six mois plus tard doit produire la même image (B17).
		buildRef := buildReference(record, request.GitRef)
		jobIDs, buildErr := h.enqueueBuild(r.Context(), releaseID, record, buildRef, identity.Subject)
		switch {
		case buildErr != nil:
			// La release est créée : le dire, plutôt que de laisser croire à un
			// échec de création. La construction se relance depuis la file.
			warnings = append(warnings,
				"la release est créée mais la construction n'a pas pu être mise en file: "+buildErr.Error())
			response["warnings"] = warnings
		default:
			response["build_job_ids"] = jobIDs
			// L'état suit la mise en file et non la fin du pipeline : `building`
			// signifie « construction demandée », ce qui est exactement ce qu'on
			// sait à cet instant.
			if err := h.store.SetReleaseState(r.Context(), releaseID, string(release.StateBuilding)); err != nil {
				warnings = append(warnings,
					"construction mise en file, mais l'état de la release n'a pas pu être avancé")
				response["warnings"] = warnings
			} else {
				record.State = string(release.StateBuilding)
				response["release"] = toReleasePayload(record, request.SourceTag, who)
			}
		}
	}

	// La note de release est composée en tâche de fond : la détection interroge
	// jusqu'à deux cents pull requests, et la faire ici aurait fait attendre
	// l'utilisateur devant son formulaire. Son échec ne remet pas la création en
	// cause — la note se complète à la main.
	if _, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind:        ReleaseNotesJobKind,
		Params:      map[string]string{},
		RequestedBy: identity.Subject,
		ReleaseID:   &releaseID,
	}); err != nil {
		warnings = append(warnings,
			"la note de release n'a pas pu être mise en file: "+err.Error())
		response["warnings"] = warnings
	}

	// Une release finale doit exister sous son propre tag dans le registre, pas
	// seulement sous celui de sa RC. Sans ce re-tag, `1.8.0` ne désigne rien :
	// aucune ImagePolicy ne peut le retenir, et la release n'est promouvable nulle
	// part alors que son image est prête (B17).
	if record.ImageVersion != record.Tag {
		jobIDs, err := h.enqueueRetag(r.Context(), releaseID, record, identity.Subject)
		if err != nil {
			// La release est créée : le dire, plutôt que de laisser croire à un
			// échec de création. Le re-tag se relance.
			response["warnings"] = append(warnings,
				"la release est créée mais le re-tag vers son propre tag n'a pas pu être mis en file")
		} else {
			response["retag_job_ids"] = jobIDs
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
		"release": toReleasePayload(record, "", h.actorsFor(r.Context())),
	})
}

// Rebuild relance la construction des images d'une release.
//
// Sert après un échec dont la cause est extérieure au code — clé de déploiement
// mal déclarée, registre injoignable, nœud saturé. Créer une RC de plus pour
// contourner un incident d'infrastructure ferait porter au numéro de version
// l'historique des pannes du cluster.
//
// La référence Git n'est pas redemandée : elle est celle de la release, décidée à
// sa création. La rouvrir permettrait de reconstruire un tag donné depuis un
// autre commit, et deux images porteraient alors le même numéro sans être la même
// chose (B17).
func (h *ReleasesHandler) Rebuild(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
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

	// Une release finale n'a rien à construire : c'est un re-tag, et son image est
	// celle de sa RC source.
	if record.Kind == string(release.KindFinal) {
		writeProblem(w, http.StatusConflict, "Reconstruction impossible",
			"une release finale est un re-tag: son image est celle de sa pré-release source")
		return
	}

	from := release.State(record.State)
	if !release.CanTransition(from, release.StateBuilding) {
		writeProblem(w, http.StatusConflict, "Reconstruction impossible",
			fmt.Sprintf("la release %q est dans l'état %q, d'où aucune reconstruction n'est permise",
				tag, record.State))
		return
	}

	jobIDs, err := h.enqueueBuild(r.Context(), record.ID, record, record.GitRef, identity.Subject)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "Reconstruction impossible", err.Error())
		return
	}

	// L'état suit la mise en file, comme à la création : `building` signifie
	// « construction demandée ». L'écriture est conditionnée à l'état lu — deux
	// relances simultanées ne doivent pas produire deux runs.
	advanced, err := h.store.AdvanceReleaseState(r.Context(), record.ID, record.State, string(release.StateBuilding))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Reconstruction impossible", "")
		return
	}
	if !advanced {
		writeProblem(w, http.StatusConflict, "Reconstruction impossible",
			"l'état de la release a changé entre-temps, rechargez la page")
		return
	}

	h.audit(r, store.AuditReleaseRebuild, tag, map[string]any{
		"from": record.State, "git_ref": record.GitRef, "jobs": jobIDs,
	})

	record.State = string(release.StateBuilding)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"release": toReleasePayload(record, "", h.actorsFor(r.Context())), "build_job_ids": jobIDs,
	})
}

// VerifiableKinds énumère les familles de vérification relançables à l'unité.
//
// Volontairement figée ici plutôt que déduite du catalogue : celui-ci porte aussi
// `build` et `promote`, qui ne sont pas des vérifications, et une relance de
// construction déguisée en relance de test ferait bien plus que ce que
// l'utilisateur a demandé.
//
// Doit rester alignée sur `verificationKinds` de l'ordonnanceur. Un test le
// vérifie sur le catalogue livré.
var VerifiableKinds = []string{
	"static-tests", "unit-tests", "security-audit", "integration-tests",
}

func verifiable(kind string) bool {
	for _, known := range VerifiableKinds {
		if known == kind {
			return true
		}
	}
	return false
}

// Verify relance une seule famille de vérification sur une release.
//
// Le geste manquait : une vérification en échec pour une cause extérieure —
// registre injoignable, nœud saturé, dépôt momentanément inaccessible —
// n'obligeait à rien de moins qu'une reconstruction complète, qui republie les
// images et rejoue *toutes* les vérifications. Rejouer celle qui a échoué coûte
// une fraction de cela et ne touche pas à l'artefact.
//
// L'état de la release n'est **pas** modifié. Une relance ne dit rien de neuf sur
// la release tant qu'elle n'a pas conclu, et la faire retomber en `verifying`
// depuis `ready` annulerait une décision déjà prise sur les autres familles.
func (h *ReleasesHandler) Verify(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	tag := r.PathValue("tag")
	kind := r.PathValue("kind")
	if !verifiable(kind) {
		writeProblem(w, http.StatusBadRequest, "Famille inconnue",
			fmt.Sprintf("%q n'est pas une famille de vérification relançable", kind))
		return
	}

	record, err := h.store.GetRelease(r.Context(), tag)
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", tag)
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	// La révision vérifiée est le commit construit, jamais la branche dont il
	// vient : celle-ci a pu bouger, et les tests porteraient alors sur autre chose
	// que l'image (B17).
	revision := record.GitCommit
	if revision == "" {
		revision = record.GitRef
	}
	if revision == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Relance impossible",
			"cette release ne désigne aucune référence Git: il n'y a rien à vérifier")
		return
	}

	pipeline, found := h.catalog.Find(kind)
	if !found {
		writeProblem(w, http.StatusUnprocessableEntity, "Relance impossible",
			fmt.Sprintf("le catalogue ne déclare aucun pipeline %q", kind))
		return
	}

	params, missing := pipeline.VerificationInputs(revision, record.Tag, h.registryProject)
	if missing != "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Relance impossible",
			fmt.Sprintf("le paramètre %q ne peut pas être renseigné pour cette release", missing))
		return
	}

	jobID, err := h.store.Enqueue(r.Context(), store.JobRequest{
		Kind:        pipeline.Kind,
		Params:      params,
		RequestedBy: identity.Subject,
		ReleaseID:   &record.ID,
	})
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Relance impossible", "")
		return
	}

	h.audit(r, store.AuditReleaseVerify, tag, map[string]any{
		"kind": kind, "revision": revision, "job": jobID,
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"kind": kind, "job_id": jobID, "revision": revision,
	})
}

func imageReference(registryProject, name, tag string) string {
	return imagenames.Reference(registryProject, name, tag)
}

func imageRepository(registryProject, name string) string {
	return imagenames.Repository(registryProject, name)
}

// notePayload est une ligne de note de release.
type notePayload struct {
	RedmineID int64  `json:"redmine_id"`
	Subject   string `json:"subject,omitempty"`
	Tracker   string `json:"tracker,omitempty"`
	Status    string `json:"status,omitempty"`
	// Anomaly range la ligne : les anomalies vont aux corrections, le reste aux
	// nouveautés. Le tracker et non le statut : c'est la nature de la demande qui
	// décide, pas son avancement.
	Anomaly bool `json:"anomaly"`
	// Known est faux tant que la synchronisation Redmine n'a pas vu la demande.
	// La ligne reste affichée : la masquer ferait disparaître un ajout manuel.
	Known       bool   `json:"known"`
	Source      string `json:"source"`
	PullRequest string `json:"pull_request,omitempty"`
	AddedBy     string `json:"added_by"`
	// URL vers la demande dans Redmine. Vide quand Redmine n'est pas configuré :
	// un lien mort vaut moins que pas de lien.
	URL string `json:"url,omitempty"`
}

// Notes rend la note d'une release.
func (h *ReleasesHandler) Notes(w http.ResponseWriter, r *http.Request) {
	record, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	entries, err := h.store.ListNoteEntries(r.Context(), record.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de la note impossible", "")
		return
	}

	who := h.actorsFor(r.Context())

	payload := make([]notePayload, 0, len(entries))
	linked := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		linked[entry.RedmineID] = true
		payload = append(payload, notePayload{
			RedmineID: entry.RedmineID, Subject: entry.Subject, Tracker: entry.Tracker,
			Status: entry.StatusName, Anomaly: entry.Anomaly, Known: entry.Known,
			Source: entry.Source, PullRequest: entry.PullRequest,
			AddedBy: who.name(entry.AddedBy),
			URL:     h.issueURL(entry.RedmineID),
		})
	}

	// Le vivier dans lequel compléter la note à la main : les demandes de la
	// version, moins celles déjà rattachées. Rendu avec la note plutôt que par un
	// second appel — les deux se lisent ensemble, et l'écran n'a pas à composer
	// une différence que le serveur connaît déjà.
	//
	// Un échec de lecture n'empêche pas d'afficher la note — le rattachement par
	// numéro reste possible — mais il est **dit**. Le taire rendait une liste vide
	// indiscernable d'une version sans demandes, et l'écran annonçait alors que
	// tout était déjà rattaché alors qu'il n'avait rien pu lire. Même règle que
	// `images_error` : l'absence et l'impossibilité de lire ne se ressemblent pas.
	available := make([]notePayload, 0)
	var availableError string

	issues, err := h.store.ListReleaseVersionIssues(r.Context(), record.ID)
	if err != nil {
		availableError = "Les demandes de la version n'ont pas pu être lues."
		h.logger.Error("demandes de la version d'une release",
			"release", record.Tag, "error", err)
	}
	for _, issue := range issues {
		if linked[issue.RedmineID] {
			continue
		}
		available = append(available, notePayload{
			RedmineID: issue.RedmineID, Subject: issue.Subject, Tracker: issue.Tracker,
			Status: issue.StatusName, Anomaly: issue.Anomaly, Known: true,
			URL: h.issueURL(issue.RedmineID),
		})
	}

	response := map[string]any{"notes": payload, "available": available}
	if availableError != "" {
		response["available_error"] = availableError
	}
	writeJSON(w, http.StatusOK, response)
}

// AddNote rattache une demande à la main.
//
// Le geste existe parce que la détection ne peut pas tout voir : une PR dont le
// titre ne suit pas la convention, une demande passée en revue après la création
// de la release. Sans lui, la seule issue serait de recréer la release.
func (h *ReleasesHandler) AddNote(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	record, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	redmineID, err := strconv.ParseInt(r.PathValue("issue"), 10, 64)
	if err != nil || redmineID <= 0 {
		writeProblem(w, http.StatusBadRequest, "Demande invalide",
			"le numéro de demande doit être un entier positif")
		return
	}

	switch err := h.store.LinkNoteIssue(r.Context(), record.ID, redmineID,
		store.NoteSourceManual, "", identity.Subject); {
	case errors.Is(err, store.ErrNoteAlreadyLinked):
		writeProblem(w, http.StatusConflict, "Demande déjà rattachée",
			fmt.Sprintf("la demande #%d figure déjà dans la note de %s", redmineID, record.Tag))
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Rattachement impossible", "")
		return
	}

	h.audit(r, store.AuditReleaseNoteAdd, record.Tag, map[string]any{"redmine_id": redmineID})
	writeJSON(w, http.StatusCreated, map[string]any{"redmine_id": redmineID})
}

// RemoveNote retire une demande de la note.
func (h *ReleasesHandler) RemoveNote(w http.ResponseWriter, r *http.Request) {
	record, err := h.store.GetRelease(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	redmineID, err := strconv.ParseInt(r.PathValue("issue"), 10, 64)
	if err != nil || redmineID <= 0 {
		writeProblem(w, http.StatusBadRequest, "Demande invalide", "")
		return
	}

	if err := h.store.UnlinkNoteIssue(r.Context(), record.ID, redmineID); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Retrait impossible", "")
		return
	}

	h.audit(r, store.AuditReleaseNoteRemove, record.Tag, map[string]any{"redmine_id": redmineID})
	w.WriteHeader(http.StatusNoContent)
}

// anchorCommit résout une référence en commit, et dit pourquoi il ne l'a pas fait.
//
// Une release qui désigne une branche n'est pas reproductible : la branche bouge,
// et reconstruire produirait une autre image. Résoudre à la création fige le
// point de départ, même quand le tag ne peut pas être posé.
func (h *ReleasesHandler) anchorCommit(ctx context.Context, ref string) (string, []string) {
	if strings.TrimSpace(ref) == "" {
		return "", []string{"aucune référence Git fournie: la release n'est rattachée à aucun commit"}
	}
	if h.forge == nil {
		return "", []string{
			"forge non configurée: la release retient la référence " + ref +
				" sans la résoudre en commit, et n'est donc pas reproductible",
		}
	}

	commit, err := h.forge.ResolveCommit(ctx, ref)
	if err != nil {
		return "", []string{"la référence " + ref + " n'a pas pu être résolue en commit: " + err.Error()}
	}
	return commit, nil
}

// tagRelease pose le tag Git de la release sur son commit.
//
// C'est le segment que B3 confie au dashboard et qui restait manuel. Un échec est
// une réserve et non un refus : le tag se repose, alors qu'une release refusée
// oblige à tout recommencer.
func (h *ReleasesHandler) tagRelease(ctx context.Context, tag, commit string) (bool, []string) {
	if h.tagger == nil {
		return false, []string{
			"aucun jeton capable d'écrire sur la forge: le tag " + tag +
				" n'a pas été posé, la release reste identifiée par son commit",
		}
	}

	err := h.tagger.CreateTag(ctx, tag, commit, "Release "+tag)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, gitea.ErrTagExists):
		// Reposer un tag après une reprise est normal. Il désigne déjà ce qu'il
		// faut, et le signaler suffit.
		return true, []string{"le tag " + tag + " existait déjà sur la forge et n'a pas été modifié"}
	default:
		return false, []string{"le tag " + tag + " n'a pas pu être posé: " + err.Error()}
	}
}

// buildReference choisit ce que la construction va cloner.
//
// **Le commit d'abord, pas le tag.** B3 décrit une construction depuis
// `refs/tags/<tag>`, et c'est ce qui était fait ici : le clone réussissait, puis
// `generate-image-tag` échouait sur `pathspec 3.2.0-rc2 did not match`. La cause
// est que `git-clone` travaille en `--depth=1` : un clone superficiel positionne
// l'arbre sur la révision demandée mais ne crée pas la référence locale du tag,
// et le `git checkout` que la Task fait ensuite ne trouve rien. Un SHA, lui, est
// toujours présent — c'est `HEAD`.
//
// Le tag reste posé sur la forge, où il sert aux humains, et `version` continue
// de le porter jusqu'au `version.txt` de l'image : l'invariant est intact, seule
// la façon de désigner le point de départ change.
//
// Ordre : commit résolu, puis référence saisie faute de mieux. Cette dernière
// n'est pas immuable et ne devrait servir que quand la forge est injoignable.
func buildReference(record store.Release, requested string) string {
	if record.GitCommit != "" {
		return record.GitCommit
	}
	return requested
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
// Une exécution par image, et non une pour toutes. Le pipeline n'en construit
// qu'une : c'est l'appelant qui décide combien il en lance, et c'est ce qui
// permet à chacune d'échouer et de se relancer séparément.
//
// Les mises en file sont indépendantes, mais l'échec de la première interrompt :
// laisser partir deux constructions quand la troisième n'a pas pu être enfilée
// produirait une release que rien ne pourrait conclure — le décompte attendrait
// une image qui n'a jamais été demandée.
func (h *ReleasesHandler) enqueueBuild(
	ctx context.Context, releaseID int64, record store.Release, gitRef, subject string,
) ([]int64, error) {
	pipeline, found := h.catalog.Find("build")
	if !found {
		return nil, errors.New("catalogue sans pipeline build")
	}
	if strings.TrimSpace(gitRef) == "" {
		return nil, errors.New("aucune référence Git à construire")
	}

	project, err := h.store.BuildableProject(ctx)
	if err != nil {
		return nil, err
	}

	targets := project.Buildable()
	jobs := make([]int64, 0, len(targets))
	for _, target := range targets {
		jobID, err := h.store.Enqueue(ctx, store.JobRequest{
			Kind: pipeline.Kind,
			Params: map[string]string{
				"revision":   gitRef,
				"version":    record.Tag,
				"tag-name":   record.Tag,
				"image-name": target.Image,
				"target":     target.Stage,
			},
			RequestedBy: subject,
			ReleaseID:   &releaseID,
		})
		if err != nil {
			return jobs, fmt.Errorf("construction de %s: %w", target.Image, err)
		}
		jobs = append(jobs, jobID)
	}
	return jobs, nil
}

// enqueueRetag met en file le re-tag d'une release vers son propre tag.
//
// Une exécution par image promue — l'image de développement n'en est pas : aucun
// environnement du flux de promotion ne la déploie.
func (h *ReleasesHandler) enqueueRetag(
	ctx context.Context, releaseID int64, record store.Release, subject string,
) ([]int64, error) {
	pipeline, found := h.catalog.Find(retagJobKind)
	if !found {
		return nil, errors.New("catalogue sans pipeline de re-tag")
	}

	project, err := h.store.BuildableProject(ctx)
	if err != nil {
		return nil, err
	}

	targets := project.Promotable()
	jobs := make([]int64, 0, len(targets))
	for _, target := range targets {
		jobID, err := h.store.Enqueue(ctx, store.JobRequest{
			Kind: pipeline.Kind,
			Params: map[string]string{
				"image-name": target.Image,
				"old-tag":    record.ImageVersion,
				"new-tag":    record.Tag,
			},
			RequestedBy: subject,
			ReleaseID:   &releaseID,
		})
		if err != nil {
			return jobs, fmt.Errorf("re-tag de %s: %w", target.Image, err)
		}
		jobs = append(jobs, jobID)
	}
	return jobs, nil
}

// retagJobKind est la nature de job qui pose un tag supplémentaire.
//
// Nommée `promote` jusqu'ici, ce qui décrivait l'intention plutôt que l'acte : le
// pipeline ne promeut rien, il re-tague, et le dashboard s'en sert aussi hors
// promotion — donner à une release finale son propre tag depuis celui de sa
// pré-release source.
const retagJobKind = "retag"

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

	pipeline, found := h.catalog.Find(retagJobKind)
	if !found {
		writeProblem(w, http.StatusInternalServerError, "Catalogue incomplet", retagJobKind)
		return
	}

	// Une exécution et une ligne de promotion par image. La table les distingue
	// déjà — sa clé porte le nom de l'image — mais une seule y était écrite : le
	// portail. Ce qui suivait le portail sans être suivi, c'était `portal-cli`,
	// et rien n'aurait dit qu'un de ses re-tags avait échoué.
	project, err := h.store.BuildableProject(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Images promues inconnues", err.Error())
		return
	}

	targets := project.Promotable()
	jobIDs := make([]int64, 0, len(targets))
	for _, target := range targets {
		jobID, err := h.store.Enqueue(r.Context(), store.JobRequest{
			Kind: pipeline.Kind,
			Params: map[string]string{
				"image-name": target.Image,
				"old-tag":    plan.OldTag,
				"new-tag":    plan.NewTag,
			},
			RequestedBy: identity.Subject,
			ReleaseID:   &item.ID,
			Environment: &environment.ID,
		})
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Mise en file impossible", "")
			return
		}
		jobIDs = append(jobIDs, jobID)

		// Enregistrer la promotion **demandée**, sans digest : le re-tag n'a pas
		// eu lieu. C'est ce qui permettra de distinguer « promu mais pas encore
		// déployé » de « jamais promu » quand l'observation contredira l'attendu.
		if err := h.store.RecordPromotion(r.Context(), store.Promotion{
			ReleaseID: item.ID, EnvironmentID: environment.ID,
			ImageName: target.Image, Tag: plan.NewTag, JobID: &jobID,
		}); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Enregistrement de la promotion impossible", "")
			return
		}
	}

	// Consigné après l'enregistrement : ce qui compte est que le tag ait été
	// demandé, et le tag part dès que le job est pris.
	h.audit(r, store.AuditReleasePromote, item.Tag, map[string]any{
		"environment": environment.ID, "tag": plan.NewTag,
		"from": plan.OldTag, "job_ids": jobIDs,
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_ids":     jobIDs,
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
		UpdatedBy: h.actorsFor(r.Context()).name(item.ManualActionsUpdatedBy),
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
