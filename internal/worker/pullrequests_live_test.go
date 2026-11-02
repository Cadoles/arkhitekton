package worker

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/reaction"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// A reaction to a push on a pull request builds its head under the request's tag, and the job it
// queues belongs to the request. Run by `make test-integration`.
func TestLiveAPullRequestPlanBuildsItsHead(t *testing.T) {
	db := throwawayDatabase(t)
	ctx := context.Background()

	projectID, err := db.CreateProject(ctx, store.Project{
		Repository: "acme/shop", Label: "Shop", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./Dockerfile", BuildContext: ".", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	updated := time.Now()
	pull, _, err := db.UpsertPullRequest(ctx, store.PullRequest{
		ProjectID: projectID, Number: 12, Title: "Feature", State: "open", HeadSHA: "abcdef0123456789",
		SourceBranch: "feature", TargetBranch: "main", UpdatedAt: &updated,
	})
	if err != nil {
		t.Fatalf("pull request: %v", err)
	}
	if _, err := db.SaveReaction(ctx, reaction.Reaction{
		ProjectID: projectID, Name: "Build each push", EventType: string(event.PullRequestSynchronized), Enabled: true,
		Plan: plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{
			Kind: plan.GestePipeline, Ref: "core/build",
			Params: map[string]string{"image-name": "shop", "target": "web"},
		}}}}},
	}, "test"); err != nil {
		t.Fatalf("reaction: %v", err)
	}

	engine := NewEngine(db, NewReleaseRunner(testCatalogue(t), db, quietLogger()), quietLogger())
	fact := event.PullRequestAt(event.PullRequestSynchronized, updated, projectID, pull.Number, pull.HeadSHA,
		PullRequestPayload(pull, "reg.example/team"))
	fact.ProjectID = &projectID
	NewDispatcher(db, engine, quietLogger()).Dispatch(ctx, fact)

	var (
		jobPull int64
		params  map[string]string
	)
	if err := db.Pool().QueryRow(ctx,
		`select pull_request_id, params from jobs where kind = 'core/build'`).Scan(&jobPull, &params); err != nil {
		t.Fatalf("the build job: %v", err)
	}
	if jobPull != pull.ID {
		t.Errorf("job pull request = %d, want %d", jobPull, pull.ID)
	}
	if params["revision"] != "abcdef0123456789" || params["tag-name"] != "pr-12-abcdef0" || params["image-name"] != "shop" {
		t.Errorf("params = %v", params)
	}

	var planPull int64
	if err := db.Pool().QueryRow(ctx, `select pull_request_id from plan_runs`).Scan(&planPull); err != nil || planPull != pull.ID {
		t.Errorf("plan run pull request = %d, %v", planPull, err)
	}
}
