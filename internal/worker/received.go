package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Routing the facts a source pushed.
 *
 * The routing is otherwise called where a fact is observed, never by a clock (see `dispatch.go`):
 * the observer and the probes know when they have just written one. A delivery is the exception,
 * because it is received by the `api` and the reactions leave from here. The `api` records the fact
 * as pending, and this loop routes it.
 *
 * A fact is marked routed after its reactions were triggered, not before: a worker that stops in
 * between routes it again at its next pass, and the uniqueness of a plan run per trigger and fact
 * keeps that second routing from opening a second run.
 */

// receivedInterval spaces two passes. Short: a push someone is waiting on should start its
// pipeline in the time the queue takes to notice it, which is two seconds too.
const receivedInterval = 2 * time.Second

// receivedBatch bounds a pass. A burst of deliveries is routed over several passes rather than
// holding the loop.
const receivedBatch = 100

// ReceivedStore is what routing the received facts needs from the database.
type ReceivedStore interface {
	PendingEvents(ctx context.Context, limit int) ([]store.RecordedEvent, error)
	MarkEventDispatched(ctx context.Context, id int64) error
}

// ReceivedFacts routes the facts the `api` received.
type ReceivedFacts struct {
	store      ReceivedStore
	dispatcher *Dispatcher
	logger     *slog.Logger
}

func NewReceivedFacts(received ReceivedStore, dispatcher *Dispatcher, logger *slog.Logger) *ReceivedFacts {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReceivedFacts{store: received, dispatcher: dispatcher, logger: logger}
}

// Run routes the received facts until cancellation.
func (r *ReceivedFacts) Run(ctx context.Context) error {
	ticker := time.NewTicker(receivedInterval)
	defer ticker.Stop()
	for {
		r.Pass(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Pass routes what is pending.
func (r *ReceivedFacts) Pass(ctx context.Context) {
	pending, err := r.store.PendingEvents(ctx, receivedBatch)
	if err != nil {
		r.logger.Error("received facts", "error", err)
		return
	}
	for _, recorded := range pending {
		r.dispatcher.Dispatch(ctx, event.Event{
			Type: event.Type(recorded.Type), Source: recorded.Source, ProjectID: recorded.ProjectID,
			Subject: recorded.Subject, Payload: recorded.Payload, OccurredAt: recorded.OccurredAt,
			Depth: recorded.Depth, DedupKey: recorded.DedupKey,
		})
		if err := r.store.MarkEventDispatched(ctx, recorded.ID); err != nil {
			// Routed again at the next pass, which opens nothing twice.
			r.logger.Error("marking a received fact routed", "fact", recorded.ID, "error", err)
		}
	}
}
