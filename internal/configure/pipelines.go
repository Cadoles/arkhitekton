package configure

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// ProjectPipelineStore is what a project's pipeline switches need from the database.
type ProjectPipelineStore interface {
	SetProjectPipeline(ctx context.Context, projectID int64, kind string, enabled bool, actor string) error
	Auditor
}

// ProjectPipelines switches a catalogue kind on or off for one project.
type ProjectPipelines struct {
	Store   ProjectPipelineStore
	Catalog catalog.Source
	Logger  *slog.Logger
}

// Set records a project's decision about a kind.
func (s ProjectPipelines) Set(ctx context.Context, actor string, project store.Project, kind string, enabled bool) (catalog.Pipeline, error) {
	logger := loggerOr(s.Logger)
	if s.Catalog == nil {
		return catalog.Pipeline{}, refuse(http.StatusConflict, "Catalogue unavailable", "")
	}
	kind = strings.TrimSpace(kind)
	pipeline, found := catalog.Of(s.Catalog).Find(kind)
	if !found {
		// Refused rather than recorded: a row for a non-existent kind would give a screen saying
		// "disabled" about a pipeline nobody launches, and would hide the typo.
		return catalog.Pipeline{}, refuse(http.StatusNotFound, "Kind unknown to the catalogue", kind)
	}

	if err := s.Store.SetProjectPipeline(ctx, project.ID, pipeline.Kind, enabled, actor); err != nil {
		return pipeline, failure(logger, "Cannot save", err, "project", project.Slug, "kind", pipeline.Kind)
	}

	// Recorded: disabling a verification changes what a release will have gone through before
	// being shipped, and that is the kind of decision one wants to be able to find again.
	audit(ctx, logger, s.Store, actor, "project.pipeline", project.Slug+"/"+pipeline.Kind,
		map[string]any{"enabled": enabled})
	return pipeline, nil
}

// QueueLimitStore is what the queues' ceilings need from the database.
type QueueLimitStore interface {
	SetQueueLimit(ctx context.Context, kind string, max int, actor string) error
	ClearQueueLimit(ctx context.Context, kind string) error
	Auditor
}

// MaxQueueLimit bounds what a setting may ask for.
//
// The global ceiling stays above it anyway, but accepting 10,000 would suggest the number means
// something. Twenty is already more than the cluster holds.
const MaxQueueLimit = 20

// QueueLimits sets the ceilings of the queues, which live in the database and not in the
// catalogue: a setting that requires rebuilding a ConfigMap is not a setting.
type QueueLimits struct {
	Store  QueueLimitStore
	Logger *slog.Logger
}

// Set sets a queue's ceiling.
func (s QueueLimits) Set(ctx context.Context, actor, kind string, max int) error {
	if kind == "" {
		return refuse(http.StatusBadRequest, "Missing queue type", "")
	}
	if max < 1 || max > MaxQueueLimit {
		return refuse(http.StatusBadRequest, "Invalid ceiling", fmt.Sprintf("expected between 1 and %d", MaxQueueLimit))
	}
	logger := loggerOr(s.Logger)
	if err := s.Store.SetQueueLimit(ctx, kind, max, actor); err != nil {
		return failure(logger, "Cannot set", err, "kind", kind)
	}
	// Recorded like the other operational decisions: "who opened the queue to ten" must have an
	// answer without consulting the application logs.
	audit(ctx, logger, s.Store, actor, store.AuditQueueLimit, kind, map[string]any{"max_concurrent": max})
	return nil
}

// Clear returns a queue to its catalogue ceiling.
func (s QueueLimits) Clear(ctx context.Context, actor, kind string) error {
	logger := loggerOr(s.Logger)
	if err := s.Store.ClearQueueLimit(ctx, kind); err != nil {
		return failure(logger, "Cannot set", err, "kind", kind)
	}
	audit(ctx, logger, s.Store, actor, store.AuditQueueLimit, kind, map[string]any{"max_concurrent": nil})
	return nil
}

type pipelineClearer interface {
	ClearProjectPipeline(ctx context.Context, projectID int64, kind string) error
}

// Clear forgets a project's decision about a kind: it follows the default again.
func (s ProjectPipelines) Clear(ctx context.Context, actor string, project store.Project, kind string) error {
	logger := loggerOr(s.Logger)
	clearer, ok := s.Store.(pipelineClearer)
	if !ok {
		return refuse(http.StatusNotImplemented, "Cannot clear a pipeline decision", "")
	}
	if err := clearer.ClearProjectPipeline(ctx, project.ID, kind); err != nil {
		return failure(logger, "Cannot save", err, "project", project.Slug, "kind", kind)
	}
	audit(ctx, logger, s.Store, actor, "project.pipeline", project.Slug+"/"+kind, map[string]any{"enabled": nil})
	return nil
}
