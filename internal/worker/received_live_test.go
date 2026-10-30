package worker

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/tekton"
)

// A received fact is routed once, by the worker, and only a received one: a fact a probe or the
// observer wrote was routed where it was observed. Run by `make test-integration`.
func TestLiveAReceivedFactIsRoutedOnce(t *testing.T) {
	db := throwawayDatabase(t)
	ctx := context.Background()

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: "acme/shop", Label: "Shop", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Static tests on push", EventType: "gitea.push", Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{
			Gestes: []plan.Geste{{Kind: plan.GesteAction, Ref: "webhook"}},
		}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	counter := &countingRunner{}
	routing := NewReceivedFacts(db, NewDispatcher(db, NewEngine(db, counter, quietLogger()), quietLogger()),
		quietLogger())

	push := func(commit string) event.Event {
		return event.Event{
			Type: "gitea.push", Source: "gitea", ProjectID: &projectID, Subject: "main",
			Payload:    map[string]string{"branch": "main", "revision": commit},
			OccurredAt: time.Now(), DedupKey: "gitea.push:acme/shop:refs/heads/main:" + commit,
		}
	}

	if fresh, err := db.RecordReceivedEvent(ctx, push("abc")); err != nil || !fresh {
		t.Fatalf("first delivery: fresh=%v err=%v", fresh, err)
	}
	// The same fact delivered again, by the forge or by whoever replays it, is not recorded twice.
	if fresh, err := db.RecordReceivedEvent(ctx, push("abc")); err != nil || fresh {
		t.Fatalf("replayed delivery: fresh=%v err=%v", fresh, err)
	}
	// A fact observed by a probe is not the loop's to route.
	if _, err := db.RecordEvent(ctx, push("def")); err != nil {
		t.Fatalf("probed fact: %v", err)
	}

	routing.Pass(ctx)
	routing.Pass(ctx)
	if len(counter.sent) != 1 {
		t.Fatalf("%d steps started, expected 1", len(counter.sent))
	}

	// A worker stopping between routing and marking routes the fact again at its next pass, and opens
	// no second run for it.
	if _, err := db.Pool().Exec(ctx, "update events set dispatch_pending = true where dedup_key like '%:abc'"); err != nil {
		t.Fatal(err)
	}
	routing.Pass(ctx)
	runs, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || len(counter.sent) != 1 {
		t.Errorf("%d runs and %d steps after routing again, expected 1 and 1", len(runs), len(counter.sent))
	}
	if pending, err := db.PendingEvents(ctx, 10); err != nil || len(pending) != 0 {
		t.Errorf("still pending: %v, %v", pending, err)
	}
}

// enqueuingRunner queues each step as a job of the project, attached to no release, as a reaction
// to a push does.
type enqueuingRunner struct{ db *store.DB }

func (r enqueuingRunner) Run(ctx context.Context, run store.PlanRun, geste plan.Geste) (*int64, error) {
	id, err := r.db.Enqueue(ctx, store.JobRequest{Kind: geste.Ref, ProjectID: run.ProjectID, RequestedBy: "system:plan"})
	return &id, err
}

// A plan a push triggered carries jobs with no release, and still moves to its next stage when
// they conclude: seen on kind-mse, where the step stayed `running` after its pipeline succeeded.
func TestLiveAPlanWithoutAReleaseAdvances(t *testing.T) {
	db := throwawayDatabase(t)
	ctx := context.Background()

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: "acme/shop", Label: "Shop", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	engine := NewEngine(db, enqueuingRunner{db: db}, quietLogger())
	if started := engine.Trigger(ctx, store.PlanRun{
		Trigger: "reaction:1", EventKey: "gitea.push:acme/shop:refs/heads/main:abc", ProjectID: &projectID,
		Plan: plan.Plan{Stages: []plan.Stage{
			{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "php/static-tests"}}},
			{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "php/unit-tests"}}},
		}},
	}); started != 1 {
		t.Fatalf("%d steps started", started)
	}
	runs, err := db.ListPlanRuns(ctx, projectID, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %v, %v", runs, err)
	}
	steps, err := db.PlanSteps(ctx, runs[0].ID)
	if err != nil || steps[0].JobID == nil {
		t.Fatalf("steps %+v, %v", steps, err)
	}

	sink := NewRunSink(db).WithEngine(engine)
	if err := sink.finishJob(ctx, tekton.Run{JobID: *steps[0].JobID, Status: tekton.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}

	steps, err = db.PlanSteps(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].State != string(plan.Succeeded) || steps[1].JobID == nil {
		t.Errorf("after the first job: %+v", steps)
	}
}
