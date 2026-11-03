package worker

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

/*
 * What a run's templates can read beyond its fact: the release or the pull request it bears on,
 * as a message needs them. Flat keys, as everywhere else in a run's context: an action composes
 * text, it does not walk structures.
 */

// The keys a run's context gains.
const (
	contextReleaseURL     = "release.url"
	contextReleaseNotes   = "release.notes"
	contextReleaseImages  = "release.images"
	contextPrerelease     = "release.prerelease"
	contextPullRequestURL = "pull_request.url"
	contextPlanSummary    = "plan.summary"
)

// ContextSource adds to a run's context what the database knows of its subject.
type ContextSource interface {
	Enrich(ctx context.Context, run store.PlanRun) map[string]string
}

// DashboardLinks composes the dashboard's links, naming the project: the screen otherwise opens
// the project last chosen in the browser.
type DashboardLinks struct {
	PublicURL string
	Projects  StatusProjects
}

func (l DashboardLinks) link(ctx context.Context, projectID *int64, path string) string {
	base := strings.TrimRight(l.PublicURL, "/")
	if base == "" || projectID == nil || l.Projects == nil {
		return ""
	}
	project, err := l.Projects.GetProject(ctx, *projectID)
	if err != nil {
		return ""
	}
	return base + path + "?project=" + url.QueryEscape(project.Slug)
}

// Release links a release's page.
func (l DashboardLinks) Release(ctx context.Context, projectID *int64, tag string) string {
	if tag == "" {
		return ""
	}
	return l.link(ctx, projectID, "/releases/"+url.PathEscape(tag))
}

// PullRequest links a pull request's page.
func (l DashboardLinks) PullRequest(ctx context.Context, projectID *int64, number string) string {
	if number == "" {
		return ""
	}
	return l.link(ctx, projectID, "/pulls/"+url.PathEscape(number))
}

// RunContextStore is what the release's variables are read from.
type RunContextStore interface {
	StatusProjects
	GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	ListNoteEntries(ctx context.Context, releaseID int64) ([]store.NoteEntry, error)
	ReleaseArtifacts(ctx context.Context, releaseID int64) ([]store.PlanArtifact, error)
}

// IssueLinks links an issue in a project's tracker. Empty when the project has none.
type IssueLinks func(ctx context.Context, projectID int64, issue string) string

// RunContext reads a run's release and pull request variables.
type RunContext struct {
	store  RunContextStore
	links  DashboardLinks
	issues IssueLinks
	logger *slog.Logger
}

func NewRunContext(source RunContextStore, publicURL string, issues IssueLinks, logger *slog.Logger) *RunContext {
	if logger == nil {
		logger = slog.Default()
	}
	return &RunContext{
		store: source, links: DashboardLinks{PublicURL: publicURL, Projects: source},
		issues: issues, logger: logger,
	}
}

// Enrich implements ContextSource.
func (c *RunContext) Enrich(ctx context.Context, run store.PlanRun) map[string]string {
	added := map[string]string{}
	if run.PullRequestID != nil {
		if link := c.links.PullRequest(ctx, run.ProjectID, run.Context["number"]); link != "" {
			added[contextPullRequestURL] = link
		}
	}
	if run.ReleaseID == nil {
		return added
	}

	record, err := c.store.GetReleaseByID(ctx, *run.ReleaseID)
	if err != nil {
		c.logger.Warn("release of a run", "run", run.ID, "error", err)
		return added
	}
	projectID := &record.ProjectID
	added[contextReleaseURL] = c.links.Release(ctx, projectID, record.Tag)
	added[contextPrerelease] = c.prerelease(ctx, record)
	added[contextReleaseNotes] = c.notes(ctx, record)
	added[contextReleaseImages] = c.images(ctx, record)
	return added
}

func (c *RunContext) prerelease(ctx context.Context, record store.Release) string {
	project, err := c.store.GetProject(ctx, record.ProjectID)
	if err != nil {
		return "false"
	}
	scheme, err := version.For(project.VersionScheme)
	if err != nil {
		return "false"
	}
	parsed, err := scheme.Parse(record.Tag)
	if err != nil {
		return "false"
	}
	prerelease, known := parsed.Prereleased()
	return fmt.Sprint(known && prerelease)
}

// notes lists the release's issues, one Markdown line each, linked to the tracker when it has one.
func (c *RunContext) notes(ctx context.Context, record store.Release) string {
	entries, err := c.store.ListNoteEntries(ctx, record.ID)
	if err != nil {
		c.logger.Warn("note of a release", "release", record.Tag, "error", err)
		return ""
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		reference := "#" + entry.ExternalID
		if c.issues != nil {
			if link := c.issues(ctx, record.ProjectID, entry.ExternalID); link != "" {
				reference = "[" + reference + "](" + link + ")"
			}
		}
		line := "- " + reference
		if entry.Subject != "" {
			line += " " + entry.Subject
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// images lists the images the release's automation published, one Markdown line each.
func (c *RunContext) images(ctx context.Context, record store.Release) string {
	artifacts, err := c.store.ReleaseArtifacts(ctx, record.ID)
	if err != nil {
		c.logger.Warn("artifacts of a release", "release", record.Tag, "error", err)
		return ""
	}
	var lines []string
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		if artifact.Name != "image" || seen[artifact.Reference] {
			continue
		}
		seen[artifact.Reference] = true
		lines = append(lines, "- `"+artifact.Reference+"`")
	}
	return strings.Join(lines, "\n")
}

// stepVariables are what a plan's own progress says: each step's state, and a summary of its
// pipelines for a message.
func stepVariables(progress plan.Progress) map[string]string {
	variables := map[string]string{}
	var rows []string
	for stage, declared := range progress.Plan.Stages {
		for position, geste := range declared.Gestes {
			state := progress.States[stage][position]
			name := geste.Label
			if name == "" {
				name = geste.Ref
			}
			variables["steps."+name+".state"] = string(state)
			if geste.Kind == plan.GestePipeline {
				rows = append(rows, "| "+name+" | "+summaryOf[state]+" |")
			}
		}
	}
	if len(rows) > 0 {
		variables[contextPlanSummary] = "| Étape | Résultat |\n|---|---|\n" + strings.Join(rows, "\n")
	}
	return variables
}

var summaryOf = map[plan.State]string{
	plan.Pending:   "⏳ en attente",
	plan.Running:   "⏳ en cours",
	plan.Succeeded: "✅ réussi",
	plan.Failed:    "❌ échoué",
	plan.Skipped:   "⏭️ sauté",
}

// withVariables returns the context the steps about to start read: the run's, its artifacts', its
// steps' and its subject's.
func (e *Engine) withVariables(ctx context.Context, run store.PlanRun, progress plan.Progress) map[string]string {
	enriched := maps.Clone(e.withArtifacts(ctx, run, progress.Plan))
	if enriched == nil {
		enriched = map[string]string{}
	}
	maps.Copy(enriched, stepVariables(progress))
	if e.contexts != nil {
		for key, value := range e.contexts.Enrich(ctx, run) {
			if value != "" {
				enriched[key] = value
			}
		}
	}
	return enriched
}
