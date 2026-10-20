package http

import (
	"context"
	"net/http"

	"golang.org/x/sync/errgroup"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// ForgeReader is what the references route needs from the forge.
type ForgeReader interface {
	DefaultBranch(ctx context.Context) (string, error)
	ListBranches(ctx context.Context) ([]provider.Ref, bool, error)
	ListTags(ctx context.Context) ([]provider.Ref, bool, error)
}

// ForgeHandler serves a project repository's branches and tags.
//
// It exists for the release creation form: `git_ref` must point at a real branch or tag,
// and a free-text field postpones the typo to build time — the worst moment to discover it.
type ForgeHandler struct {
	projects projectResolver
	// forges resolves the project's forge. When none is configured, the route answers
	// `configured: false` and the form falls back to free-text input: a missing token must not
	// prevent creating a release.
	forges Forges
}

func NewForgeHandler(projects projectResolver, forges Forges) *ForgeHandler {
	return &ForgeHandler{projects: projects, forges: forges}
}

type refVw struct {
	Name string `json:"name"`
	SHA  string `json:"sha,omitempty"`
}

// Refs returns the default branch, the branches and the tags.
//
// Read live, with no server-side cache: branches move at the developers' pace, and the call
// happens only when the form opens. The client caches its own answer for the duration of
// typing.
func (h *ForgeHandler) Refs(w http.ResponseWriter, r *http.Request) {
	projectID, ok := requestProject(w, r, h.projects)
	if !ok {
		return
	}
	var forge ForgeReader
	if h.forges != nil {
		resolved, err := h.forges.Forge(r.Context(), projectID)
		switch {
		case err == nil:
			forge = resolved
		case !notConfigured(err):
			writeProblem(w, http.StatusBadGateway, "Forge unreachable", err.Error())
			return
		}
	}
	if forge == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"branches":   []refVw{},
			"tags":       []refVw{},
		})
		return
	}

	// The three reads in parallel. In sequence, against the real repository, they cost eight
	// seconds — paginating the tags alone is worth several — and eight seconds of waiting on a
	// form reads as an empty list.
	var (
		defaultBranch                    string
		branches, tags                   []provider.Ref
		branchesTruncated, tagsTruncated bool
	)
	group, groupCtx := errgroup.WithContext(r.Context())
	group.Go(func() (err error) {
		defaultBranch, err = forge.DefaultBranch(groupCtx)
		return err
	})
	group.Go(func() (err error) {
		branches, branchesTruncated, err = forge.ListBranches(groupCtx)
		return err
	})
	group.Go(func() (err error) {
		tags, tagsTruncated, err = forge.ListTags(groupCtx)
		return err
	})
	if err := group.Wait(); err != nil {
		// 502 and the client's message: the fault is with the forge or with the token, not in the
		// dashboard, and the form must be able to say so.
		writeProblem(w, http.StatusBadGateway, "Forge unreachable", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"configured":     true,
		"default_branch": defaultBranch,
		"branches":       refViews(branches),
		"tags":           refViews(tags),
		// Truncation is stated: a silently incomplete list reads as exhaustive, and the missing
		// reference would look as though it did not exist.
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

// What this package asks of the forge is a subset of what being a forge means. The
// assertion proves it at compile time, and that is its point: a method specific to one
// forge slipping into one of these interfaces would break nothing at runtime — the concrete
// client carries it — but would silently make the dashboard unfit for any other forge. Here
// it does not compile.
var (
	_ ForgeReader    = provider.Forge(nil)
	_ CommitResolver = provider.Forge(nil)
	_ Tagger         = provider.TagWriter(nil)
	_ ProjectProber  = provider.RepositoryProber(nil)
)
