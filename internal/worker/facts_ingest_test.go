package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"path"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capabilities"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

type factStore struct {
	snapshots map[int64]store.RunSnapshot
	jobs      map[int64]store.Job
	saved     []store.InterpretedRun
}

func (s *factStore) RunSnapshot(_ context.Context, id int64) (store.RunSnapshot, error) {
	return s.snapshots[id], nil
}

func (s *factStore) GetJob(_ context.Context, id int64) (store.Job, error) {
	return s.jobs[id], nil
}

func (s *factStore) StartConditions(context.Context, int64) (bool, int, error) { return true, 0, nil }

func (s *factStore) SaveFacts(_ context.Context, run store.InterpretedRun) error {
	for _, saved := range s.saved {
		if saved.RunID == run.RunID {
			return store.ErrAlreadyInterpreted
		}
	}
	s.saved = append(s.saved, run)
	return nil
}

type memoryObjects map[string][]byte

func (o memoryObjects) List(_ context.Context, prefix string) ([]ObjectRef, error) {
	var refs []ObjectRef
	for key, content := range o {
		if strings.HasPrefix(key, prefix) {
			refs = append(refs, ObjectRef{Key: key, Size: int64(len(content))})
		}
	}
	return refs, nil
}

func (o memoryObjects) Put(_ context.Context, key string, reader io.Reader, _ int64, contentType string) error {
	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	o[key] = content
	o[key+"#type"] = []byte(contentType)
	return nil
}

func (o memoryObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	content, found := o[key]
	if !found {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = writer.Close()
	_ = gz.Close()
	return buffer.Bytes()
}

// unitReader reads JUnit records the usual way, and refuses to make up anything else.
type unitReader struct{ tool string }

func (r unitReader) Interpret(_ context.Context, _ capability.RunState, records []parse.Record) ([]facts.Set, error) {
	var sets []facts.Set
	for _, record := range records {
		report, ok := record.Payload.(parse.JUnit)
		if !ok {
			continue
		}
		sets = append(sets, facts.Set{
			Shape: facts.ShapeCases, Group: "unit", Tool: r.tool, RawObject: record.Object,
			Cases: interpret.JUnitCases(report),
		})
	}
	return sets, nil
}

func testRegistry(t *testing.T, tool string) *capabilities.Registry {
	t.Helper()
	registry, err := capabilities.New(capability.Plugin{ID: "php", Version: "2.0.0", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{{
		Name: "unit-tests", Interpreter: unitReader{tool: tool},
		Spec: capability.PipelineSpec{
			PipelineRef: "php-unit-tests", ObservedPipelines: []string{"nightly-unit"},
			Produces: []capability.Product{capability.ProductResults},
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "unit-tests.tar.gz",
				Format:  capability.FormatArchive,
				Members: []capability.Member{{Match: "unit/*.xml", Parser: parse.JUnitXML, Tool: "phpunit"}},
			}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

const junitReport = `<testsuite name="s"><testcase classname="A" name="ok"/><testcase classname="A" name="ko"><failure message="no"/></testcase></testsuite>`

func finished(id int64, name, pipeline string, job *int64, release *int64, tasks ...store.TaskSnapshot) store.RunSnapshot {
	return store.RunSnapshot{ID: id, Name: name, Pipeline: pipeline, Status: "failed",
		JobID: job, ReleaseID: release, Tasks: tasks}
}

func TestALaunchedRunIsReadByItsCapability(t *testing.T) {
	job, release := int64(3), int64(9)
	db := &factStore{
		snapshots: map[int64]store.RunSnapshot{1: finished(1, "mse-x-php-unit-tests-3", "php-unit-tests", &job, &release,
			store.TaskSnapshot{Name: "run-unit-tests", Status: "failed"})},
		jobs: map[int64]store.Job{3: {ID: 3, Kind: "php/unit-tests", Capability: "php/unit-tests", PluginVersion: "2.0.0"}},
	}
	objects := memoryObjects{"raw/mse-x-php-unit-tests-3/unit-tests.tar.gz": tarball(t, map[string]string{
		"test-results/unit/junit.xml": junitReport, "test-results/unit/notes.txt": "ignored",
	})}

	ingester := NewFactIngester(db, objects, testRegistry(t, "phpunit"), slog.New(slog.DiscardHandler))
	if err := ingester.Ingest(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	if len(db.saved) != 1 {
		t.Fatalf("saved %d run(s)", len(db.saved))
	}
	saved := db.saved[0]
	if saved.Producer.String() != "php@2.0.0" || saved.Capability != "php/unit-tests" ||
		saved.ReleaseID == nil || *saved.ReleaseID != 9 || !saved.Exclusive {
		t.Errorf("run = %+v", saved)
	}
	if len(saved.Sets) != 1 || saved.Sets[0].Verdict() != facts.StatusFail ||
		saved.Sets[0].RawObject != "raw/mse-x-php-unit-tests-3/unit-tests.tar.gz" {
		t.Errorf("sets = %+v", saved.Sets)
	}

	// The next event about the same run reads nothing again.
	if err := ingester.Ingest(context.Background(), 1); err != nil || len(db.saved) != 1 {
		t.Errorf("read again: err %v, %d saved", err, len(db.saved))
	}
}

// The PipelineRun may end before its last TaskRun is recorded: reading then would take a task
// still running for one that never ran.
func TestARunIsReadOnlyOnceEveryTaskIsOver(t *testing.T) {
	db := &factStore{snapshots: map[int64]store.RunSnapshot{1: finished(1, "nightly-1", "nightly-unit", nil, nil,
		store.TaskSnapshot{Name: "run-unit-tests", Status: "running"})}}
	ingester := NewFactIngester(db, memoryObjects{}, testRegistry(t, "phpunit"), slog.New(slog.DiscardHandler))

	if err := ingester.Ingest(context.Background(), 1); err != nil || len(db.saved) != 0 {
		t.Errorf("read before its tasks ended: err %v, %d saved", err, len(db.saved))
	}
}

// A run the dashboard did not launch is read by the capability that observes its Pipeline, with no
// release: inventing one would put it in someone else's score.
func TestAnObservedRunIsReadWithNoRelease(t *testing.T) {
	db := &factStore{snapshots: map[int64]store.RunSnapshot{1: finished(1, "nightly-1", "nightly-unit", nil, nil)}}
	objects := memoryObjects{"raw/nightly-1/unit-tests.tar.gz": tarball(t, map[string]string{"unit/junit.xml": junitReport})}
	ingester := NewFactIngester(db, objects, testRegistry(t, "phpunit"), slog.New(slog.DiscardHandler))

	if err := ingester.Ingest(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(db.saved) != 1 || db.saved[0].ReleaseID != nil || len(db.saved[0].Sets) != 1 {
		t.Errorf("saved = %+v", db.saved)
	}
}

// An interpreter is plugin code: what it writes is checked before anyone sees it.
func TestInvalidFactsAreNotStored(t *testing.T) {
	db := &factStore{snapshots: map[int64]store.RunSnapshot{1: finished(1, "nightly-1", "nightly-unit", nil, nil)}}
	objects := memoryObjects{"raw/nightly-1/unit-tests.tar.gz": tarball(t, map[string]string{"unit/junit.xml": junitReport})}
	ingester := NewFactIngester(db, objects, testRegistry(t, ""), slog.New(slog.DiscardHandler))

	if err := ingester.Ingest(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(db.saved) != 1 || len(db.saved[0].Sets) != 0 {
		t.Errorf("a set with no tool was stored: %+v", db.saved)
	}
}

// videoReader attaches what the host kept, and, to check the host's guard, an object it did not.
type videoReader struct{ foreign bool }

func (r videoReader) Interpret(_ context.Context, _ capability.RunState, records []parse.Record) ([]facts.Set, error) {
	set := facts.Set{Shape: facts.ShapeAttachments, Group: "e2e", Tool: "cypress", Status: facts.StatusNA}
	for _, record := range records {
		if attached, ok := record.Payload.(parse.Attached); ok {
			set.Attachments = append(set.Attachments, facts.Attachment{
				Name: path.Base(record.Member), MediaType: attached.MediaType, Object: attached.Object, Size: attached.Size,
			})
		}
	}
	if r.foreign {
		set.Attachments = append(set.Attachments, facts.Attachment{
			Name: "x", MediaType: "text/plain", Object: "artifacts/releases/1.0.0/delivery-note.pdf",
		})
	}
	return []facts.Set{set}, nil
}

func videoRegistry(t *testing.T, reader capability.Interpreter, members ...capability.Member) *capabilities.Registry {
	t.Helper()
	registry, err := capabilities.New(capability.Plugin{ID: "mse", Version: "1.0.0", APIVersion: sdk.APIVersion,
		Pipelines: []capability.Pipeline{{
			Name: "e2e-tests", Interpreter: reader,
			Spec: capability.PipelineSpec{
				PipelineRef: "e2e-test", Produces: []capability.Product{capability.ProductResults},
				Artifacts: []capability.Artifact{
					{Name: "videos", Object: "e2e-videos.tar.gz", Format: capability.FormatArchive, Members: members},
					// Declared with nothing to read nor keep: never downloaded.
					{Name: "screenshots", Object: "e2e-screenshots.tar.gz", Format: capability.FormatArchive},
				},
			},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// A video in an archive is kept as an object of its own, typed by its name, and attached; the
// archive nothing reads is not even opened.
func TestAnArchivedVideoIsKeptAsAnAttachment(t *testing.T) {
	job := int64(3)
	db := &factStore{
		snapshots: map[int64]store.RunSnapshot{1: finished(1, "ark-x-mse-e2e-tests-3", "e2e-test", &job, nil)},
		jobs:      map[int64]store.Job{3: {ID: 3, Kind: "mse/e2e-tests", Capability: "mse/e2e-tests"}},
	}
	objects := memoryObjects{
		"raw/ark-x-mse-e2e-tests-3/e2e-videos.tar.gz": tarball(t, map[string]string{
			"videos/login.cy.js.mp4": "not really a video", "videos/notes.txt": "ignored",
		}),
		"raw/ark-x-mse-e2e-tests-3/e2e-screenshots.tar.gz": []byte("not even an archive"),
	}

	ingester := NewFactIngester(db, objects, videoRegistry(t, videoReader{},
		capability.Member{Match: "*/*.mp4", Parser: parse.Attach, Tool: "cypress"}), slog.New(slog.DiscardHandler))
	if err := ingester.Ingest(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	if len(db.saved) != 1 || len(db.saved[0].Sets) != 1 || len(db.saved[0].Sets[0].Attachments) != 1 {
		t.Fatalf("saved = %+v", db.saved)
	}
	video := db.saved[0].Sets[0].Attachments[0]
	if video.Name != "login.cy.js.mp4" || video.MediaType != "video/mp4" || video.Size != 18 ||
		!strings.HasPrefix(video.Object, "attachments/ark-x-mse-e2e-tests-3/") {
		t.Errorf("video = %+v", video)
	}
	if string(objects[video.Object]) != "not really a video" || string(objects[video.Object+"#type"]) != "video/mp4" {
		t.Errorf("stored %q as %q", objects[video.Object], objects[video.Object+"#type"])
	}
}

// An interpreter cannot attach an object the run did not keep: the dashboard would serve it under
// a release.
func TestAnAttachmentOfAnotherObjectIsRefused(t *testing.T) {
	job := int64(3)
	db := &factStore{
		snapshots: map[int64]store.RunSnapshot{1: finished(1, "ark-x-mse-e2e-tests-3", "e2e-test", &job, nil)},
		jobs:      map[int64]store.Job{3: {ID: 3, Kind: "mse/e2e-tests", Capability: "mse/e2e-tests"}},
	}
	objects := memoryObjects{"raw/ark-x-mse-e2e-tests-3/e2e-videos.tar.gz": tarball(t, map[string]string{
		"videos/login.cy.js.mp4": "video",
	})}

	ingester := NewFactIngester(db, objects, videoRegistry(t, videoReader{foreign: true},
		capability.Member{Match: "*/*.mp4", Parser: parse.Attach, Tool: "cypress"}), slog.New(slog.DiscardHandler))
	if err := ingester.Ingest(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(db.saved) != 1 || len(db.saved[0].Sets) != 0 {
		t.Errorf("a set attaching a foreign object was stored: %+v", db.saved)
	}
}
