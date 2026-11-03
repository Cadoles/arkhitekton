package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// KindReleasePlan is the kind of job that opens a release's plan.
//
// The api queues it and the worker carries it out: a release's plan may start with an action, a
// commit status for instance, and only the worker holds what an action needs. Opening the plan on
// the api side failed every such step.
const KindReleasePlan = "release-plan"

// ReleasePlanStore is what opening a release's plan needs from the database.
type ReleasePlanStore interface {
	GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	AdvanceReleaseState(ctx context.Context, id int64, from, to string) (bool, error)
}

// ReleasePlanTask opens the plan of the release its job names, from the revision its job carries.
type ReleasePlanTask struct {
	planner *ReleasePlanner
	store   ReleasePlanStore
	logger  *slog.Logger
}

func NewReleasePlanTask(planner *ReleasePlanner, releases ReleasePlanStore, logger *slog.Logger) *ReleasePlanTask {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReleasePlanTask{planner: planner, store: releases, logger: logger}
}

// Kind implements LocalTask.
func (t *ReleasePlanTask) Kind() string { return KindReleasePlan }

// Run opens the plan. When nothing starts, the release, announced as building when the job was
// queued, fails: left building, it would wait for a pipeline that never comes.
func (t *ReleasePlanTask) Run(ctx context.Context, job store.Job) error {
	if job.ReleaseID == nil {
		return errors.New("release plan: no release identifier")
	}
	record, err := t.store.GetReleaseByID(ctx, *job.ReleaseID)
	if err != nil {
		return fmt.Errorf("release plan: %w", err)
	}
	project, err := t.store.GetProject(ctx, record.ProjectID)
	if err != nil {
		return fmt.Errorf("release plan: %w", err)
	}

	if started := t.planner.OpenReleasePlan(ctx, project, record, job.Params["revision"]); started > 0 {
		return nil
	}
	if _, err := t.store.AdvanceReleaseState(ctx, record.ID,
		string(release.StateBuilding), string(release.StateFailed)); err != nil {
		t.logger.Error("failing a release nothing started", "release", record.Tag, "error", err)
	}
	return fmt.Errorf("release plan of %s: no step could start", record.Tag)
}

// QueuedReleasePlans opens a release's plan by queuing the job the worker carries out. It is what
// the api wires: it says a plan was queued, and the worker says what started.
type QueuedReleasePlans struct {
	Queue interface {
		Enqueue(ctx context.Context, request store.JobRequest) (int64, error)
	}
	Logger *slog.Logger
}

// OpenReleasePlan returns 1 when the opening is queued, 0 when it could not be.
func (q QueuedReleasePlans) OpenReleasePlan(
	ctx context.Context, project store.Project, record store.Release, revision string,
) int {
	projectID, releaseID := project.ID, record.ID
	if _, err := q.Queue.Enqueue(ctx, store.JobRequest{
		Kind:        KindReleasePlan,
		Params:      map[string]string{"revision": revision},
		RequestedBy: record.LaunchedBy,
		ReleaseID:   &releaseID,
		ProjectID:   &projectID,
	}); err != nil {
		logger := q.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("queuing a release's plan", "release", record.Tag, "error", err)
		return 0
	}
	return 1
}
