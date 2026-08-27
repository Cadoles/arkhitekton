package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/auth"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

// AdminStore est ce que l'administration attend de la base.
type AdminStore interface {
	ListUserRoles(ctx context.Context) ([]store.UserRole, error)
	// SetRoleIn attribue un rôle dans un projet, ou partout quand il est nil.
	SetRoleIn(ctx context.Context, subject, username, role string, projectID *int64, grantedBy string) error
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	// RevokeRoleIn retire le rôle d'une portée. La portée est obligatoire dans le
	// raisonnement même quand elle est nulle : effacer toutes les lignes d'un
	// sujet supprimerait, en voulant lui retirer un projet, le rôle global qui
	// vaut partout ailleurs.
	RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error
	ListAudit(ctx context.Context, target string, limit int) ([]store.AuditEntry, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
	Directory(ctx context.Context) (map[string]string, error)
}

// AdminHandler expose l'attribution des rôles et le journal d'audit.
type AdminHandler struct{ store AdminStore }

func NewAdminHandler(admin AdminStore) *AdminHandler { return &AdminHandler{store: admin} }

type userRolePayload struct {
	Subject  string `json:"subject"`
	Username string `json:"username,omitempty"`
	Role     string `json:"role"`
	// GrantedBy vaut `bootstrap` pour les comptes promus par la configuration.
	// L'interface le signale : retirer un tel rôle sans toucher à la
	// configuration le fait réapparaître au redémarrage suivant.
	GrantedBy string    `json:"granted_by"`
	GrantedAt time.Time `json:"granted_at"`
	// Project nomme la portée. Vide pour un rôle global — la grande majorité.
	Project string `json:"project,omitempty"`
	// ProjectLabel est son libellé lisible, pour ne pas afficher un slug seul.
	ProjectLabel string     `json:"project_label,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	Sessions     int        `json:"sessions"`
}

// Roles rend les rôles attribués et ce qu'ils permettent.
func (h *AdminHandler) Roles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.store.ListUserRoles(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des rôles impossible", "")
		return
	}

	who := resolveActors(r.Context(), h.store, slog.Default())

	payload := make([]userRolePayload, 0, len(roles))
	for _, role := range roles {
		payload = append(payload, userRolePayload{
			Subject: role.Subject, Username: role.Username, Role: role.Role,
			GrantedBy: who.name(role.GrantedBy), GrantedAt: role.GrantedAt,
			Project: role.ProjectSlug, ProjectLabel: role.ProjectLabel,
			LastSeen: role.LastSeen, Sessions: role.Sessions,
		})
	}

	// Le catalogue des rôles accompagne la liste : l'interface doit proposer les
	// valeurs possibles sans les coder en dur, et ce qu'un rôle permet est décidé
	// par `internal/auth`.
	catalogue := make([]map[string]any, 0, len(auth.AllRoles))
	for _, role := range auth.AllRoles {
		catalogue = append(catalogue, map[string]any{
			"role":        string(role),
			"permissions": auth.PermissionsOf(role),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"roles": payload, "catalogue": catalogue})
}

type grantRequest struct {
	// Subject est l'identifiant du fournisseur d'identité, pas le nom affiché :
	// c'est lui qui apparaît dans le jeton, et c'est sur lui que porte
	// l'autorisation. Un nom d'utilisateur change, un sujet non.
	Subject  string `json:"subject"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Project restreint l'attribution à un projet, nommé par son slug. Vide, le
	// rôle vaut partout — ce qui reste le cas courant, et ce que toutes les
	// attributions antérieures signifient.
	Project string `json:"project,omitempty"`
}

// Grant attribue un rôle.
func (h *AdminHandler) Grant(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Non authentifié", "")
		return
	}

	var request grantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}

	request.Subject = strings.TrimSpace(request.Subject)
	if request.Subject == "" {
		writeProblem(w, http.StatusBadRequest, "Sujet requis",
			"l'identifiant du fournisseur d'identité, et non le nom affiché")
		return
	}

	role, err := auth.ParseRole(request.Role)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Rôle inconnu", err.Error())
		return
	}

	// La portée est facultative : sans elle, le rôle vaut partout, ce qui reste
	// le cas courant. Un projet nommé restreint l'attribution à lui seul.
	var projectID *int64
	if slug := strings.TrimSpace(request.Project); slug != "" {
		project, err := h.store.ProjectBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, store.ErrProjectNotFound) {
				writeProblem(w, http.StatusNotFound, "Projet inconnu", slug)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
			return
		}
		projectID = &project.ID
	}

	err = h.store.SetRoleIn(r.Context(), request.Subject,
		strings.TrimSpace(request.Username), string(role), projectID, identity.Subject)
	if err != nil && projectID != nil && role == auth.RoleAdmin {
		writeProblem(w, http.StatusUnprocessableEntity, "Portée impossible",
			"le rôle d'administrateur gouverne les comptes et les dépôts, qui n'appartiennent à aucun projet")
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			// 409 et non 403 : la requête est légitime et l'appelant a le droit,
			// c'est l'état du système qui l'interdit.
			writeProblem(w, http.StatusConflict, "Dernier administrateur", err.Error())
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Attribution impossible", "")
		return
	}

	h.audit(r, store.AuditRoleGrant, request.Subject, map[string]any{
		"role": string(role), "username": request.Username,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"subject": request.Subject, "role": string(role),
		// Le changement ferme les sessions de l'intéressé : le dire évite qu'un
		// administrateur croie l'ancien rôle encore en vigueur, ou l'inverse.
		"sessions_closed": true,
	})
}

// Revoke retire le rôle d'un utilisateur.
func (h *AdminHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	subject := r.PathValue("subject")

	// La portée vient de la requête : retirer « le rôle de quelqu'un » n'a plus
	// de sens unique depuis qu'il peut en avoir un par projet.
	var projectID *int64
	if slug := strings.TrimSpace(r.URL.Query().Get("project")); slug != "" {
		project, err := h.store.ProjectBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, store.ErrProjectNotFound) {
				writeProblem(w, http.StatusNotFound, "Projet inconnu", slug)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
			return
		}
		projectID = &project.ID
	}

	if err := h.store.RevokeRoleIn(r.Context(), subject, projectID); err != nil {
		switch {
		case errors.Is(err, store.ErrRoleNotFound):
			writeProblem(w, http.StatusNotFound, "Rôle introuvable", "")
		case errors.Is(err, store.ErrLastAdmin):
			writeProblem(w, http.StatusConflict, "Dernier administrateur", err.Error())
		default:
			writeProblem(w, http.StatusInternalServerError, "Retrait impossible", "")
		}
		return
	}

	h.audit(r, store.AuditRoleRevoke, subject, nil)
	writeJSON(w, http.StatusOK, map[string]any{"subject": subject, "sessions_closed": true})
}

type auditPayload struct {
	ID      int64          `json:"id"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Target  string         `json:"target,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
	At      time.Time      `json:"at"`
}

// Audit rend le journal.
//
// Filtrable par cible : c'est la lecture qu'on fait réellement — « qu'est-il
// arrivé à cette release » — et l'index existe pour elle.
func (h *AdminHandler) Audit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	entries, err := h.store.ListAudit(r.Context(), r.URL.Query().Get("target"), limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture du journal impossible", "")
		return
	}

	who := resolveActors(r.Context(), h.store, slog.Default())

	payload := make([]auditPayload, 0, len(entries))
	for _, entry := range entries {
		payload = append(payload, auditPayload{
			ID: entry.ID, Actor: who.name(entry.Actor), Action: entry.Action,
			Target: entry.Target, Payload: entry.Payload, At: entry.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": payload})
}

// audit consigne une action d'administration sans la faire échouer.
//
// Même règle qu'ailleurs : le journal est une trace, pas une transaction. Le
// rôle, lui, est déjà attribué.
func (h *AdminHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
