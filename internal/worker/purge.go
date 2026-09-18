package worker

import (
	"context"
	"log/slog"
	"time"
)

// purgeInterval bounds how often the purge runs.
//
// A quarter of an hour: these tables grow only at the pace of human sign-ins, and purging more
// often would cost queries for nothing. Less often would let abandoned transactions
// accumulate, each born of a sign-in started then interrupted.
const purgeInterval = 15 * time.Minute

// Expirable is what the purge can clean.
//
// Both together, because both accumulate for the same reason: a user closing their tab.
// Separating them would give two loops at the same pace, on the same database.
type Expirable interface {
	// PurgeExpired deletes the sessions that have lapsed.
	PurgeExpired(ctx context.Context) (int64, error)
}

// Flows cleans the abandoned authentication transactions.
type Flows interface {
	PurgeExpiredFlows(ctx context.Context) (int64, error)
}

// Purger periodically deletes what has expired.
//
// The loop was missing. `PurgeExpired` existed, was documented as "called periodically by the
// worker", and nobody called it: lapsed sessions stayed in the database indefinitely.
// Documentation describing a behaviour that is absent is worse than no documentation — it
// excuses one from going to look.
type Purger struct {
	sessions Expirable
	flows    Flows
	logger   *slog.Logger
}

func NewPurger(sessions Expirable, flows Flows, logger *slog.Logger) *Purger {
	return &Purger{sessions: sessions, flows: flows, logger: logger}
}

// Run purges until cancellation.
//
// A failed purge does not bring down the loop: it is maintenance, not a function. Losing the
// worker because a deletion failed would stop the scheduling and the observation, which have
// nothing to do with it.
func (p *Purger) Run(ctx context.Context) error {
	// A first pass immediately: at restart, there is often what the previous process had no time
	// to clean.
	p.once(ctx)

	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.once(ctx)
		}
	}
}

func (p *Purger) once(ctx context.Context) {
	if sessions, err := p.sessions.PurgeExpired(ctx); err != nil {
		p.logger.Error("purging the sessions", "error", err)
	} else if sessions > 0 {
		p.logger.Info("lapsed sessions deleted", "count", sessions)
	}

	if flows, err := p.flows.PurgeExpiredFlows(ctx); err != nil {
		p.logger.Error("purging the authentication transactions", "error", err)
	} else if flows > 0 {
		p.logger.Info("abandoned authentication transactions deleted", "count", flows)
	}
}
