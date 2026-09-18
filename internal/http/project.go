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

// projectResolver translates the name a URL carries into a project.
//
// Separate from [projectSource]: resolving by path is what every project-scoped handler
// needs, whereas falling back to the buildable project now concerns only those with no path
// to hand. Merging them would force the former to carry a method they never call.
type projectResolver interface {
	ProjectBySlug(ctx context.Context, slug string) (store.Project, error)
}

// projectLister lists the declared projects, so the interface can offer a choice rather than
// endure one.
type projectLister interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
}

// projectSource can in addition name the project being built.
type projectSource interface {
	projectResolver
	BuildableProject(ctx context.Context) (store.Project, error)
}

// requestProject resolves the project the path names.
//
// It writes the response and returns false when there is none: 404 on an unknown slug — a
// stale URL is not a fault, and answering 500 would send someone hunting for an incident.
func requestProject(w http.ResponseWriter, r *http.Request, source projectResolver) (int64, bool) {
	slug := r.PathValue("project")
	if slug == "" {
		writeProblem(w, http.StatusBadRequest, "No project named",
			"this route expects a project in its path")
		return 0, false
	}

	project, err := source.ProjectBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown project", slug)
			return 0, false
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
		return 0, false
	}
	return project.ID, true
}

// requestProjectRecord returns the whole project the path names.
//
// Distinct from [requestProject], which returns only its key: now that the version scheme
// and the release cycle are declared per project, creating a release needs the row and not
// its identifier. Both coexist because most reads need only the key, and loading the whole
// project everywhere would pay for a join for nothing.
func requestProjectRecord(
	w http.ResponseWriter, r *http.Request, source projectResolver,
) (store.Project, error) {
	slug := r.PathValue("project")
	if slug == "" {
		writeProblem(w, http.StatusBadRequest, "No project named",
			"this route expects a project in its path")
		return store.Project{}, errors.New("no project named")
	}

	project, err := source.ProjectBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeProblem(w, http.StatusNotFound, "Unknown project", slug)
			return store.Project{}, err
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the project", "")
		return store.Project{}, err
	}
	return project, nil
}

// buildableProject returns the project being built.
//
// The fallback for reads with no project in their path. One today; several candidates are
// refused by the store rather than settled at random.
func buildableProject(ctx context.Context, source projectSource) (int64, error) {
	project, err := source.BuildableProject(ctx)
	if err != nil {
		return 0, fmt.Errorf("projet constructible: %w", err)
	}
	return project.ID, nil
}
