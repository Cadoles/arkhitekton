package http

import (
	"context"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"net/http"

	"golang.org/x/sync/errgroup"
)

// ForgeReader est ce que la route des références attend de la forge.
type ForgeReader interface {
	DefaultBranch(ctx context.Context) (string, error)
	ListBranches(ctx context.Context) ([]provider.Ref, bool, error)
	ListTags(ctx context.Context) ([]provider.Ref, bool, error)
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

	// Les trois lectures en parallèle. En séquence, dépôt réel à l'appui, elles
	// coûtaient huit secondes — la pagination des tags à elle seule en vaut
	// plusieurs — et huit secondes d'attente sur un formulaire se lisent comme une
	// liste vide.
	var (
		defaultBranch                    string
		branches, tags                   []provider.Ref
		branchesTruncated, tagsTruncated bool
	)
	group, groupCtx := errgroup.WithContext(r.Context())
	group.Go(func() (err error) {
		defaultBranch, err = h.forge.DefaultBranch(groupCtx)
		return err
	})
	group.Go(func() (err error) {
		branches, branchesTruncated, err = h.forge.ListBranches(groupCtx)
		return err
	})
	group.Go(func() (err error) {
		tags, tagsTruncated, err = h.forge.ListTags(groupCtx)
		return err
	})
	if err := group.Wait(); err != nil {
		// 502 et le message du client : la panne est chez la forge ou dans le
		// jeton, pas dans le dashboard, et le formulaire doit pouvoir le dire.
		writeProblem(w, http.StatusBadGateway, "Forge injoignable", err.Error())
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

func refViews(refs []provider.Ref) []refVw {
	views := make([]refVw, 0, len(refs))
	for _, ref := range refs {
		views = append(views, refVw{Name: ref.Name, SHA: ref.SHA})
	}
	return views
}

// Ce que ce paquet demande à la forge est un sous-ensemble de ce qu'être une
// forge veut dire. L'assertion le prouve à la compilation, et c'est là son
// intérêt : une méthode propre à Gitea qui entrerait dans une de ces interfaces
// ne casserait rien à l'exécution — le client concret la porte — mais rendrait
// silencieusement le dashboard inapte à toute autre forge. Ici, elle ne compile
// pas (doc 12 §12.9, chantier 4).
var (
	_ ForgeReader    = provider.Forge(nil)
	_ CommitResolver = provider.Forge(nil)
	_ Tagger         = provider.TagWriter(nil)
	_ ProjectProber  = provider.RepositoryProber(nil)
)
