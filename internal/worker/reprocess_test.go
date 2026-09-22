package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeReprocessStore struct {
	targets []store.ReprocessTarget
	deleted []int64
	err     error
}

func (f *fakeReprocessStore) ListReprocessTargets(context.Context, int64) ([]store.ReprocessTarget, error) {
	return f.targets, f.err
}

func (f *fakeReprocessStore) DeleteTestResults(_ context.Context, runID int64) (int64, error) {
	f.deleted = append(f.deleted, runID)
	return 2, nil
}

func newReprocessor(targets *fakeReprocessStore, objects fakeObjects) (*fakeTestStore, *Reprocessor) {
	tests := &fakeTestStore{known: map[string]bool{}}
	discard := slog.New(slog.DiscardHandler)
	return tests, NewReprocessor(targets,
		NewTestIngester(tests, objects, discard), nil, nil, discard)
}

// The replay erases before rebuilding. Stacking would duplicate every case and skew any
// comparison between releases — precisely what the replay serves to fix.
func TestReprocessDeletesBeforeRebuilding(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1", Tools: []string{"phpunit"}},
	}}
	objects := fakeObjects{"raw/unit-1/junit.xml": junitXML}
	tests, reprocessor := newReprocessor(targets, objects)

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if len(targets.deleted) != 1 || targets.deleted[0] != 1 {
		t.Errorf("effacements = %v", targets.deleted)
	}
	if len(tests.saved) != 1 {
		t.Fatalf("%d rapport(s) reconstruit(s)", len(tests.saved))
	}
	if report.Replayed != 1 || report.Deleted != 2 {
		t.Errorf("rapport = %+v", report)
	}
}

// The replay destroys before rebuilding: being able to see what would disappear, destroying
// nothing, is what makes the operation defensible.
func TestDryRunTouchesNothing(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1", Tools: []string{"phpunit"}},
	}}
	tests, reprocessor := newReprocessor(targets, fakeObjects{"raw/unit-1/junit.xml": junitXML})

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{DryRun: true})
	if err != nil {
		t.Fatalf("simulation: %v", err)
	}

	if len(targets.deleted) != 0 {
		t.Error("the dry run must erase nothing")
	}
	if len(tests.saved) != 0 {
		t.Error("the dry run must rebuild nothing")
	}
	// It accounts for what it would have touched.
	if report.Examined != 1 || report.Replayed != 0 {
		t.Errorf("rapport = %+v", report)
	}
}

// Replaying fifty runs and losing everything because the third has a purged artifact would be a
// regression worse than the original defect.
func TestAFailingRunDoesNotStopTheOthers(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "purge", Tools: []string{"phpunit"}},
		{RunID: 2, RunName: "unit-2", Tools: []string{"phpunit"}},
	}}
	// `purge` no longer has a readable artifact: its object exists in the list but reading it fails.
	objects := fakeObjects{
		"raw/purge/junit.xml":  "",
		"raw/unit-2/junit.xml": junitXML,
	}
	tests := &fakeTestStore{known: map[string]bool{}}
	discard := slog.New(slog.DiscardHandler)
	reprocessor := NewReprocessor(targets,
		NewTestIngester(tests, brokenObjects{objects, "raw/purge/junit.xml"}, discard),
		nil, nil, discard)

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if report.Replayed != 1 {
		t.Errorf("%d run(s) replayed, expected 1", report.Replayed)
	}
	// Failures are named, not counted: knowing which ones is what allows deciding whether to worry.
	if len(report.Failed) != 1 || report.Failed[0] != "purge" {
		t.Errorf("failures = %v", report.Failed)
	}
	if len(tests.saved) != 1 {
		t.Errorf("%d rapport(s) reconstruit(s)", len(tests.saved))
	}
}

// brokenObjects fails the reading of one precise key, as an object purged by retention would.
type brokenObjects struct {
	fakeObjects
	broken string
}

func (b brokenObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == b.broken {
		return nil, errors.New("purged object")
	}
	return b.fakeObjects.Get(ctx, key)
}

func TestReprocessWithNothingToReplay(t *testing.T) {
	targets := &fakeReprocessStore{}
	_, reprocessor := newReprocessor(targets, fakeObjects{})

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if report.Examined != 0 || report.Replayed != 0 {
		t.Errorf("rapport = %+v", report)
	}
}
