package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

/*
 * Seeding the data.
 *
 * What is applied once, at the schema migration, then managed in the database.
 *
 * Two sets of data were born of an environment variable re-read every time: the administrators,
 * tested at each sign-in, and the driven repositories, redeclared at each start-up. Re-reading a
 * seed configuration indefinitely has two defects.
 *
 * The database stops being the source: removing a repository from the interface saw it come back
 * at the next start-up, with nothing on screen explaining it. And the configuration becomes a
 * permanent power — whoever can edit it can give themselves an administrator role again,
 * indefinitely, leaving no trace other than a sign-in.
 */

// Bootstrap steps. Named here rather than as free strings at the call site: a typo would
// replay a step already applied, which is precisely what the table exists to prevent.
const (
	BootstrapGrants   = "grants"
	BootstrapProjects = "projects"
	// BootstrapTrackers attaches already-declared projects to their tracking tool.
	//
	// A step distinct from `projects`, and that is what makes it useful: that one was claimed
	// before `tracker_project` existed, and a step already claimed is never replayed. Without
	// this second step, an existing deployment would silently lose the link to its tracker
	// the day the code began to honour it.
	BootstrapTrackers = "trackers"
	// BootstrapPlugins moves plugin configuration from the environment into the database.
	//
	// The same reason as `trackers`: a tracker's URL and key were deployment variables, and
	// become settings. Without this step, an existing deployment would see its
	// synchronisation stop the day the code reads settings rather than configuration — a
	// silent failure, the same one that motivated the previous step.
	BootstrapPlugins = "plugins"
)

// Grant is a role declared before its holder's subject is known.
type Grant struct {
	// Identifier is the `subject` when known, the account name otherwise.
	Identifier string
	Role       string
}

// BootstrapSpec carries what a bootstrap has to set.
type BootstrapSpec struct {
	Grants   []Grant
	Projects []Project
	// Plugins carries the settings derived from the configuration, with secrets already
	// sealed.
	//
	// Sealed by the caller and not here: this store does not have the key, deliberately — it
	// must not be able to return plaintext, even by a mistaken call.
	Plugins []PluginSettings
	// TrackerPluginID names the tracker plugin to attach the projects that declare one to.
	//
	// Passed rather than hard-coded: the registry lives in internal/plugin, and writing a
	// plugin's name here would make the store a second place that knows plugin names — hence
	// a place where renaming one would be forgotten.
	TrackerPluginID string
}

// BootstrapReport says what the bootstrap did, step by step.
//
// Returned to the caller rather than logged here: the caller knows at what level to say
// it, and a skipped step is not an event of the same weight as an applied one.
type BootstrapReport struct {
	GrantsApplied   int
	ProjectsApplied int
	TrackersApplied int
	PluginsApplied  int
	Skipped         []string
}

// Bootstrap applies the steps that have not been applied yet.
//
// A step already present in `bootstrap_steps` is never replayed, even if the
// configuration has changed since. That is what makes the database sole master after the
// first pass: a repository removed through the interface does not come back, and a role
// taken from someone is not re-granted by a restart.
//
// Each step is applied in its own transaction, with its record: a step that fails does
// not stop the next from succeeding, and is not marked as done.
func (db *DB) Bootstrap(ctx context.Context, spec BootstrapSpec) (BootstrapReport, error) {
	var report BootstrapReport
	var problems []error

	applied, count, err := db.applyStep(ctx, BootstrapGrants, func(ctx context.Context) (int, string, error) {
		return db.seedGrants(ctx, spec.Grants)
	})
	switch {
	case err != nil:
		problems = append(problems, err)
	case applied:
		report.GrantsApplied = count
	default:
		report.Skipped = append(report.Skipped, BootstrapGrants)
	}

	applied, count, err = db.applyStep(ctx, BootstrapProjects, func(ctx context.Context) (int, string, error) {
		declared, err := db.SeedProjects(ctx, spec.Projects)
		return declared, fmt.Sprintf("%d repository(ies)", declared), err
	})
	switch {
	case err != nil:
		problems = append(problems, err)
	case applied:
		report.ProjectsApplied = count
	default:
		report.Skipped = append(report.Skipped, BootstrapProjects)
	}

	applied, count, err = db.applyStep(ctx, BootstrapTrackers, func(ctx context.Context) (int, string, error) {
		linked, err := db.seedTrackers(ctx, spec.Projects)
		return linked, fmt.Sprintf("%d tracker(s) attached", linked), err
	})
	switch {
	case err != nil:
		problems = append(problems, err)
	case applied:
		report.TrackersApplied = count
	default:
		report.Skipped = append(report.Skipped, BootstrapTrackers)
	}

	applied, count, err = db.applyStep(ctx, BootstrapPlugins, func(ctx context.Context) (int, string, error) {
		posed, err := db.seedPlugins(ctx, spec.Plugins, spec.TrackerPluginID)
		return posed, fmt.Sprintf("%d setting(s)", posed), err
	})
	switch {
	case err != nil:
		problems = append(problems, err)
	case applied:
		report.PluginsApplied = count
	default:
		report.Skipped = append(report.Skipped, BootstrapPlugins)
	}

	return report, errors.Join(problems...)
}

// seedTrackers attaches to their tracker the projects that declare none.
func (db *DB) seedTrackers(ctx context.Context, projects []Project) (int, error) {
	linked := 0
	for _, project := range projects {
		if strings.TrimSpace(project.TrackerProject) == "" || project.Repository == "" {
			continue
		}

		tag, err := db.pool.Exec(ctx,
			`update projects set tracker_project = $2, updated_at = now()
			 where repository = $1 and coalesce(trim(tracker_project), '') = ''`,
			project.Repository, project.TrackerProject)
		if err != nil {
			return linked, fmt.Errorf("attaching the tracker of %q: %w", project.Repository, err)
		}
		linked += int(tag.RowsAffected())
	}
	return linked, nil
}

// applyStep runs a step unless it has already been applied.
//
// The marking happens in the same transaction as the step: separating them would leave a
// window where the step had happened without being marked — and a second start-up would
// replay it, which is exactly what this avoids.
//
// The claim goes through `insert … on conflict do nothing` rather than a read followed by
// a write: two migration Jobs launched in parallel — which Kubernetes readily does after
// a failure — must not apply the same step twice.
func (db *DB) applyStep(
	ctx context.Context, step string, apply func(context.Context) (int, string, error),
) (bool, int, error) {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("bootstrap %q: %w", step, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	tag, err := transaction.Exec(ctx,
		`insert into bootstrap_steps (step) values ($1) on conflict (step) do nothing`, step)
	if err != nil {
		return false, 0, fmt.Errorf("bootstrap %q: %w", step, err)
	}
	if tag.RowsAffected() == 0 {
		return false, 0, nil
	}

	count, detail, err := apply(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("bootstrap %q: %w", step, err)
	}

	if _, err := transaction.Exec(ctx,
		`update bootstrap_steps set detail = $2 where step = $1`, step, detail); err != nil {
		return false, 0, fmt.Errorf("bootstrap %q: %w", step, err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return false, 0, fmt.Errorf("bootstrap %q: %w", step, err)
	}
	return true, count, nil
}

// seedGrants declares the roles awaiting a subject.
func (db *DB) seedGrants(ctx context.Context, grants []Grant) (int, string, error) {
	declared := 0
	for _, grant := range grants {
		identifier := strings.TrimSpace(grant.Identifier)
		if identifier == "" || grant.Role == "" {
			continue
		}

		tag, err := db.pool.Exec(ctx,
			`insert into bootstrap_grants (identifier, role) values ($1, $2)
			 on conflict (identifier) do nothing`, identifier, grant.Role)
		if err != nil {
			return declared, "", fmt.Errorf("role declared for %q: %w", identifier, err)
		}
		if tag.RowsAffected() > 0 {
			declared++
		}
	}
	return declared, fmt.Sprintf("%d role(s) declared", declared), nil
}

// PendingGrants lists the declared roles that have not yet found their holder.
//
// It serves diagnosis: a misspelled identifier has no visible symptom — the user simply
// signs in with no permission — and this list is the only place the discrepancy shows.
func (db *DB) PendingGrants(ctx context.Context) ([]Grant, error) {
	rows, err := db.pool.Query(ctx,
		`select identifier, role from bootstrap_grants
		 where claimed_at is null order by identifier`)
	if err != nil {
		return nil, fmt.Errorf("declared roles pending: %w", err)
	}
	defer rows.Close()

	var grants []Grant
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.Identifier, &grant.Role); err != nil {
			return nil, fmt.Errorf("declared roles pending: %w", err)
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}
