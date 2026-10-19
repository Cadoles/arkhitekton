package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability/interpret"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/facts"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
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

func testRegistry(t *testing.T, tool string) *capability.Registry {
	t.Helper()
	registry, err := capability.NewRegistry(capability.Plugin{ID: "php", Version: "2.0.0", Pipelines: []capability.Pipeline{{
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
