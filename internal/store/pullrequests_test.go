package store

import (
	"context"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
)

func openPull(projectID, number int64, head string, updated time.Time) PullRequest {
	return PullRequest{
		ProjectID: projectID, Number: number, Title: "Feature", Author: "dev", SourceBranch: "feature",
		TargetBranch: "main", HeadSHA: head, State: "open", URL: "https://forge.test/pulls/1",
		UpdatedAt: &updated,
	}
}

// The first sighting has no previous state; the next one returns what was recorded before it.
func TestAPullRequestIsRecordedWithItsPreviousState(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	projectID := project(t, db, "team/app")
	now := time.Now().UTC().Truncate(time.Second)

	first, previous, err := db.UpsertPullRequest(ctx, openPull(projectID, 7, "aaa", now))
	if err != nil || previous != nil || first.ID == 0 {
		t.Fatalf("first sighting: %+v, previous %+v, %v", first, previous, err)
	}
	pushed := openPull(projectID, 7, "bbb", now.Add(time.Minute))
	second, previous, err := db.UpsertPullRequest(ctx, pushed)
	if err != nil || previous == nil || previous.HeadSHA != "aaa" || second.HeadSHA != "bbb" || second.ID != first.ID {
		t.Fatalf("second sighting: %+v, previous %+v, %v", second, previous, err)
	}

	merged := openPull(projectID, 8, "ccc", now.Add(2*time.Minute))
	merged.State = "merged"
	if _, _, err := db.UpsertPullRequest(ctx, merged); err != nil {
		t.Fatal(err)
	}

	open, _ := db.ListPullRequests(ctx, projectID, PullRequestsOpen, 0)
	closed, _ := db.ListPullRequests(ctx, projectID, PullRequestsClosed, 0)
	all, _ := db.ListPullRequests(ctx, projectID, "", 0)
	if len(open) != 1 || open[0].Number != 7 || len(closed) != 1 || closed[0].Number != 8 || len(all) != 2 || all[0].Number != 8 {
		t.Errorf("open %+v, closed %+v, all %+v", open, closed, all)
	}
	if _, err := db.PullRequestByNumber(ctx, projectID, 9); err != ErrPullRequestNotFound {
		t.Errorf("unknown request: %v", err)
	}
}

// A job launched on a pull request carries it, takes its project, and so do the results its run
// produces and the plan that opened it.
func TestWhatRunsOnAPullRequestCarriesIt(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	projectID := project(t, db, "team/app")
	pull, _, err := db.UpsertPullRequest(ctx, openPull(projectID, 7, "aaa", time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	run, _, err := db.CreatePlanRun(ctx, PlanRun{
		Trigger: "reaction:1", EventKey: "pull_request.synchronized:1:7:aaa", ProjectID: &projectID,
		PullRequestID: &pull.ID, Plan: plan.Plan{Stages: []plan.Stage{{Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "php/static-tests"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if read, err := db.PlanRunByID(ctx, run.ID); err != nil || read.PullRequestID == nil || *read.PullRequestID != pull.ID {
		t.Errorf("plan run = %+v, %v", read, err)
	}
	if err := db.RecordPlanArtifacts(ctx, run.ID, nil, []PlanArtifact{{Name: "image", Kind: "image", Reference: "r/app:pr-7-aaa"}}); err != nil {
		t.Fatal(err)
	}

	jobID, err := db.Enqueue(ctx, JobRequest{Kind: "php/static-tests", PullRequestID: &pull.ID})
	if err != nil {
		t.Fatal(err)
	}
	record := runningRecord("run-on-pull")
	record.JobID = jobID
	runID, err := db.UpsertRun(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFacts(ctx, InterpretedRun{
		RunID: runID, Producer: facts.Producer{Plugin: "php", Version: "1.0.0"}, Capability: "php/static-tests",
		Sets: []facts.Set{{Shape: facts.ShapeCases, Group: "static", Tool: "phpstan",
			Cases: []facts.Case{{Suite: "src/A.php", Name: "rule", Status: facts.CasePassed}}}},
	}); err != nil {
		t.Fatal(err)
	}

	var jobProject, jobPull, resultPull, artifactPull int64
	if err := db.Pool().QueryRow(ctx, `select project_id, pull_request_id from jobs where id = $1`, jobID).
		Scan(&jobProject, &jobPull); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx, `select pull_request_id from test_results where run_id = $1`, runID).
		Scan(&resultPull); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx, `select pull_request_id from plan_artifacts where run_id = $1`, run.ID).
		Scan(&artifactPull); err != nil {
		t.Fatal(err)
	}
	if jobProject != projectID || jobPull != pull.ID || resultPull != pull.ID || artifactPull != pull.ID {
		t.Errorf("job project %d pull %d, result pull %d, artifact pull %d, want project %d pull %d",
			jobProject, jobPull, resultPull, artifactPull, projectID, pull.ID)
	}
}

// An early pass asked for is taken once.
func TestAnEarlyPassIsTakenOnce(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	projectID := project(t, db, "team/app")
	if err := db.RecordSyncSuccess(ctx, PullRequestSyncSource("gitea"), projectID, "2026-11-02T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.RequestSync(ctx, PullRequestSyncSource("gitea"), projectID); err != nil {
			t.Fatal(err)
		}
	}
	taken, err := db.TakeSyncRequests(ctx)
	if err != nil || len(taken) != 1 || taken[0].ProjectID != projectID || taken[0].Source != "gitea.pull_request_sync" {
		t.Fatalf("taken = %+v, %v", taken, err)
	}
	if again, _ := db.TakeSyncRequests(ctx); len(again) != 0 {
		t.Errorf("taken twice: %+v", again)
	}
	if state, _ := db.GetSyncState(ctx, PullRequestSyncSource("gitea"), projectID); state.Cursor != "2026-11-02T10:00:00Z" {
		t.Errorf("the request moved the cursor: %+v", state)
	}
}

// A pull request's results are read by its scope, and its latest push compared with the previous.
func TestAPullRequestsPushesAreCompared(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	projectID := project(t, db, "team/app")
	pull, _, err := db.UpsertPullRequest(ctx, openPull(projectID, 7, "aaa", time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	run := func(name string, status facts.CaseStatus) {
		t.Helper()
		jobID, err := db.Enqueue(ctx, JobRequest{Kind: "php/static-tests", PullRequestID: &pull.ID})
		if err != nil {
			t.Fatal(err)
		}
		record := runningRecord(name)
		record.JobID = jobID
		runID, err := db.UpsertRun(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveFacts(ctx, InterpretedRun{
			RunID: runID, Producer: facts.Producer{Plugin: "php", Version: "1.0.0"}, Capability: "php/static-tests",
			Sets: []facts.Set{{Shape: facts.ShapeCases, Group: "static", Tool: "phpstan",
				Cases: []facts.Case{{Suite: "src/A.php", Name: "rule", Status: status}}}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	run("first-push", facts.CasePassed)
	if _, err := db.DiffLatestRuns(ctx, PullRequestScope(pull.ID), "static"); err != ErrNothingToCompare {
		t.Errorf("a single push compared: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	run("second-push", facts.CaseFailed)

	reports, err := db.ListTestResults(ctx, PullRequestScope(pull.ID))
	if err != nil || len(reports) != 2 {
		t.Fatalf("reports = %d, %v", len(reports), err)
	}
	if releases, _ := db.ListTestResults(ctx, ReleaseScope(pull.ID)); len(releases) != 0 {
		t.Errorf("a release with the same id reads the pull request's results: %d", len(releases))
	}
	diffs, err := db.DiffLatestRuns(ctx, PullRequestScope(pull.ID), "static")
	if err != nil || len(diffs) != 1 || diffs[0].Change != "new-failure" {
		t.Errorf("diffs = %+v, %v", diffs, err)
	}
}
