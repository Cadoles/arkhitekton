package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// A loop that dies ends the term: the lease is given up and the loops start again at the next one,
// rather than the worker keeping the lease with a loop missing.
func TestADeadLoopGivesTheLeaseUpAndTheLoopsStartAgain(t *testing.T) {
	previous := standAgainAfter
	standAgainAfter = 10 * time.Millisecond
	t.Cleanup(func() { standAgainAfter = previous })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var terms atomic.Int32
	secondTerm := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunAsLeader(ctx, fake.NewClientset(), LeaderConfig{Namespace: "arkhitekton", Identity: "worker-0"},
			quietLogger(), func(leadingCtx context.Context) error {
				if terms.Add(1) == 1 {
					return errors.New("a loop died")
				}
				close(secondTerm)
				<-leadingCtx.Done()
				return leadingCtx.Err()
			})
	}()

	select {
	case <-secondTerm:
	case <-time.After(30 * time.Second):
		t.Fatalf("the loops did not start again after a loop died (%d term(s))", terms.Load())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stopping: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the election did not end with its context")
	}
}
