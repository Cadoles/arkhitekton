package worker

import (
	"context"
	"log/slog"
	"time"
)

/*
 * The hourly check of every project: does the dashboard still read its repository, and is its deploy
 * key still set there? A key removed from the repository then shows on the projects' list, and the
 * builds refuse to leave, without anyone having to test again.
 */

const (
	projectCheckInterval = time.Hour
	// projectCheckDelay spares the forge a round of checks at every rollout.
	projectCheckDelay = 5 * time.Minute
)

// ProjectChecks checks every project. Implemented by [configure.Checks].
type ProjectChecks interface {
	CheckAll(ctx context.Context, actor string) error
}

// ProjectChecker runs the hourly check, under the leader's lease.
type ProjectChecker struct {
	checks   ProjectChecks
	logger   *slog.Logger
	delay    time.Duration
	interval time.Duration
}

func NewProjectChecker(checks ProjectChecks, logger *slog.Logger) *ProjectChecker {
	return &ProjectChecker{checks: checks, logger: logger, delay: projectCheckDelay, interval: projectCheckInterval}
}

// checkActor signs, in the audit log, what the hourly check found changed.
const checkActor = "worker"

func (c *ProjectChecker) Run(ctx context.Context) error {
	wait := time.NewTimer(c.delay)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("project checks stopped")
			return nil
		case <-wait.C:
		}
		if err := c.checks.CheckAll(ctx, checkActor); err != nil && ctx.Err() == nil {
			c.logger.Error("checking the projects", "error", err)
		}
		wait.Reset(c.interval)
	}
}
