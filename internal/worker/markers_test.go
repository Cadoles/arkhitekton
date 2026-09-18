package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/logs"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// fakeLogSource serves one log per task, in a single chunk.
type fakeLogSource struct {
	byTask map[string]string
	// slice forces reading in chunks, to exercise the pagination loop: the real reader does not
	// return everything at once.
	slice int
}

func (f *fakeLogSource) From(
	_ context.Context, _ string, taskName string, offset, _ int64,
) (logs.Segment, error) {
	text, found := f.byTask[taskName]
	if !found {
		return logs.Segment{}, errors.New("log missing")
	}

	size := int64(len(text))
	if offset >= size {
		return logs.Segment{Offset: size, NextOffset: size, Size: size, Sealed: true}, nil
	}

	end := size
	if f.slice > 0 && offset+int64(f.slice) < size {
		end = offset + int64(f.slice)
	}
	return logs.Segment{
		Content: []byte(text[offset:end]), Offset: offset,
		NextOffset: end, Size: size, Sealed: true,
	}, nil
}

type fakeMarkerStore struct {
	saved    []store.TestResult
	existing map[string]bool
}

func (f *fakeMarkerStore) SaveTestResult(
	_ context.Context, result store.TestResult, _ []store.TestCase,
) (int64, error) {
	f.saved = append(f.saved, result)
	return int64(len(f.saved)), nil
}

func (f *fakeMarkerStore) TestResultExists(_ context.Context, _ int64, tool string) (bool, error) {
	return f.existing[tool], nil
}

func markerIngester(t *testing.T, source *fakeLogSource) (*MarkerIngester, *fakeMarkerStore) {
	t.Helper()
	db := &fakeMarkerStore{existing: map[string]bool{}}
	return NewMarkerIngester(db, source, results.NewMarkerSet("MSE"), quietLogger()), db
}

// The case the channel existed to preserve: a crashed scanner publishes zero violations,
// exactly like healthy code. Without the descriptor, it displayed as "compliant".
func TestAToolThatDidNotConcludeBecomesNotMeasured(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"audit": `Running trivy...
##MSE-META## {"tool":"trivy","exit_code":0,"status":"error","error":"db download failed"}
##MSE-SUMMARY## audit interrompu
`,
	}})

	err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 7, Name: "security-audit-run-abc", Tasks: []string{"audit"}, Family: "security",
	})
	if err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	if len(db.saved) != 1 {
		t.Fatalf("%d result(s) recorded, expected 1: %+v", len(db.saved), db.saved)
	}
	saved := db.saved[0]
	// `na` and not `fail`: it is not a verdict on the code, it is the absence of a verdict. The
	// score takes this signal out of the computation instead of counting a wrong zero.
	if saved.Status != results.ReportNA {
		t.Errorf("status = %q, expected %q", saved.Status, results.ReportNA)
	}
	if saved.Tool != "trivy" || saved.Family != "security" || saved.RunID != 7 {
		t.Errorf("result = %+v", saved)
	}
	// The cause comes from the pipeline: an empty cell would read as a measurement missing for no
	// reason, which is half the information.
	if saved.Summary != "db download failed" {
		t.Errorf("summary = %q", saved.Summary)
	}
}

// A tool that concluded is not recorded here: its artifact carries the count, and drawing a
// second result from it would give two rows for one run with nothing saying which is
// authoritative.
func TestAToolThatConcludedIsNotRecorded(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"tests": `##MSE-META## {"tool":"phpstan","exit_code":1,"status":"ok"}` + "\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 8, Name: "static-run", Tasks: []string{"tests"}, Family: "static",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	// `exit_code: 1` is a **success** for PHPStan: it exits with 1 as soon as it finds a
	// violation. Confusing the two would make every fruitful analysis look like a broken tool.
	if len(db.saved) != 0 {
		t.Errorf("a tool that concluded must record nothing here: %+v", db.saved)
	}
}

// The artifact wins when it exists: ingestion is idempotent per tool, and recording again
// would double the row.
func TestAResultAlreadyPresentIsNotDoubled(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"tests": `##MSE-META## {"tool":"phpunit","exit_code":255,"status":"error"}` + "\n",
	}})
	db.existing["phpunit"] = true

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 9, Name: "unit-run", Tasks: []string{"tests"}, Family: "unit",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 0 {
		t.Errorf("no duplicate expected: %+v", db.saved)
	}
}

// A task with no log must not carry off the others' markers: it may have been skipped, or its
// collector sealed nothing.
func TestATaskWithNoLogDoesNotCarryOffTheOthers(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"seconde": `##MSE-META## {"tool":"gitleaks","status":"error","error":"timeout"}` + "\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 10, Name: "audit-run", Tasks: []string{"missing", "seconde"}, Family: "security",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 1 || db.saved[0].Tool != "gitleaks" {
		t.Errorf("results = %+v", db.saved)
	}
}

// The log is read in chunks, the real reader not returning everything at once. A marker
// straddling two chunks must stay readable.
func TestTheLogIsReadToTheEnd(t *testing.T) {
	descriptor := `##MSE-META## {"tool":"cypress","status":"error","error":"browser missing"}`
	ingester, db := markerIngester(t, &fakeLogSource{
		byTask: map[string]string{"e2e": strings.Repeat("bruit\n", 200) + descriptor + "\n"},
		slice:  17,
	})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 11, Name: "e2e-run", Tasks: []string{"e2e"}, Family: "e2e",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 1 || db.saved[0].Summary != "browser missing" {
		t.Errorf("results = %+v", db.saved)
	}
}

// With no family, nothing is recorded: a result attaching to no signal would appear nowhere,
// and would take up a row for nothing.
func TestWithNoFamilyNothingIsRecorded(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"build": `##MSE-META## {"tool":"buildx","status":"error"}` + "\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 12, Name: "build-run", Tasks: []string{"build"},
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(db.saved) != 0 {
		t.Errorf("no record expected with no family: %+v", db.saved)
	}
}

// The family comes from the catalogue, which already declares it in order to file the
// artifacts. A pipeline declaring none, or several, gives nothing to file by.
func TestTheFamilyComesFromTheCatalogue(t *testing.T) {
	pipelines := &catalog.Catalog{Pipelines: []catalog.Pipeline{
		{Kind: "unit-tests", Uploads: map[string]catalog.Upload{"rapport": {Family: "unit"}}},
		{Kind: "build"},
		{Kind: "mixte", Uploads: map[string]catalog.Upload{"a": {Family: "unit"}, "b": {Family: "e2e"}}},
	}}

	if run, ok := markerRunFor(pipelines, "unit-tests", MarkerRun{ID: 1}); !ok || run.Family != "unit" {
		t.Errorf("unit-tests: ok=%v famille=%q", ok, run.Family)
	}
	if _, ok := markerRunFor(pipelines, "build", MarkerRun{ID: 1}); ok {
		t.Error("a pipeline with no artifact measures no family")
	}
	// Two families: nothing says which one a tool attaches to, and filing at random would move a
	// verdict onto a signal that was not measured.
	if _, ok := markerRunFor(pipelines, "mixte", MarkerRun{ID: 1}); ok {
		t.Error("a pipeline with two families gives nothing to file by")
	}
	if _, ok := markerRunFor(nil, "unit-tests", MarkerRun{ID: 1}); ok {
		t.Error("with no catalogue, nothing can be filed")
	}
}

// The stored chunks keep the timestamp kubelet added, and the marker is therefore **not** at
// the start of the line. That is what made the channel recognise nothing on real logs although
// every unit test passed: their fixtures carried none.
func TestAMarkerPrecededByATimestampIsRecognised(t *testing.T) {
	ingester, db := markerIngester(t, &fakeLogSource{byTask: map[string]string{
		"audit": "2026-08-25T14:33:39.746477394Z Running trivy...\n" +
			`2026-08-25T14:33:39.746667427Z ##MSE-META## {"tool":"trivy","status":"error","error":"db download failed"}` + "\n" +
			"2026-08-25T14:33:39.746678103Z ##MSE-SUMMARY## audit interrompu\n",
	}})

	if err := ingester.Ingest(context.Background(), MarkerRun{
		ID: 13, Name: "audit-run", Tasks: []string{"audit"}, Family: "security",
	}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	if len(db.saved) != 1 {
		t.Fatalf("%d result(s), expected 1: the timestamped marker is not recognised", len(db.saved))
	}
	if db.saved[0].Tool != "trivy" || db.saved[0].Summary != "db download failed" {
		t.Errorf("result = %+v", db.saved[0])
	}
}
