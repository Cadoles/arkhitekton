package configure

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// StatusMappingStore is what a tracker status's mapping needs from the database.
type StatusMappingStore interface {
	SetStatusBucket(ctx context.Context, projectID int64, statusName, bucket string) error
	ReprojectIssues(ctx context.Context, projectID int64) (int64, error)
	Auditor
}

// StatusMappings maps a project's tracker statuses onto the dashboard's buckets.
type StatusMappings struct {
	Store  StatusMappingStore
	Logger *slog.Logger
}

// Set declares a status's mapping, then reapplies it.
//
// Reapplying happens in the same gesture, and that is not a detail: without it the new rule would
// hold only for issues modified afterwards in the tracker, and the screen would mix two rules
// without saying so.
func (s StatusMappings) Set(ctx context.Context, actor string, project store.Project, statusName, bucket string) (int64, error) {
	logger := loggerOr(s.Logger)
	statusName = strings.TrimSpace(statusName)
	if statusName == "" {
		return 0, refuse(http.StatusBadRequest, "Missing status", "")
	}
	// The accepted buckets come from the store: a second enumeration here would diverge from the
	// database constraint at the first addition.
	if !store.ValidBucket(bucket) {
		return 0, refuse(http.StatusBadRequest, "Invalid bucket",
			"The bucket is one of “todo”, “doing”, “client-test”, “done” or “rejected”.")
	}

	if err := s.Store.SetStatusBucket(ctx, project.ID, statusName, bucket); err != nil {
		return 0, failure(logger, "Cannot save", err, "project", project.Slug, "status", statusName)
	}
	// Bounded to the project, like the declaration: reapplying one's mapping must not move the
	// other's issues, whose vocabulary is not the same.
	moved, err := s.Store.ReprojectIssues(ctx, project.ID)
	if err != nil {
		return 0, failure(logger, "Cannot reapply", err, "project", project.Slug)
	}

	// The log carries how many moved: changing a status's mapping moves a completion rate
	// displayed to the client, which deserves a record.
	audit(ctx, logger, s.Store, actor, "sync.tracker.mapping", project.Slug+"/"+statusName, map[string]any{
		"bucket": bucket, "issues_moved": moved,
	})
	return moved, nil
}

type statusClearer interface {
	ClearStatusBucket(ctx context.Context, projectID int64, statusName string) error
}

// Clear removes a project's own mapping of a status, then reapplies the mapping.
func (s StatusMappings) Clear(ctx context.Context, actor string, project store.Project, statusName string) error {
	logger := loggerOr(s.Logger)
	clearer, ok := s.Store.(statusClearer)
	if !ok {
		return refuse(http.StatusNotImplemented, "Cannot clear a status mapping", "")
	}
	if err := clearer.ClearStatusBucket(ctx, project.ID, statusName); err != nil {
		return failure(logger, "Cannot save", err, "project", project.Slug, "status", statusName)
	}
	moved, err := s.Store.ReprojectIssues(ctx, project.ID)
	if err != nil {
		return failure(logger, "Cannot reapply", err, "project", project.Slug)
	}
	audit(ctx, logger, s.Store, actor, "sync.tracker.mapping", project.Slug+"/"+statusName, map[string]any{
		"bucket": nil, "issues_moved": moved,
	})
	return nil
}
