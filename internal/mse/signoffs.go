package mse

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Validation de recette.
 *
 * Doc 01 §1.2 décrit le sign-off comme la concaténation de deux moitiés : les
 * contrôles automatiques et la validation humaine. Ces routes portent la seconde.
 *
 * Dans le plugin parce qu'un avis porte sur une release **dans un environnement**
 * : la table a une clé étrangère vers `environments`, et sans le module il n'y a
 * pas d'environnement sur lequel se prononcer (doc 16 §16.7, couture 5).
 */

// SignoffStore est ce que les routes de validation attendent de la base.
type SignoffStore interface {
	dashhttp.ProjectResolver
	GetRelease(ctx context.Context, projectID int64, tag string) (store.Release, error)
	ListSignoffs(ctx context.Context, releaseID int64) ([]store.Signoff, error)
	SetSignoff(ctx context.Context, releaseID int64, environmentID, role, status, detail, decidedBy string) error
	DeleteSignoff(ctx context.Context, releaseID int64, environmentID, role string) error
	SignoffEnvironmentExists(ctx context.Context, environmentID string) (bool, error)
	ListEnvironments(ctx context.Context) ([]store.Environment, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// SignoffsHandler sert la validation humaine d'une release.
type SignoffsHandler struct {
	store  SignoffStore
	logger *slog.Logger
}

func NewSignoffsHandler(signoffs SignoffStore, logger *slog.Logger) *SignoffsHandler {
	return &SignoffsHandler{store: signoffs, logger: logger}
}

// Routes monte les routes de validation.
//
// Se prononcer est une décision de livraison : `release:edit`, la même permission
// que les actions manuelles, parce que ce sont les mêmes personnes qui conduisent
// une recette. Exiger `release:create:final` réserverait la validation à ceux qui
// livrent, alors que valider et livrer sont les deux gestes qu'on souhaite voir
// accomplis par des personnes distinctes.
func (h *SignoffsHandler) Routes(mux *http.ServeMux, m *dashhttp.Middleware) {
	module := m.RequireModule(plugin.ModuleMSE)

	mux.Handle("GET /projects/{project}/releases/{tag}/signoffs", dashhttp.Chain(http.HandlerFunc(h.List),
		m.Authenticate, module, m.RequireIn(auth.PermRead)))
	mux.Handle("PUT /projects/{project}/releases/{tag}/signoffs/{env}", dashhttp.Chain(http.HandlerFunc(h.Set),
		m.Authenticate, module, m.CSRF, m.RequireIn(auth.PermReleaseEdit)))
	mux.Handle("DELETE /projects/{project}/releases/{tag}/signoffs/{env}", dashhttp.Chain(http.HandlerFunc(h.Clear),
		m.Authenticate, module, m.CSRF, m.RequireIn(auth.PermReleaseEdit)))
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

// release lit la release du chemin, ou écrit la réponse.
func (h *SignoffsHandler) release(w http.ResponseWriter, r *http.Request) (store.Release, bool) {
	projectID, ok := dashhttp.RequestProject(w, r, h.store)
	if !ok {
		return store.Release{}, false
	}
	release, err := h.store.GetRelease(r.Context(), projectID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			dashhttp.WriteProblem(w, http.StatusNotFound, "Release introuvable", "")
			return store.Release{}, false
		}
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture de la release impossible", "")
		return store.Release{}, false
	}
	return release, true
}

// List rend les validations d'une release, groupées par environnement.
//
// Tous les environnements sont rendus, y compris ceux sans avis : l'interface doit
// distinguer « personne ne s'est prononcé » de « cet environnement n'existe pas »,
// et l'attente est précisément l'information utile en recette.
func (h *SignoffsHandler) List(w http.ResponseWriter, r *http.Request) {
	release, ok := h.release(w, r)
	if !ok {
		return
	}

	environments, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des environnements impossible", "")
		return
	}

	signoffs, err := h.store.ListSignoffs(r.Context(), release.ID)
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture des validations impossible", "")
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
		entries := byEnvironment[environment.ID]
		if entries == nil {
			// Un tableau vide et non `null` : l'interface itère dessus sans garde.
			entries = []signoffVw{}
		}
		payload = append(payload, environmentSignoffsVw{
			Environment: environment.ID, Label: environment.Label, Signoffs: entries,
		})
	}

	dashhttp.WriteJSON(w, http.StatusOK, map[string]any{"environments": payload})
}

type signoffRequest struct {
	Role   string `json:"role"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// signoffStatuses borne les avis enregistrables.
//
// `na` en est absent : l'attente est l'absence de ligne, pas une ligne qui dit
// « en attente ». L'accepter créerait deux représentations du même état.
var signoffStatuses = map[string]bool{"pass": true, "warn": true, "fail": true}

// Set enregistre l'avis d'un intervenant sur une release dans un environnement.
func (h *SignoffsHandler) Set(w http.ResponseWriter, r *http.Request) {
	identity, ok := dashhttp.IdentityFrom(r.Context())
	if !ok {
		dashhttp.WriteProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request signoffRequest
	// 16 Kio : un motif de refus est une phrase, pas un rapport.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
		dashhttp.WriteProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	role := strings.TrimSpace(request.Role)
	detail := strings.TrimSpace(request.Detail)

	if role == "" {
		dashhttp.WriteProblem(w, http.StatusBadRequest, "Rôle absent",
			"Précisez qui se prononce : « Client », « Recette », « Exploitation ».")
		return
	}
	if !signoffStatuses[request.Status] {
		dashhttp.WriteProblem(w, http.StatusBadRequest, "Avis invalide",
			"L'avis vaut « pass », « warn » ou « fail ». L'attente s'obtient en retirant la validation.")
		return
	}
	// Une réserve ou un refus sans motif ne dit rien à qui le lit ensuite. Exigé
	// par l'API et non par la base : la règle peut évoluer, les données écrites non.
	if request.Status != "pass" && detail == "" {
		dashhttp.WriteProblem(w, http.StatusBadRequest, "Motif absent",
			"Une réserve ou un refus doit indiquer son motif.")
		return
	}

	release, ok := h.release(w, r)
	if !ok {
		return
	}

	environmentID := r.PathValue("env")
	exists, err := h.store.SignoffEnvironmentExists(r.Context(), environmentID)
	if err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Lecture de l'environnement impossible", "")
		return
	}
	if !exists {
		dashhttp.WriteProblem(w, http.StatusNotFound, "Environnement introuvable", environmentID)
		return
	}

	if err := h.store.SetSignoff(r.Context(), release.ID, environmentID, role,
		request.Status, detail, identity.Subject); err != nil {
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	// Le journal porte l'avis complet, motif inclus : la table ne garde que le
	// dernier état, et « qui avait validé avant de se rétracter » n'a de réponse
	// qu'ici.
	h.audit(r, identity, store.AuditSignoffSet, release.Tag, map[string]any{
		"environment": environmentID, "role": role, "status": request.Status, "detail": detail,
	})

	dashhttp.WriteJSON(w, http.StatusNoContent, nil)
}

// Clear retire un avis et remet le rôle en attente.
//
// Retirer n'est pas refuser : un avis rendu sur la mauvaise release doit pouvoir
// disparaître sans laisser un refus qui bloquerait la livraison.
func (h *SignoffsHandler) Clear(w http.ResponseWriter, r *http.Request) {
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	if role == "" {
		dashhttp.WriteProblem(w, http.StatusBadRequest, "Rôle absent",
			"Précisez le rôle dont l'avis doit être retiré.")
		return
	}

	release, ok := h.release(w, r)
	if !ok {
		return
	}

	environmentID := r.PathValue("env")
	if err := h.store.DeleteSignoff(r.Context(), release.ID, environmentID, role); err != nil {
		if errors.Is(err, store.ErrSignoffNotFound) {
			dashhttp.WriteProblem(w, http.StatusNotFound, "Validation introuvable", "")
			return
		}
		dashhttp.WriteProblem(w, http.StatusInternalServerError, "Retrait impossible", "")
		return
	}

	if identity, ok := dashhttp.IdentityFrom(r.Context()); ok {
		h.audit(r, identity, store.AuditSignoffClear, release.Tag, map[string]any{
			"environment": environmentID, "role": role,
		})
	}

	dashhttp.WriteJSON(w, http.StatusNoContent, nil)
}

// audit consigne une décision sans jamais faire échouer celle-ci.
func (h *SignoffsHandler) audit(
	r *http.Request, identity auth.Identity, action, target string, payload map[string]any,
) {
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		h.logger.Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
