package configure

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Cadoles/arkhitekton/internal/release"
	"github.com/Cadoles/arkhitekton/internal/store"
	"github.com/Cadoles/arkhitekton/internal/version"
)

// ProjectStore is what declaring a project needs from the database.
type ProjectStore interface {
	CreateProject(ctx context.Context, project store.Project) (int64, error)
	UpdateProject(ctx context.Context, project store.Project) error
	Auditor
}

// ProjectInput is a project as an administrator declares it.
type ProjectInput struct {
	Repository string
	Label      string
	ForgeURL   string
	// Slug names the project in URLs. Empty: derived from the repository.
	Slug string
	// VersionScheme names the shape of the project's tags. Empty means `semver`.
	VersionScheme string
	// ReleaseKinds declares the release cycle. Empty: the default at creation, the recorded cycle
	// at a correction.
	ReleaseKinds release.Cycle
}

// Projects declares and corrects the repositories the dashboard drives.
type Projects struct {
	Store ProjectStore
	// ForgeURL is the deployment's default, used when a project names none.
	ForgeURL string
	// SchemeCheck refuses a version scheme the deployer cannot follow. Nil: no deployer objects.
	SchemeCheck func(context.Context, version.Scheme) error
	Logger      *slog.Logger
}

// repositorySeparator splits `owner/name`, which the forge API expects. Checking the shape here
// rather than at the first call avoids an access check failing on a malformed URL and reading as a
// rights problem.
const repositorySeparator = "/"

// Normalise fills the defaults and refuses what cannot be driven. It is what the screen's form and
// a configuration file go through before anything is written.
func (s Projects) Normalise(ctx context.Context, input ProjectInput) (ProjectInput, error) {
	input.Repository = strings.Trim(strings.TrimSpace(input.Repository), "/")
	owner, name, found := strings.Cut(input.Repository, repositorySeparator)
	if !found || owner == "" || name == "" || strings.Contains(name, repositorySeparator) {
		return input, refuse(http.StatusBadRequest, "Invalid repository", "expected owner/name")
	}

	input.Label = strings.TrimSpace(input.Label)
	if input.Label == "" {
		// The repository's name makes an acceptable label, and requiring one would stumble over a
		// field whose value is obvious.
		input.Label = name
	}

	input.ForgeURL = strings.TrimRight(strings.TrimSpace(input.ForgeURL), "/")
	if input.ForgeURL == "" {
		input.ForgeURL = strings.TrimRight(s.ForgeURL, "/")
	}

	input.Slug = strings.TrimSpace(input.Slug)

	// The scheme and deployer coupling is refused here, at configuration time, and not discovered
	// at the first deployment that does not leave.
	//
	// It is the failure mode of a GitOps controller selecting by SemVer range: with an opaque
	// scheme the promotion tag would be pushed, accepted by the registry, ignored by the
	// controller; the promotion would look successful and nothing would move.
	input.VersionScheme = strings.TrimSpace(input.VersionScheme)
	scheme, err := version.For(input.VersionScheme)
	if err != nil {
		return input, invalid("Unknown version scheme", err)
	}
	if s.SchemeCheck != nil {
		if err := s.SchemeCheck(ctx, scheme); err != nil {
			return input, invalid("Version scheme incompatible with the deployer", err)
		}
	}
	if len(input.ReleaseKinds) > 0 {
		if err := input.ReleaseKinds.Validate(scheme); err != nil {
			return input, invalid("Invalid release cycle", err)
		}
	}
	return input, nil
}

// Create declares a repository.
func (s Projects) Create(ctx context.Context, actor string, input ProjectInput) (int64, error) {
	logger := loggerOr(s.Logger)
	input, err := s.Normalise(ctx, input)
	if err != nil {
		return 0, err
	}

	project := input.project()
	project.CreatedBy = actor
	id, err := s.Store.CreateProject(ctx, project)
	if errors.Is(err, store.ErrProjectExists) {
		return 0, refuse(http.StatusConflict, "Repository already declared", input.Repository)
	}
	if err != nil {
		return 0, failure(logger, "Cannot declare", err, "repository", input.Repository)
	}

	audit(ctx, logger, s.Store, actor, store.AuditProjectSet, input.Repository, map[string]any{"label": input.Label})
	return id, nil
}

// Update replaces a project's description, repository included. Changing the repository clears
// the access verdict: it was about something no longer driven.
func (s Projects) Update(ctx context.Context, actor string, id int64, input ProjectInput) error {
	logger := loggerOr(s.Logger)
	input, err := s.Normalise(ctx, input)
	if err != nil {
		return err
	}

	project := input.project()
	project.ID = id
	err = s.Store.UpdateProject(ctx, project)
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		return refuse(http.StatusNotFound, "Project not found", "")
	case errors.Is(err, store.ErrProjectExists):
		return refuse(http.StatusConflict, "Repository already declared", input.Repository)
	case err != nil:
		return failure(logger, "Cannot update", err, "project", strconv.FormatInt(id, 10))
	}

	audit(ctx, logger, s.Store, actor, store.AuditProjectSet, input.Repository,
		map[string]any{"label": input.Label, "id": id})
	return nil
}

func (input ProjectInput) project() store.Project {
	return store.Project{
		Repository: input.Repository, Label: input.Label, ForgeURL: input.ForgeURL, Slug: input.Slug,
		VersionScheme: input.VersionScheme, ReleaseKinds: input.ReleaseKinds,
	}
}
