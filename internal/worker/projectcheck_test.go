package worker

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type countingChecks struct{ rounds atomic.Int32 }

func (c *countingChecks) CheckAll(_ context.Context, actor string) error {
	if actor != checkActor {
		panic(actor)
	}
	c.rounds.Add(1)
	return nil
}

// The check waits before its first round, then comes back at each interval, until stopped.
func TestTheProjectsAreCheckedAgainAndAgain(t *testing.T) {
	checks := &countingChecks{}
	checker := NewProjectChecker(checks, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker.delay, checker.interval = 20*time.Millisecond, 20*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = checker.Run(ctx); close(done) }()

	if checks.rounds.Load() != 0 {
		t.Error("a round before the delay")
	}
	deadline := time.Now().Add(2 * time.Second)
	for checks.rounds.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if checks.rounds.Load() < 2 {
		t.Errorf("%d round(s)", checks.rounds.Load())
	}
}
