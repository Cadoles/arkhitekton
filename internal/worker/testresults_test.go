package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeTestStore struct {
	saved []store.TestResult
	cases [][]store.TestCase
	known map[string]bool
}

func (f *fakeTestStore) SaveTestResult(
	_ context.Context, result store.TestResult, cases []store.TestCase,
) (int64, error) {
	f.saved = append(f.saved, result)
	f.cases = append(f.cases, cases)
	return int64(len(f.saved)), nil
}

func (f *fakeTestStore) TestResultExists(_ context.Context, _ int64, tool string) (bool, error) {
	return f.known[tool], nil
}

const junitXML = `<testsuites><testsuite name="Unit" time="1.0">
  <testcase name="ok" classname="A" time="0.5"/>
  <testcase name="ko" classname="A" time="0.5"><failure message="boom">trace</failure></testcase>
</testsuite></testsuites>`

const phpstanJSON = `{"totals":{"errors":0,"file_errors":1},"files":{
  "src/A.php":{"errors":1,"messages":[{"message":"m","line":3,"identifier":"r"}]}},"errors":[]}`

// archive builds a tar.gz, the form in which a directory is published.
func archive(t *testing.T, files map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)

	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}
	writer.Close()
	gz.Close()
	return buffer.String()
}

func newTestIngester(objects fakeObjects) (*fakeTestStore, *TestIngester) {
	results := &fakeTestStore{known: map[string]bool{}}
	return results, NewTestIngester(results, objects, slog.New(slog.DiscardHandler))
}

func TestIngestReadsReportsFromAnArchive(t *testing.T) {
	objects := fakeObjects{
		"raw/static-tests-1/static-tests.tar.gz": archive(t, map[string]string{
			"junit.xml":    junitXML,
			"phpstan.json": phpstanJSON,
			// Neither a report nor a known format: ignored without failing the rest.
			"phpunit.log": "bruit",
		}),
	}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "static-tests-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	tools := map[string]store.TestResult{}
	for _, result := range results.saved {
		tools[result.Tool] = result
	}
	if len(tools) != 2 {
		t.Fatalf("tools recorded = %v", tools)
	}
	if tools["phpunit"].Failed != 1 || tools["phpunit"].Status != "fail" {
		t.Errorf("phpunit = %+v", tools["phpunit"])
	}
	if tools["phpstan"].Family != "static" {
		t.Errorf("phpstan = %+v", tools["phpstan"])
	}
}

func TestIngestReadsAStandaloneFile(t *testing.T) {
	objects := fakeObjects{"raw/unit-1/junit.xml": junitXML}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "unit-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(results.saved) != 1 {
		t.Fatalf("%d rapport(s)", len(results.saved))
	}
	// Le lien vers le brut permet de rejouer l'ingestion depuis l'objet d'origine.
	if results.saved[0].RawObject != "raw/unit-1/junit.xml" {
		t.Errorf("objet source = %q", results.saved[0].RawObject)
	}
}

// The observer sees a finished run again at restart. Recording again would double every case,
// therefore every comparison with the previous release.
func TestIngestIsIdempotentPerTool(t *testing.T) {
	objects := fakeObjects{"raw/unit-1/junit.xml": junitXML}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "unit-1"}); err != nil {
		t.Fatalf("first ingestion: %v", err)
	}
	results.known["phpunit"] = true

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 2, Name: "unit-1"}); err != nil {
		t.Fatalf("seconde ingestion: %v", err)
	}
	if len(results.saved) != 1 {
		t.Errorf("%d report(s), expected 1", len(results.saved))
	}
}

// Load measurements are not test cases: passing them through here would project them into a
// model that does not suit them.
func TestLoadArtifactsAreLeftToTheirOwnIngester(t *testing.T) {
	objects := fakeObjects{"raw/load-test-1/k6/panier-summary.json": `{"metrics":{}}`}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 3, Name: "load-test-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(results.saved) != 0 {
		t.Errorf("%d report(s) recorded", len(results.saved))
	}
}

// A campaign producing four reports must not be lost whole because one of them is truncated.
func TestOneBadReportDoesNotLoseTheOthers(t *testing.T) {
	objects := fakeObjects{
		"raw/static-tests-1/static-tests.tar.gz": archive(t, map[string]string{
			"junit.xml":    junitXML,
			"phpstan.json": `{"totals":`,
		}),
	}
	results, ingester := newTestIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 4, Name: "static-tests-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(results.saved) != 1 || results.saved[0].Tool != "phpunit" {
		t.Errorf("rapports = %+v", results.saved)
	}
}

// A Cypress archive weighs mostly videos: fetching them into memory only to throw them away at
// once would be absurd.
func TestUnknownFilesAreNotLoaded(t *testing.T) {
	files, err := extract("x.tar.gz", bytes.NewReader([]byte(archive(t, map[string]string{
		"videos/panier.mp4": "beaucoup d'octets",
		"mochawesome.json":  `{"stats":{"duration":0},"results":[]}`,
	}))))
	if err != nil {
		t.Fatalf("extraction: %v", err)
	}
	if len(files) != 1 || files[0].name != "mochawesome.json" {
		t.Errorf("fichiers extraits = %+v", files)
	}
}

func TestRecognise(t *testing.T) {
	cases := map[string]string{
		"phpstan.json":          "phpstan",
		"reports/cs-fixer.json": "cs-fixer",
		"mochawesome.json":      "cypress",
		"junit-integration.xml": "phpunit-integration",
		"junit.xml":             "phpunit",
	}
	for name, expected := range cases {
		kind, known := recognise(name)
		if !known || kind.tool != expected {
			t.Errorf("recognise(%q) = %q/%v, expected %q", name, kind.tool, known, expected)
		}
	}
	if _, known := recognise("videos/panier.mp4"); known {
		t.Error("a video is not a report")
	}
}

// The defect that made the `integration` family unobservable. The Tasks write under
// `test-results/<family>/` and all name their report `junit.xml`: integration therefore fell
// into the unit-test rule, and its failure was reported as a unit failure — on a signal
// weighing 25 in the score instead of 20, and leaving `integration` forever unmeasured.
func TestTheDirectoryNamesTheFamily(t *testing.T) {
	cases := map[string]struct {
		family results.Family
		tool   string
	}{
		"test-results/integration/junit.xml": {results.FamilyIntegration, "phpunit-integration"},
		"test-results/unit/junit.xml":        {results.FamilyUnit, "phpunit"},
		"test-results/phpstan/report.json":   {results.FamilyStatic, "phpstan"},
		"test-results/cs-fixer/report.json":  {results.FamilyStatic, "cs-fixer"},
		// The same file, published flat: the name takes over.
		"junit.xml": {results.FamilyUnit, "phpunit"},
	}

	for name, expected := range cases {
		kind, known := recognise(name)
		if !known {
			t.Errorf("recognise(%q) non reconnu", name)
			continue
		}
		if kind.family != expected.family || kind.tool != expected.tool {
			t.Errorf("recognise(%q) = %s/%s, expected %s/%s",
				name, kind.family, kind.tool, expected.family, expected.tool)
		}
	}
}

// Two reports of one run, in two different families, must be told apart. It is the case of an
// archive carrying both the unit and the integration ones — and the case the old recognition
// confused.
func TestTwoFamiliesInOneArchiveAreNotConfused(t *testing.T) {
	unit, _ := recognise("test-results/unit/junit.xml")
	integration, _ := recognise("test-results/integration/junit.xml")

	if unit.family == integration.family {
		t.Errorf("the same family %q for two distinct directories", unit.family)
	}
	// The tools differ too: the ingestion's idempotence is indexed by tool, and two families under
	// the same tool name would lose the second.
	if unit.tool == integration.tool {
		t.Errorf("the same tool %q — the second ingestion would be ignored", unit.tool)
	}
}

// The published contract must be true for all six families, not for four.
//
// The normative thing is the contract, not the implementation. The MSE Tasks write their
// Cypress report under `mocha/`, so that the name rule was enough; a project following the
// documented convention and writing `test-results/e2e/report.json` was **not recognised at
// all**.
//
// Silence is the worst outcome: the tests screen stays empty, the `e2e` family is never
// measured, and nothing says why.
func TestLeRepertoireDeFamillePubliéEstReconnu(t *testing.T) {
	cases := map[string]struct {
		family results.Family
		tool   string
	}{
		// The report's name says nothing: only the directory files it.
		"test-results/e2e/report.json": {results.FamilyE2E, "cypress"},
		// The tool's convention stays read: removing it would break MSE's existing runs.
		"test-results/mocha/mochawesome.json": {results.FamilyE2E, "cypress"},
	}

	for name, expected := range cases {
		kind, known := recognise(name)
		if !known {
			t.Errorf("recognise(%q) not recognised — the contract announces it nonetheless", name)
			continue
		}
		if kind.family != expected.family || kind.tool != expected.tool {
			t.Errorf("recognise(%q) = %s/%s, expected %s/%s",
				name, kind.family, kind.tool, expected.family, expected.tool)
		}
	}
}

// For static analysis, the family directory is not enough: PHPStan and php-cs-fixer produce two
// different JSON formats, and the parser cannot be guessed. The file's name then takes over —
// which is what the contract must require, rather than letting one believe `static/` is enough.
func TestUnderStaticTheToolMustStayIdentifiable(t *testing.T) {
	kind, known := recognise("test-results/static/phpstan.json")
	if !known || kind.family != results.FamilyStatic || kind.tool != "phpstan" {
		t.Errorf("recognise = %s/%s/%v — the name must designate the tool",
			kind.family, kind.tool, known)
	}

	// With no identifiable tool, nothing is guessed: better to ignore than to pick a parser at
	// random and return false violations.
	if _, known := recognise("test-results/static/report.json"); known {
		t.Error("a static report with no identifiable tool must not be guessed")
	}
}
