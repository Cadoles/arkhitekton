package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * D'où vient le projet dont un tag fait foi.
 *
 * Depuis la migration 0025, une release et une version n'existent que **dans** un
 * projet : le tag `1.8.0` n'est unique qu'en son sein, et le lire sans dire lequel
 * rendrait la release d'un autre dès qu'un second existera.
 *
 * Les routes le portent donc : `/projects/{projet}/releases/{tag}`. Le segment est
 * le slug du projet et non son identifiant — ces URL finissent en favori et dans
 * des tickets, et un numéro n'y dirait rien de ce qu'il désigne.
 *
 * Le repli sur le projet constructible subsiste pour les lectures qui n'ont pas de
 * chemin à leur disposition — les suggestions du formulaire de lancement, par
 * exemple, qui portent sur la file et non sur un projet. Il est nommé pour être
 * trouvable : `grep buildableProject` rend la liste de ce qui reste à adresser
 * (doc 12 §12.9, lot B).
 */

// projectSource est ce que les handlers savent tous faire : nommer un projet.
type projectSource interface {
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
	BuildableProject(ctx context.Context) (store.Project, error)
}

// requestProject résout le projet désigné par le chemin.
//
// Écrit la réponse et rend `false` quand il n'y en a pas : 404 sur un slug
// inconnu — une URL périmée n'est pas une panne, et répondre 500 ferait chercher
// un incident.
func requestProject(w http.ResponseWriter, r *http.Request, source projectSource) (int64, bool) {
	slug := r.PathValue("project")
	if slug == "" {
		writeProblem(w, http.StatusBadRequest, "Projet non désigné",
			"cette route attend un projet dans son chemin")
		return 0, false
	}

	project, err := source.ProjectBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet inconnu", slug)
			return 0, false
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
		return 0, false
	}
	return project.ID, true
}

// buildableProject rend le projet qui se construit.
//
// Repli des lectures qui n'ont pas de projet dans leur chemin. Un seul
// aujourd'hui ; plusieurs candidats sont refusés par le magasin plutôt que
// départagés au hasard.
func buildableProject(ctx context.Context, source projectSource) (int64, error) {
	project, err := source.BuildableProject(ctx)
	if err != nil {
		return 0, fmt.Errorf("projet constructible: %w", err)
	}
	return project.ID, nil
}
