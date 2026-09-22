package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Where the project a tag is authoritative in comes from.
 *
 * Since the migration, a release and a version exist only **within** a project: the tag `1.8.0`
 * is unique only inside it, and reading it without saying which would return another's release as
 * soon as a second exists.
 *
 * The routes therefore carry it: `/projects/{project}/releases/{tag}`. The segment is the
 * project's slug and not its identifier — these URLs end up in bookmarks and in tickets, and a
 * number would say nothing there about what it names.
 *
 * The fallback to the buildable project survives for the reads that have no path at their
 * disposal — the launch form's suggestions, for instance, which bear on the queue and not on a
 * project. It is named so that it can be found: `grep buildableProject` returns the list of what
 * remains to be addressed.
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
