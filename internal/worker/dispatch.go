package worker

import (
	"context"
	"log/slog"
	"strconv"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The link between a fact and the plans it triggers.
 *
 * Facts, the engine and the actions were already in place; what was missing was the routing —
 * finding who is listening, and opening a run.
 *
 * # Called where a fact is observed, never by a clock
 *
 * Like the engine (see `engine.go`), and for the same reason: facts arrive by two known paths —
 * the observer and the probes — which both know when they have just written one. A loop
 * re-reading the feed would add a cursor, a latency and a second source of truth about what has
 * been handled.
 *
 * # What is **not** triggered
 *
 * An already known fact. `RecordEvent` returns `false` on a redelivery, and the routing is
 * called only on a new fact — the first bound. The uniqueness per (trigger, fact) of
 * `plan_runs` is the second: two bounds for the same guarantee, because this one decides on
 * outgoing calls.
 */

// ReactionStore is what the routing needs from the database.
//
// A narrow interface: it reads only the active reactions of one type, and giving it the rest
// would invite it to decide things that are not its own.
type ReactionStore interface {
	ReactionsFor(ctx context.Context, projectID int64, eventType string) ([]reaction.Reaction, error)
}

// Dispatcher opens the runs a fact triggers.
type Dispatcher struct {
	store  ReactionStore
	engine *Engine
	logger *slog.Logger
}

func NewDispatcher(reactions ReactionStore, engine *Engine, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: reactions, engine: engine, logger: logger}
}

// Dispatch triggers the reactions this fact matches.
//
// Without effect when the fact names no project: a reaction is declared per project, and a
// fact naming none has nobody to listen to it.
func (d *Dispatcher) Dispatch(ctx context.Context, fact event.Event) {
	if d == nil || d.engine == nil || fact.ProjectID == nil {
		return
	}

	reactions, err := d.store.ReactionsFor(ctx, *fact.ProjectID, string(fact.Type))
	if err != nil {
		d.logger.Error("a fact's reactions",
			"type", fact.Type, "project", *fact.ProjectID, "error", err)
		return
	}

	for _, declared := range reactions {
		if !declared.Matches(fact.Payload) {
			// Logged at `Debug`: it is the common case — a reaction on final releases sees every
			// pre-release go by — and saying it at `Info` would drown the log.
			d.logger.Debug("reaction not matched",
				"reaction", declared.Name, "fact", fact.Type)
			continue
		}

		started := d.engine.Trigger(ctx, store.PlanRun{
			Trigger:   declared.Trigger(),
			EventKey:  fact.DedupKey,
			ProjectID: fact.ProjectID,
			// A fact naming a release of its project opens a plan on that release: its messages can
			// then list the release's issues and images.
			ReleaseID: d.releaseOf(ctx, fact),
			// A pull request's fact opens a plan on that request: its jobs, their results and what
			// they produce show on its page.
			PullRequestID: pullRequestOf(fact),
			Plan:          declared.Plan,
			// The fact's payload becomes the run's context: it is what binds the steps' templates. The
			// plan **names**, the run resolves.
			Context: fact.Payload,
		})
		d.logger.Info("reaction triggered",
			"reaction", declared.Name, "fact", fact.Type,
			"subject", fact.Subject, "steps", started)
	}
}

// releaseOf returns the release a fact's `release_id` names, when it belongs to the fact's
// project. Nil otherwise: a payload is not trusted to point into another project.
func (d *Dispatcher) releaseOf(ctx context.Context, fact event.Event) *int64 {
	releases, ok := d.store.(interface {
		GetReleaseByID(ctx context.Context, id int64) (store.Release, error)
	})
	if !ok || fact.ProjectID == nil {
		return nil
	}
	id, err := strconv.ParseInt(fact.Payload[contextReleaseID], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	record, err := releases.GetReleaseByID(ctx, id)
	if err != nil || record.ProjectID != *fact.ProjectID {
		return nil
	}
	return &id
}

// pullRequestOf returns the pull request a core pull request fact names, nil for any other fact. A
// plugin's fact carrying the same key names nothing the core recorded.
func pullRequestOf(fact event.Event) *int64 {
	switch fact.Type {
	case event.PullRequestOpened, event.PullRequestSynchronized, event.PullRequestClosed,
		event.PullRequestMerged, event.PullRequestReopened:
	default:
		return nil
	}
	id, err := strconv.ParseInt(fact.Payload[ContextPullRequest], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}
