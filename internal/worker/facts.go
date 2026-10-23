package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The emission of facts, on the observation side.
 *
 * Nobody listens to them yet. That is the subject here: putting the emission points in place
 * and exercising that they hold under redelivery, while an error costs nothing.
 *
 * # Where they are placed, and why there
 *
 * Each fact is emitted **after** the write that establishes it, never before. The observer's
 * idempotence mechanism is a conditional write — a single delivery wins it — and leaning on it
 * makes the fact inherit its guarantee. The deduplication key stays a second belt, for the
 * paths that do not go through a state transition.
 *
 * # A failure interrupts nothing
 *
 * A fact that could not be written is logged and processing continues. It is the same rule as
 * for the ingestions, and here it has a net: the observer redelivers finished runs, so that a
 * fact lost at restart will be re-emitted at the next pass.
 *
 * That net does **not** exist for the facts emitted by the `api` — a release declared, a
 * release delivered. A lost fact is lost for good. That is acceptable as long as nothing
 * reacts; it will no longer be, and it is a gap to deal with when the engine arrives.
 */

// EventRecorder records a fact, or observes that it is already known.
//
// A narrow interface rather than the whole store: emission needs only that answer, and giving
// access to the rest would invite reading something else there.
type EventRecorder interface {
	// RecordEvent returns `false` when the fact already existed — the nominal case of a
	// redelivery, and not an error.
	RecordEvent(ctx context.Context, fact event.Event) (bool, error)
}

// WithEvents wires the emission of facts.
//
// Optional like the ingestions: without it, observation continues, and the fact feed stays
// empty. Nobody reacts yet, so nothing more than a trace is lost.
func (s *runSink) WithEvents(recorder EventRecorder) *runSink {
	s.events = recorder
	return s
}

// at dates a fact.
//
// Falls back to the clock when nothing was injected. The fallback is no frill: `runSink` is
// built literally in several tests, and a missing clock would there produce a nil dereference
// far from the offending line — on a path that, moreover, emits nothing.
func (s *runSink) at() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// emit records a fact without interrupting what produced it.
func (s *runSink) emit(ctx context.Context, fact event.Event) {
	if s.events == nil {
		return
	}

	recorded, err := s.events.RecordEvent(ctx, fact)
	if err != nil {
		s.logger.Error("emitting a fact",
			"type", fact.Type, "subject", fact.Subject, "error", err)
		return
	}
	if !recorded {
		// The nominal case of a redelivery. Logged at `Debug` and not silenced: that is what allows
		// checking that deduplication is working, without polluting an operations log at every
		// restart.
		s.logger.Debug("fact already known, not re-emitted",
			"type", fact.Type, "subject", fact.Subject, "key", fact.DedupKey)
		return
	}
	s.logger.Info("fact observed", "type", fact.Type, "subject", fact.Subject)

	// The routing is called **only on a new fact**. It is the first of the two bounds that stop a
	// redelivery replaying outgoing calls; the second is the uniqueness per (trigger, fact) of
	// `plan_runs`.
	//
	// Two bounds for the same guarantee, because this one decides on calls that leave the cluster
	// in the instance's name.
	s.dispatcher.Dispatch(ctx, fact)
}

// WithDispatcher wires the routing of facts towards the declared reactions.
//
// Optional like the rest: without it, facts are observed and nobody reacts to them.
func (s *runSink) WithDispatcher(dispatcher *Dispatcher) *runSink {
	s.dispatcher = dispatcher
	return s
}

// PromotionFacts composes the fact of a completed promotion.
//
// Supplied by the plugin that promotes: the core knows that a job attached to an environment
// concluded, not what that means.
type PromotionFacts interface {
	Promoted(at time.Time, linked store.JobReleaseContext) event.Event
}

// WithPromotionFacts wires the observation of promotions.
//
// Without it, no promotion is observed: that is the state of an instance without the MSE
// plugin, where nothing promotes.
func (s *runSink) WithPromotionFacts(facts PromotionFacts) *runSink {
	s.promotions = facts
	return s
}

func (s *runSink) emitReleasePromoted(ctx context.Context, linked store.JobReleaseContext, jobState string) {
	if s.promotions == nil || jobState != store.JobSucceeded || linked.Environment == "" {
		return
	}
	s.emit(ctx, s.promotions.Promoted(s.at(), linked))
}

// WithEngine wires the engine that runs the plans.
//
// It is what chains the verifications of a successful build, and what concludes their steps
// at the end of their job. Without it, nothing is chained — and `launchVerification` says so
// rather than returning zero silently.
func (s *runSink) WithEngine(engine *Engine) *runSink {
	s.engine = engine
	return s
}

// NewVerificationEngine assembles the engine that chains the verifications.
//
// The runner is bound to the sink: it needs the catalogue and the queue, which the sink
// already carries. Assembling them here rather than in `main` avoids exposing those two
// dependencies a second time.
func NewVerificationEngine(
	sink *runSink, plans PlanStore, actions GesteRunner, logger *slog.Logger,
) *Engine {
	// The routing between the two kinds of step lives outside the engine: it has no business
	// knowing the difference between a pipeline, which becomes a job, and an action, which leaves
	// the cluster.
	return NewEngine(plans, stepRouter{
		pipelines: NewReleaseRunner(catalog.Of(sink.catalog), sink.verification, logger),
		actions:   actions,
	}, logger)
}

// planStateOf translates a job's outcome into the plans' vocabulary.
//
// A cancellation is a failure for a plan: what followed must not start, and there is no
// "interrupted" state in the model. The nuance survives on the job, which carries and
// displays it; losing it here costs nothing, because the only question asked at this point is
// "does what follows start?".
func planStateOf(jobState string) plan.State {
	if jobState == store.JobSucceeded {
		return plan.Succeeded
	}
	return plan.Failed
}
