package worker

import (
	"context"
	"log/slog"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// StatusForges returns what writes to a project's forge.
type StatusForges interface {
	TagWriter(ctx context.Context, projectID int64) (provider.TagWriter, error)
}

// StatusProjects names a project in the links a status carries.
type StatusProjects interface {
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// CommitStatuses reports each pipeline step of a plan that asks for it on the commit the run bears
// on: `pending` when the step starts, then its outcome. The status links the release or the pull
// request on the dashboard.
//
// A status that cannot be set is logged and nothing else: the forge's display must not stop a
// build.
type CommitStatuses struct {
	forges    StatusForges
	projects  StatusProjects
	publicURL string
	logger    *slog.Logger
}

func NewCommitStatuses(forges StatusForges, projects StatusProjects, publicURL string, logger *slog.Logger) *CommitStatuses {
	if logger == nil {
		logger = slog.Default()
	}
	return &CommitStatuses{
		forges: forges, projects: projects, publicURL: strings.TrimRight(publicURL, "/"), logger: logger,
	}
}

// StatusContext is the status's name on the forge: one per step, which its outcome replaces.
func StatusContext(geste plan.Geste) string {
	name := geste.Label
	if name == "" {
		name = geste.Ref
	}
	return "arkhitekton/" + name
}

var statusOf = map[plan.State]struct{ state, description string }{
	plan.Running:   {"pending", "En cours"},
	plan.Succeeded: {"success", "Réussi"},
	plan.Failed:    {"failure", "Échoué"},
}

// StepChanged implements StepSink.
func (c *CommitStatuses) StepChanged(ctx context.Context, run store.PlanRun, geste plan.Geste, state plan.State) {
	reported, known := statusOf[state]
	commit := run.Context["revision"]
	if !run.Plan.Statuses || !known || run.ProjectID == nil || commit == "" {
		return
	}
	logger := c.logger.With("run", run.ID, "step", StatusContext(geste), "state", reported.state)

	writer, err := c.forges.TagWriter(ctx, *run.ProjectID)
	if err != nil {
		logger.Warn("commit status not set: no forge to write to", "error", err)
		return
	}
	statuses, ok := writer.(provider.CommitStatusWriter)
	if !ok {
		logger.Warn("commit status not set: the project's forge cannot set one")
		return
	}
	if err := statuses.SetCommitStatus(ctx, commit, reported.state, StatusContext(geste),
		reported.description, c.link(ctx, run)); err != nil {
		logger.Warn("commit status not set", "error", err)
	}
}

// link is the dashboard page of what the run bears on.
func (c *CommitStatuses) link(ctx context.Context, run store.PlanRun) string {
	links := DashboardLinks{PublicURL: c.publicURL, Projects: c.projects}
	switch {
	case run.ReleaseID != nil:
		return links.Release(ctx, run.ProjectID, run.Context["tag"])
	case run.PullRequestID != nil:
		return links.PullRequest(ctx, run.ProjectID, run.Context["number"])
	default:
		return ""
	}
}
