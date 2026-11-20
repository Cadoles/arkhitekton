package configure

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

/*
 * Checking a project: does the dashboard read its repository, and is the project's deploy key set
 * there? Observed, never declared: the key was once confirmed by hand, and stayed "set" after it
 * had been removed from the repository.
 *
 * Asked at the project's creation, from its page while it is being put in service, and every hour
 * by the worker. Only a change of state reaches the audit log: the page asks every few seconds
 * while it waits for the key, and each asking is not an act.
 */

// ProjectProber reads a repository through the forge.
type ProjectProber interface {
	Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error)
}

// CheckStore is what checking a project needs.
type CheckStore interface {
	GetProject(ctx context.Context, id int64) (store.Project, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	RecordProjectProbe(ctx context.Context, id int64, probe store.ProjectProbe) (bool, error)
	Auditor
}

// Checks checks projects. Prober is nil when no forge is configured.
type Checks struct {
	Store  CheckStore
	Prober ProjectProber
	Logger *slog.Logger
}

// CheckResult is what a check observed.
type CheckResult struct {
	OK   bool
	Note string
	// KeyPresent is nil when the question could not be asked: no key yet, or a token that cannot
	// read the repository's keys.
	KeyPresent *bool
	KeyNote    string
}

// Check checks one project and records what it found.
func (c Checks) Check(ctx context.Context, actor string, id int64) (CheckResult, error) {
	if c.Prober == nil {
		// Neither green nor red: the check did not happen. Recording it as a failure would send
		// someone hunting for an access problem where there is only a missing token.
		return CheckResult{}, refuse(http.StatusServiceUnavailable, "Test unavailable",
			"no forge is configured: switch a forge plugin on and set its URL and token")
	}
	project, err := c.Store.GetProject(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			return CheckResult{}, refuse(http.StatusNotFound, "Project not found", "")
		}
		return CheckResult{}, err
	}
	return c.check(ctx, actor, project)
}

func (c Checks) check(ctx context.Context, actor string, project store.Project) (CheckResult, error) {
	probe, probeErr := c.Prober.Probe(ctx, project.Repository, project.DeployKeyPublic)
	result := CheckResult{OK: probeErr == nil, KeyPresent: probe.KeyPresent, KeyNote: probe.KeyNote}
	if probeErr != nil {
		result.Note = probeErr.Error()
	} else {
		result.Note = "default branch: " + probe.DefaultBranch
	}

	changed, err := c.Store.RecordProjectProbe(ctx, project.ID, store.ProjectProbe{
		OK: result.OK, Note: result.Note, KeyPresent: probe.KeyPresent, KeyNote: probe.KeyNote,
		KeysURL: probe.KeysURL,
	})
	if err != nil {
		return result, err
	}
	if changed {
		audit(ctx, loggerOr(c.Logger), c.Store, actor, store.AuditProjectProbe, project.Repository,
			map[string]any{"ok": result.OK, "key_present": probe.KeyPresent})
	}
	return result, nil
}

// CheckAll checks every project, one after the other. A project that fails does not stop the
// others.
func (c Checks) CheckAll(ctx context.Context, actor string) error {
	if c.Prober == nil {
		return nil
	}
	projects, err := c.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := c.check(ctx, actor, project); err != nil {
			loggerOr(c.Logger).Warn("checking a project", "project", project.Slug, "error", err)
		}
	}
	return nil
}
