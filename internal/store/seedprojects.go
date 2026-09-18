package store

import (
	"context"
	"errors"
	"fmt"
)

// SeedProjects declares the repositories that do not exist yet.
//
// The bridge between configuration and table. The driven repositories used to live in
// environment variables; the table must describe what the dashboard actually does, not what
// an administrator will have remembered to retype. Deriving them is therefore the only way
// to get a correct list from the first start-up.
//
// Nothing is overwritten: an existing declaration beats what the configuration suggests.
// That is what lets an administrator correct a label or add stages without a restart erasing
// them — and what makes the bootstrap safe to replay.
//
// Returns how many repositories were actually declared, so the caller can say it in its logs
// rather than suggest an operation with no effect.
func (db *DB) SeedProjects(ctx context.Context, projects []Project) (int, error) {
	declared := 0
	for _, project := range projects {
		if project.Repository == "" {
			// A clone URL from which `owner/name` could not be derived. Discarding it beats declaring a
			// repository that names nothing: the access check would fail with no way to know the fault
			// is upstream.
			continue
		}

		_, err := db.CreateProject(ctx, project)
		switch {
		case err == nil:
			declared++
		case errors.Is(err, ErrProjectExists):
			// Already declared: the nominal case after the first start-up.
		default:
			return declared, fmt.Errorf("bootstrapping project %q: %w", project.Repository, err)
		}
	}
	return declared, nil
}
