package store

import (
	"context"
	"testing"
	"time"
)

// The counts keep to the projects asked for and to the period; a draft is not a launched release, a
// local task is not a pipeline, and the median duration is that of the runs that started.
func TestTheStatisticsCountThePeriodsReleasesAndJobs(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	other := project(t, db, "team/other")
	now := time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)
	since := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0", true)
	release := func(projectID int64, tag, state string, at time.Time) {
		t.Helper()
		id, err := db.CreateRelease(ctx, Release{
			ProjectID: projectID, VersionID: versionID, Tag: tag, Kind: "pre", State: "draft",
			GitRef: "main", ImageVersion: tag, CreatedBy: "test",
		})
		if err != nil {
			t.Fatalf("release %s: %v", tag, err)
		}
		if _, err := db.Pool().Exec(ctx, `update releases set state = $2, created_at = $3 where id = $1`,
			id, state, at); err != nil {
			t.Fatal(err)
		}
	}
	release(testProjectID, "1.8.0-rc1", "failed", now.AddDate(0, 0, -3))
	release(testProjectID, "1.8.0-rc2", "released", now.AddDate(0, 0, -2))
	release(testProjectID, "1.8.0-rc3", "draft", now.AddDate(0, 0, -1))
	release(testProjectID, "1.7.0-rc1", "released", now.AddDate(0, -2, 0))
	release(other, "9.0.0", "released", now.AddDate(0, 0, -1))

	project := int64(testProjectID)
	job := func(kind, state string, projectID int64, finished time.Time, seconds int) {
		t.Helper()
		id, err := db.Enqueue(ctx, JobRequest{Kind: kind, RequestedBy: "test", ProjectID: &projectID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool().Exec(ctx,
			`update jobs set state = $2, started_at = $3, finished_at = $4 where id = $1`,
			id, state, finished.Add(-time.Duration(seconds)*time.Second), finished); err != nil {
			t.Fatal(err)
		}
	}
	job("docker/build", "succeeded", project, now.AddDate(0, 0, -1), 60)
	job("docker/build", "succeeded", project, now.AddDate(0, 0, -2), 120)
	job("docker/build", "failed", project, now.AddDate(0, 0, -3), 30)
	job("docker/build", "succeeded", project, now.AddDate(0, -2, 0), 600)
	job("docker/build", "succeeded", other, now.AddDate(0, 0, -1), 600)
	job("php/unit-tests", "cancelled", project, now.AddDate(0, 0, -1), 10)
	job("release-notes", "succeeded", project, now.AddDate(0, 0, -1), 1)

	counts, err := db.ReleaseStats(ctx, []int64{testProjectID}, since, StatsWeek)
	if err != nil {
		t.Fatal(err)
	}
	total, byState := 0, map[string]int{}
	for _, count := range counts {
		total += count.Count
		byState[count.State] += count.Count
		if count.Bucket.Weekday() != time.Monday {
			t.Errorf("bucket %s is not a Monday", count.Bucket)
		}
	}
	if total != 2 || byState["failed"] != 1 || byState["released"] != 1 {
		t.Errorf("releases %+v", counts)
	}

	stats, err := db.PipelineStats(ctx, []int64{testProjectID}, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Kind != "docker/build" || stats[0].Runs != 3 || stats[0].Succeeded != 2 ||
		stats[0].Failed != 1 || stats[0].MedianSeconds == nil || *stats[0].MedianSeconds != 60 {
		t.Fatalf("pipelines %+v", stats)
	}
	if stats[1].Kind != "php/unit-tests" || stats[1].Cancelled != 1 {
		t.Errorf("second kind %+v", stats[1])
	}
}
