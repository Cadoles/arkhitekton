package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Validation de recette.
 *
 * Doc 01 §1.2 décrit le sign-off comme la concaténation de deux moitiés : les
 * contrôles automatiques — « CI / Pipelines » — et la validation humaine —
 * « Client ». La première existait déjà sous forme de résultats de tests ; seule
 * la seconde manquait, et c'est ce que ces routes ajoutent.
 *
 * La jointure des deux moitiés se fait dans l'interface, où les résultats de tests
 * sont déjà chargés. La refaire ici obligerait à recalculer et retransmettre ce
 * que l'écran possède, pour un affichage identique.
 */

// SignoffStore est ce que les routes de validation attendent de la base.
type SignoffStore interface {
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	// Les deux façons de nommer un projet : par le chemin de la requête, ou à
	// défaut par celui qui se construit. Voir internal/http/project.go.
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
	ListSignoffs(ctx context.Context, releaseID int64) ([]store.Signoff, error)
	SetSignoff(ctx context.Context, releaseID int64, environmentID, role, status, detail, decidedBy string) error
	DeleteSignoff(ctx context.Context, releaseID int64, environmentID, role string) error
	SignoffEnvironmentExists(ctx context.Context, environmentID string) (bool, error)
	ListEnvironments(ctx context.Context) ([]store.Environment, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// SignoffsHandler sert la validation humaine d'une release.
type SignoffsHandler struct {
	store SignoffStore
}

func NewSignoffsHandler(signoffs SignoffStore) *SignoffsHandler {
	return &SignoffsHandler{store: signoffs}
}

type signoffVw struct {
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	DecidedBy string    `json:"decided_by"`
	DecidedAt time.Time `json:"decided_at"`
}

type environmentSignoffsVw struct {
	Environment string      `json:"environment"`
	Label       string      `json:"label"`
	Signoffs    []signoffVw `json:"signoffs"`
}

// List rend les validations d'une release, groupées par environnement.
//
// Tous les environnements sont rendus, y compris ceux sans avis. Ne renvoyer que
// les environnements validés laisserait l'interface incapable de distinguer
// « personne ne s'est prononcé » de « cet environnement n'existe pas », alors que
// l'attente est précisément l'information utile en recette.
func (h *SignoffsHandler) List(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	environments, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}

	signoffs, err := h.store.ListSignoffs(r.Context(), release.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des validations impossible", "")
		return
	}

	byEnvironment := map[string][]signoffVw{}
	for _, signoff := range signoffs {
		byEnvironment[signoff.EnvironmentID] = append(byEnvironment[signoff.EnvironmentID], signoffVw{
			Role: signoff.Role, Status: signoff.Status, Detail: signoff.Detail,
			DecidedBy: signoff.DecidedBy, DecidedAt: signoff.DecidedAt,
		})
	}

	// L'ordre est celui du flux de promotion, tel que `ListEnvironments` le rend.
	payload := make([]environmentSignoffsVw, 0, len(environments))
	for _, environment := range environments {
		payload = append(payload, environmentSignoffsVw{
			Environment: environment.ID,
			Label:       environment.Label,
			Signoffs:    orEmpty(byEnvironment[environment.ID]),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"environments": payload})
}

func orEmpty(signoffs []signoffVw) []signoffVw {
	if signoffs == nil {
		// Un tableau vide et non `null` : l'interface itère dessus sans garde.
		return []signoffVw{}
	}
	return signoffs
}

type signoffRequest struct {
	Role   string `json:"role"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// signoffStatuses borne les avis enregistrables.
//
// `na` en est absent : l'attente est l'absence de ligne, pas une ligne qui dit
// « en attente ». L'accepter créerait deux représentations du même état, dont une
// que le retrait ne saurait pas distinguer.
var signoffStatuses = map[string]bool{"pass": true, "warn": true, "fail": true}

// Set enregistre l'avis d'un intervenant sur une release dans un environnement.
func (h *SignoffsHandler) Set(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request signoffRequest
	// 16 Kio : un motif de refus est une phrase, pas un rapport.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	role := strings.TrimSpace(request.Role)
	detail := strings.TrimSpace(request.Detail)

	if role == "" {
		writeProblem(w, http.StatusBadRequest, "Rôle absent",
			"Précisez qui se prononce : « Client », « Recette », « Exploitation ».")
		return
	}
	if !signoffStatuses[request.Status] {
		writeProblem(w, http.StatusBadRequest, "Avis invalide",
			"L'avis vaut « pass », « warn » ou « fail ». L'attente s'obtient en retirant la validation.")
		return
	}
	// Une réserve ou un refus sans motif ne dit rien à qui le lit ensuite, et c'est
	// pourtant à ce moment que la question « pourquoi ? » se pose. Exigé par l'API
	// et non par la base : la règle peut évoluer, les données déjà écrites non.
	if request.Status != "pass" && detail == "" {
		writeProblem(w, http.StatusBadRequest, "Motif absent",
			"Une réserve ou un refus doit indiquer son motif.")
		return
	}

	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	environmentID := r.PathValue("env")
	exists, err := h.store.SignoffEnvironmentExists(r.Context(), environmentID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'environnement impossible", "")
		return
	}
	if !exists {
		writeProblem(w, http.StatusNotFound, "Environnement introuvable", environmentID)
		return
	}

	if err := h.store.SetSignoff(r.Context(), release.ID, environmentID, role,
		request.Status, detail, identity.Subject); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// Le journal porte l'avis complet, motif inclus : la table ne garde que le
	// dernier état, et « qui avait validé avant de se rétracter » n'a de réponse
	// qu'ici.
	h.audit(r, store.AuditSignoffSet, release.Tag, map[string]any{
		"environment": environmentID,
		"role":        role,
		"status":      request.Status,
		"detail":      detail,
	})

	writeJSON(w, http.StatusNoContent, nil)
}

// Clear retire un avis et remet le rôle en attente.
//
// Retirer n'est pas refuser. Un avis rendu sur la mauvaise release doit pouvoir
// disparaître sans laisser un refus qui bloquerait la livraison, et sans obliger
// son auteur à le remplacer par une validation qu'il ne pense pas.
func (h *SignoffsHandler) Clear(w http.ResponseWriter, r *http.Request) {
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	if role == "" {
		writeProblem(w, http.StatusBadRequest, "Rôle absent",
			"Précisez le rôle dont l'avis doit être retiré.")
		return
	}

	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}

	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return
	}

	environmentID := r.PathValue("env")
	if err := h.store.DeleteSignoff(r.Context(), release.ID, environmentID, role); err != nil {
		if errors.Is(err, store.ErrSignoffNotFound) {
			writeProblem(w, http.StatusNotFound, "Validation introuvable", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Retrait impossible", "")
		return
	}

	h.audit(r, store.AuditSignoffClear, release.Tag, map[string]any{
		"environment": environmentID,
		"role":        role,
	})

	writeJSON(w, http.StatusNoContent, nil)
}

func (h *SignoffsHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
