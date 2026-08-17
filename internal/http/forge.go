package http

import (
	"context"
	"net/http"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
)

// ForgeReader est ce que la route des références attend de la forge.
type ForgeReader interface {
	DefaultBranch(ctx context.Context) (string, error)
	ListBranches(ctx context.Context) ([]gitea.Ref, bool, error)
	ListTags(ctx context.Context) ([]gitea.Ref, bool, error)
}

// ForgeHandler sert les branches et les tags du dépôt de l'application.
//
// Il existe pour le formulaire de création de release : `git_ref` doit pointer
// une branche ou un tag réels, et un champ libre reporte la faute de frappe au
// moment du build — le pire moment pour la découvrir.
type ForgeHandler struct {
	// forge est nul quand l'accès n'est pas configuré. La route répond alors
	// `configured: false` et le formulaire retombe sur la saisie libre : l'absence
	// de jeton ne doit pas empêcher de créer une release.
	forge ForgeReader
}

func NewForgeHandler(forge ForgeReader) *ForgeHandler {
	return &ForgeHandler{forge: forge}
}

type refVw struct {
	Name string `json:"name"`
	SHA  string `json:"sha,omitempty"`
}

// Refs rend la branche par défaut, les branches et les tags.
//
// Lecture en direct, sans cache serveur : les branches bougent au rythme des
// développeurs, et l'appel n'a lieu qu'à l'ouverture du formulaire. Le client met
// sa propre réponse en cache le temps de la saisie.
func (h *ForgeHandler) Refs(w http.ResponseWriter, r *http.Request) {
	if h.forge == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"branches":   []refVw{},
			"tags":       []refVw{},
		})
		return
	}

	defaultBranch, err := h.forge.DefaultBranch(r.Context())
	if err != nil {
		// 502 et le message du client : la panne est chez la forge ou dans le
		// jeton, pas dans le dashboard, et le formulaire doit pouvoir le dire.
		writeProblem(w, http.StatusBadGateway, "Forge injoignable", err.Error())
		return
	}

	branches, branchesTruncated, err := h.forge.ListBranches(r.Context())
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Lecture des branches impossible", err.Error())
		return
	}
	tags, tagsTruncated, err := h.forge.ListTags(r.Context())
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Lecture des tags impossible", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"configured":     true,
		"default_branch": defaultBranch,
		"branches":       refViews(branches),
		"tags":           refViews(tags),
		// La troncature est dite : une liste silencieusement incomplète se lit
		// comme exhaustive, et la référence absente semblerait ne pas exister.
		"truncated": branchesTruncated || tagsTruncated,
	})
}

func refViews(refs []gitea.Ref) []refVw {
	views := make([]refVw, 0, len(refs))
	for _, ref := range refs {
		views = append(views, refVw{Name: ref.Name, SHA: ref.SHA})
	}
	return views
}
