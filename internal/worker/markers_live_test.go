package worker

import (
	"context"
	"os"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
)

/*
 * Checking the marker channel against a log that is **really sealed**, guarded by
 * `MARKERS_LIVE=1` like the Redmine and forge checks.
 *
 * The channel's unit tests serve a log from a map: they exercise the logic and nothing more.
 * What they cannot say is whether the reader really returns the complete text of a real object
 * — a sealed log is written in chunks, then recomposed, and that is exactly the kind of
 * assembly that works on a fixture and not on the data.
 *
 * What this test covers, and nothing else covered: the chain MinIO → logs.Reader →
 * MarkerIngester on a run the dashboard itself scheduled, collected and sealed.
 *
 * Expected variables:
 *
 *   MARKERS_LIVE=1
 *   MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, MINIO_BUCKET
 *   MARKERS_LIVE_RUN   name of the PipelineRun
 *   MARKERS_LIVE_TASKS names of the tasks, comma-separated
 *   MARKER_TOKEN       token of the deployment observed (`MSE` here)
 */

func liveMarkerSource(t *testing.T) (*logs.Reader, string, []string) {
	t.Helper()

	if os.Getenv("MARKERS_LIVE") != "1" {
		t.Skip("MARKERS_LIVE != 1: the check against a real log is skipped")
	}

	run := os.Getenv("MARKERS_LIVE_RUN")
	tasks := strings.Split(os.Getenv("MARKERS_LIVE_TASKS"), ",")
	if run == "" || len(tasks) == 0 || tasks[0] == "" {
		t.Skip("MARKERS_LIVE_RUN or MARKERS_LIVE_TASKS not set")
	}

	storage, err := blob.Open(context.Background(), config.StorageConfig{
		Endpoint:  os.Getenv("MINIO_ENDPOINT"),
		Bucket:    os.Getenv("MINIO_BUCKET"),
		AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Region:    "NUO-Region",
		UseSSL:    os.Getenv("MINIO_USE_SSL") == "true",
	})
	if err != nil {
		t.Fatalf("stockage objet: %v", err)
	}
	return logs.NewReader(storage), run, tasks
}

// A real run's sealed log must return its descriptors, whole and parsable.
//
// The assertion bears on what is **read**, not on what is recorded: a run whose every tool
// concluded records nothing, and would therefore prove nothing about the reading path.
func TestLiveTheDescriptorsOfASealedRunAreRead(t *testing.T) {
	reader, run, tasks := liveMarkerSource(t)

	set := results.NewMarkerSet(os.Getenv("MARKER_TOKEN"))
	ingester := NewMarkerIngester(&fakeMarkerStore{existing: map[string]bool{}}, reader, set, quietLogger())

	found := map[string]results.Meta{}
	var read int
	for _, task := range tasks {
		task = strings.TrimSpace(task)
		text, err := ingester.readTask(context.Background(), run, task)
		if err != nil {
			t.Fatalf("reading %s/%s: %v", run, task, err)
		}
		read += len(text)
		// The start of the log is returned whatever happens: when no descriptor is found, the next
		// question is always "what was in that log then", and the failure must answer it on its own.
		t.Logf("%s: %d bytes, start: %s", task, len(text), firstLines(text, 3))
		// The same timestamp stripping as the ingester: without it, this test would miss descriptors
		// that are nonetheless present — exactly the defect it served to find.
		for tool, meta := range set.ScanMarkers(logs.Plain(text)).Metas {
			found[tool] = meta
		}
	}

	if read == 0 {
		t.Fatal("no byte read: the reader returns nothing of the sealed log")
	}
	if len(found) == 0 {
		t.Fatalf("%d bytes read, no descriptor found: either the token %q does not match "+
			"what the Tasks emit, or the log is truncated", read, set.Meta)
	}

	for tool, meta := range found {
		t.Logf("descripteur lu: outil=%s statut=%s code=%d", tool, meta.Status, meta.ExitCode)
		if tool == "" {
			t.Error("a descriptor with no tool can be attached to nothing")
		}
	}
}

// And the counterpart, which is the property easiest to break without noticing: a run whose
// every tool concluded must record no result. A false positive here would mark as "not
// measured" a family that was, and take a perfectly valid signal out of the score.
func TestLiveAHealthyRunRecordsNothing(t *testing.T) {
	reader, run, tasks := liveMarkerSource(t)

	set := results.NewMarkerSet(os.Getenv("MARKER_TOKEN"))
	db := &fakeMarkerStore{existing: map[string]bool{}}
	ingester := NewMarkerIngester(db, reader, set, quietLogger())

	trimmed := make([]string, 0, len(tasks))
	for _, task := range tasks {
		trimmed = append(trimmed, strings.TrimSpace(task))
	}

	err := ingester.Ingest(context.Background(), MarkerRun{
		ID: -1, Name: run, Tasks: trimmed, Family: "static",
	})
	if err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	for _, saved := range db.saved {
		// A record is a fault only if the tool had concluded. The test is run on a successful run, so
		// the list must be empty; if it is not, the log says which one and why.
		t.Errorf("result recorded for a healthy run: tool=%s status=%s summary=%q",
			saved.Tool, saved.Status, saved.Summary)
	}
}

// firstLines returns a text's first lines, for diagnosis.
func firstLines(text string, count int) string {
	lines := strings.SplitN(strings.TrimSpace(text), "\n", count+1)
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, " | ")
}
