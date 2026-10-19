package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeReprocessStore struct {
	targets   []store.ReprocessTarget
	reset     []int64
	baselines []store.PinnedBaseline
	restored  map[int64][]store.PinnedBaseline
	err       error
}

func (f *fakeReprocessStore) ListReprocessTargets(context.Context, int64) ([]store.ReprocessTarget, error) {
	return f.targets, f.err
}

func (f *fakeReprocessStore) ResetInterpretation(_ context.Context, runID int64) (int64, []store.PinnedBaseline, error) {
	f.reset = append(f.reset, runID)
	return 2, f.baselines, nil
}

func (f *fakeReprocessStore) RestoreBaselines(_ context.Context, runID int64, baselines []store.PinnedBaseline) (int, error) {
	if f.restored == nil {
		f.restored = map[int64][]store.PinnedBaseline{}
	}
	f.restored[runID] = baselines
	return len(baselines), nil
}

type recordingReader struct {
	read []int64
	err  error
}

func (r *recordingReader) Ingest(_ context.Context, runID int64) error {
	r.read = append(r.read, runID)
	return r.err
}

func reprocessorFor(targets *fakeReprocessStore, reader *recordingReader) *Reprocessor {
	return NewReprocessor(targets, reader, map[string]string{"php": "1.0.0"}, slog.New(slog.DiscardHandler))
}

// The replay erases, then reads again. Stacking would duplicate every case and skew any comparison
// between releases, precisely what the replay serves to fix.
func TestReprocessResetsBeforeReading(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1", Producers: []string{"php@0.0.0-legacy"}},
	}}
	reader := &recordingReader{}

	report, err := reprocessorFor(targets, reader).Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.reset) != 1 || len(reader.read) != 1 || report.Replayed != 1 || report.Deleted != 2 {
		t.Errorf("reset %v, read %v, report %+v", targets.reset, reader.read, report)
	}
}

// The replay destroys before rebuilding: seeing what would be replayed, destroying nothing, is
// what makes it defensible.
func TestADryRunDestroysNothing(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{{RunID: 1, RunName: "unit-1"}}}
	reader := &recordingReader{}

	report, _ := reprocessorFor(targets, reader).Run(context.Background(), ReprocessOptions{DryRun: true})
	if len(targets.reset) != 0 || len(reader.read) != 0 || report.Examined != 1 {
		t.Errorf("reset %v, read %v, report %+v", targets.reset, reader.read, report)
	}
}

// A run another plugin version read is not replayed unless asked: reading it again with the
// installed version changes its history.
func TestARunReadByAnotherVersionIsRefused(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1", Producers: []string{"php@0.9.0"}},
	}}
	reader := &recordingReader{}

	report, _ := reprocessorFor(targets, reader).Run(context.Background(), ReprocessOptions{})
	if len(report.Refused) != 1 || len(targets.reset) != 0 {
		t.Errorf("refused %v, reset %v", report.Refused, targets.reset)
	}

	report, _ = reprocessorFor(targets, reader).Run(context.Background(), ReprocessOptions{AcceptDrift: true})
	if report.Replayed != 1 {
		t.Errorf("accepted drift: %+v", report)
	}
}

// A load baseline is erased with its measure: the replay pins it again on the new one.
func TestPinnedBaselinesSurviveTheReplay(t *testing.T) {
	baseline := store.PinnedBaseline{Scenario: "checkout", Metric: "http_req_duration:p(95)", PinnedBy: "alice"}
	targets := &fakeReprocessStore{
		targets:   []store.ReprocessTarget{{RunID: 4, RunName: "load-4", Producers: []string{"k6@0.0.0-legacy"}}},
		baselines: []store.PinnedBaseline{baseline},
	}

	if _, err := reprocessorFor(targets, &recordingReader{}).Run(context.Background(), ReprocessOptions{}); err != nil {
		t.Fatal(err)
	}
	if restored := targets.restored[4]; len(restored) != 1 || restored[0] != baseline {
		t.Errorf("restored = %v", targets.restored)
	}
}

// One run's failure does not carry off the others.
func TestAFailedRunDoesNotStopTheReplay(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1"}, {RunID: 2, RunName: "unit-2"},
	}}
	reader := &recordingReader{err: errors.New("archive unreadable")}

	report, err := reprocessorFor(targets, reader).Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failed) != 2 || len(reader.read) != 2 {
		t.Errorf("report %+v, read %v", report, reader.read)
	}
}
