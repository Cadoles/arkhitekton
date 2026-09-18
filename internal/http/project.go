package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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

// projectResolver traduit le nom porté par une URL en projet.
//
// Séparé de [projectSource] : la résolution par chemin est ce dont **tous** les
// handlers rattachés à un projet ont besoin, alors que le repli sur le projet
// constructible ne concerne plus que ceux qui n'ont pas de chemin à leur
// disposition. Les fondre obligerait les premiers à porter une méthode qu'ils
// n'appellent pas.
type projectResolver interface {
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// projectLister énumère les projets déclarés, pour que l'interface propose un
// choix plutôt que d'en subir un.
type projectLister interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
}

// projectSource sait en plus nommer le projet qui se construit.
type projectSource interface {
	projectResolver
	BuildableProject(ctx context.Context) (store.Project, error)
}

// requestProject résout le projet désigné par le chemin.
//
// Écrit la réponse et rend `false` quand il n'y en a pas : 404 sur un slug
// inconnu — une URL périmée n'est pas une panne, et répondre 500 ferait chercher
// un incident.
func requestProject(w http.ResponseWriter, r *http.Request, source projectResolver) (int64, bool) {
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

// requestProjectRecord rend le projet **entier** désigné par le chemin.
//
// Distinct de [requestProject], qui n'en rend que la clé : depuis que le schéma
// de version et le cycle de release sont déclarés par projet, la création d'une
// release a besoin de la ligne et non de son identifiant. Les deux coexistent
// parce que la plupart des lectures n'ont besoin que de la clé, et charger le
// projet entier partout ferait payer une jointure pour rien.
func requestProjectRecord(
	w http.ResponseWriter, r *http.Request, source projectResolver,
) (store.Project, error) {
	slug := r.PathValue("project")
	if slug == "" {
		writeProblem(w, http.StatusBadRequest, "Projet non désigné",
			"cette route attend un projet dans son chemin")
		return store.Project{}, errors.New("projet non désigné")
	}

	project, err := source.ProjectBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Projet inconnu", slug)
			return store.Project{}, err
		}
		writeProblem(w, http.StatusInternalServerError, "Lecture du projet impossible", "")
		return store.Project{}, err
	}
	return project, nil
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
