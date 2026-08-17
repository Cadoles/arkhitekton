package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Administration de la synchronisation Redmine.
 *
 * Trois choses à surveiller, et aucune n'est une panne du dashboard :
 *
 *   - les statuts sans projection déclarée, qui font dériver le taux de complétion
 *     sans rien signaler ;
 *   - les versions dont plusieurs noms se réduisent au même numéro, qui empêchent
 *     de rattacher une release automatiquement ;
 *   - les versions provisoires jamais adoptées, symptôme d'une faute de frappe dans
 *     un tag ou d'une version absente de Redmine.
 *
 * Les afficher est ce qui les rend corrigeables. Les taire les laisserait fausser
 * l'écran des versions en silence.
 */

// SyncAdminStore est ce que l'administration de la synchronisation attend de la base.
type SyncAdminStore interface {
	GetSyncState(ctx context.Context, source string) (store.SyncState, error)
	ResetSyncCursor(ctx context.Context, source string) error
	UnmappedStatuses(ctx context.Context) ([]store.UnmappedStatus, error)
	AmbiguousVersions(ctx context.Context) ([]store.AmbiguousVersion, error)
	PendingVersions(ctx context.Context) ([]string, error)
	StatusMapping(ctx context.Context) (map[string]string, error)
	SetStatusBucket(ctx context.Context, statusName, bucket string) error
	ReprojectIssues(ctx context.Context) (int64, error)
	Audit(ctx context.Context, actor, action, target string, payload map[string]any) error
}

// SyncAdminHandler expose l'état des synchronisations et leurs correctifs.
type SyncAdminHandler struct {
	store      SyncAdminStore
	configured bool
}

// NewSyncAdminHandler construit le handler.
//
// `configured` vient de la configuration : sans clé Redmine, l'écran doit dire « non
// configuré » plutôt que « jamais synchronisé », qui ferait chercher une panne.
func NewSyncAdminHandler(admin SyncAdminStore, configured bool) *SyncAdminHandler {
	return &SyncAdminHandler{store: admin, configured: configured}
}

// State rend l'état de la synchronisation et ce qui demande une décision humaine.
func (h *SyncAdminHandler) State(w http.ResponseWriter, r *http.Request) {
	state, err := h.store.GetSyncState(r.Context(), "redmine")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de l'état impossible", "")
		return
	}

	unmapped, err := h.store.UnmappedStatuses(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des statuts impossible", "")
		return
	}

	ambiguous, err := h.store.AmbiguousVersions(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des versions impossible", "")
		return
	}

	pending, err := h.store.PendingVersions(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture des versions provisoires impossible", "")
		return
	}

	mapping, err := h.store.StatusMapping(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lecture de la projection impossible", "")
		return
	}

	unmappedPayload := make([]map[string]any, 0, len(unmapped))
	for _, entry := range unmapped {
		unmappedPayload = append(unmappedPayload, map[string]any{
			"status_name": entry.StatusName, "issues": entry.Issues,
		})
	}

	ambiguousPayload := make([]map[string]any, 0, len(ambiguous))
	for _, entry := range ambiguous {
		ambiguousPayload = append(ambiguousPayload, map[string]any{
			"normalized": entry.Normalized, "names": entry.Names,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"configured": h.configured,
		"sync":       syncStateView(state),
		"unmapped":   unmappedPayload,
		"ambiguous":  ambiguousPayload,
		"pending":    orEmptyStrings(pending),
		"mapping":    mapping,
	})
}

func orEmptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// Resync efface le curseur pour que la passe suivante relise tout.
//
// N'interroge pas Redmine ici : la synchronisation vit dans le `worker`, sous le bail
// de leader, et l'`api` n'a pas de chemin vers elle. Effacer le curseur est le signal,
// et la passe suivante — au plus tard cinq minutes après — relira l'ensemble.
func (h *SyncAdminHandler) Resync(w http.ResponseWriter, r *http.Request) {
	if err := h.store.ResetSyncCursor(r.Context(), "redmine"); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Réinitialisation impossible", "")
		return
	}

	h.audit(r, "sync.redmine.reset", "redmine", nil)

	writeJSON(w, http.StatusAccepted, map[string]any{
		// Le message dit l'attente : sans lui, un utilisateur qui ne voit rien
		// changer relancerait en boucle.
		"message": "Curseur réinitialisé. La prochaine synchronisation relira l'ensemble, dans les cinq minutes.",
	})
}

type statusMappingRequest struct {
	Bucket string `json:"bucket"`
}

// Les seaux admis viennent du magasin : une seconde énumération ici divergerait de
// la contrainte de la base au premier ajout.

// SetMapping déclare la projection d'un statut, puis la réapplique.
//
// La réapplication est faite dans le même geste, et ce n'est pas un détail : sans
// elle, la nouvelle règle ne vaudrait que pour les demandes modifiées ensuite dans
// Redmine, et l'écran mélangerait deux règles sans le dire.
func (h *SyncAdminHandler) SetMapping(w http.ResponseWriter, r *http.Request) {
	statusName := strings.TrimSpace(r.PathValue("status"))
	if statusName == "" {
		writeProblem(w, http.StatusBadRequest, "Statut absent", "")
		return
	}

	var request statusMappingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "Requête illisible", "")
		return
	}
	if !store.ValidBucket(request.Bucket) {
		writeProblem(w, http.StatusBadRequest, "Seau invalide",
			"Le seau vaut « todo », « doing », « client-test », « done » ou « rejected ».")
		return
	}

	if err := h.store.SetStatusBucket(r.Context(), statusName, request.Bucket); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Enregistrement impossible", "")
		return
	}

	moved, err := h.store.ReprojectIssues(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Réapplication impossible", "")
		return
	}

	// Le journal porte le déplacement : changer la projection d'un statut déplace un
	// taux de complétion affiché au client, ce qui mérite une trace.
	h.audit(r, "sync.redmine.mapping", statusName, map[string]any{
		"bucket": request.Bucket, "issues_moved": moved,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status_name": statusName, "bucket": request.Bucket, "issues_moved": moved,
	})
}

func (h *SyncAdminHandler) audit(r *http.Request, action, target string, payload map[string]any) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return
	}
	if err := h.store.Audit(r.Context(), identity.Subject, action, target, payload); err != nil {
		slog.Default().Error("journal d'audit", "action", action, "target", target, "err", err)
	}
}
