package worker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

// maxArtifactBytes bounds what a report may weigh once decompressed.
//
// An archive published by a pipeline is produced in the cluster and is not hostile, but a JUnit
// report of several gigabytes — a test loop gone wild — would bring the worker to its knees
// instead of reporting it. The bound turns an out-of-memory failure into a message.
const maxArtifactBytes = 64 << 20

// TestResultStore is what the ingester needs from the database.
type TestResultStore interface {
	SaveTestResult(ctx context.Context, result store.TestResult, cases []store.TestCase) (int64, error)
	TestResultExists(ctx context.Context, runID int64, tool string) (bool, error)
}

// TestIngester records the test reports a run published.
type TestIngester struct {
	store   TestResultStore
	objects Objects
	logger  *slog.Logger
}

func NewTestIngester(results TestResultStore, objects Objects, logger *slog.Logger) *TestIngester {
	return &TestIngester{store: results, objects: objects, logger: logger}
}

// artifactKind associates a file name with its reader.
//
// Recognition is by **name**, decided by the catalogue and not by the pipeline: that is what
// allows adding a tool without touching the ingestion code, and what avoids guessing a format
// from its content.
type artifactKind struct {
	family results.Family
	tool   string
	parse  func(raw []byte) (results.Report, error)
}

// junitKind returns a JUnit reader for a given family and tool.
func junitKind(family results.Family, tool string) artifactKind {
	return artifactKind{family, tool, func(raw []byte) (results.Report, error) {
		return results.ParseJUnit(family, tool, raw)
	}}
}

// recognise identifies an artifact file.
//
// The **directory** is consulted before the file name, and that is what was missing. The Tasks
// write under `test-results/<family>/` — `unit/`, `integration/`, `phpstan/`, `cs-fixer/` — and
// all name their report `junit.xml`. Looking only at the name therefore dropped the integration
// report into the last rule, that of the unit tests: the `integration` family was never
// measured, and its failure was reported as a unit-test failure.
//
// An unknown file is ignored silently: an archive also contains logs and screenshots, and
// failing on them would lose the reports that accompany them.
func recognise(name string) (artifactKind, bool) {
	clean := strings.ToLower(path.Clean(name))
	base := path.Base(clean)
	// The segment preceding the file. `test-results/integration/junit.xml` gives `integration`.
	dir := path.Base(path.Dir(clean))

	xml := strings.HasSuffix(base, ".xml")
	json := strings.HasSuffix(base, ".json")

	switch {
	// The directory names the family, whatever the report's name.
	case dir == "integration" && xml:
		return junitKind(results.FamilyIntegration, "phpunit-integration"), true

	case dir == "unit" && xml:
		return junitKind(results.FamilyUnit, "phpunit"), true

	case dir == "phpstan" && json:
		return artifactKind{results.FamilyStatic, "phpstan", results.ParsePHPStan}, true

	case (dir == "cs-fixer" || dir == "csfixer") && json:
		return artifactKind{results.FamilyStatic, "cs-fixer", results.ParseCSFixer}, true

	// `e2e/` was missing, and it is the published contract that required it. The MSE Tasks write
	// under `mocha/`, so that the name rule was enough here; a project following the documented
	// convention and naming its report `test-results/e2e/report.json` was, on the other hand, **not
	// recognised at all**. Silence is the worst outcome: the tests screen stays empty, and nothing
	// says the family was never read.
	case dir == "e2e" && json:
		return artifactKind{results.FamilyE2E, "cypress", results.ParseMochawesome}, true

	// Failing a telling directory, the file's name. These rules cover the reports published flat,
	// outside the `test-results/` convention.
	case strings.Contains(base, "phpstan") && json:
		return artifactKind{results.FamilyStatic, "phpstan", results.ParsePHPStan}, true

	case (strings.Contains(base, "cs-fixer") || strings.Contains(base, "csfixer")) && json:
		return artifactKind{results.FamilyStatic, "cs-fixer", results.ParseCSFixer}, true

	case strings.Contains(base, "mocha") && json:
		return artifactKind{results.FamilyE2E, "cypress", results.ParseMochawesome}, true

	case strings.Contains(base, "integration") && xml:
		return junitKind(results.FamilyIntegration, "phpunit-integration"), true

	case xml && (strings.Contains(base, "junit") || strings.Contains(base, "phpunit")):
		return junitKind(results.FamilyUnit, "phpunit"), true
	}

	return artifactKind{}, false
}

// Ingest reads a finished run's artifacts and records what it recognises in them.
//
// Idempotent per tool: the observer sees a finished run again at restart, and recording again
// would double every case — therefore every comparison with the previous release.
func (i *TestIngester) Ingest(ctx context.Context, run LoadRun) error {
	prefix := "raw/" + run.Name + "/"
	objects, err := i.objects.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("artifacts of run %q: %w", run.Name, err)
	}

	var failures []string
	for _, object := range objects {
		// Load campaigns have their own ingester: their measurements are not test cases, and passing
		// them through here would project them into a model that does not suit them.
		if strings.Contains(object.Key, "/k6/") {
			continue
		}

		if err := i.ingestObject(ctx, run, object.Key); err != nil {
			i.logger.Error("test artifact", "run", run.Name, "objet", object.Key, "error", err)
			failures = append(failures, object.Key)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("run %q: %d artifact(s) not recorded: %s",
			run.Name, len(failures), strings.Join(failures, ", "))
	}
	return nil
}

func (i *TestIngester) ingestObject(ctx context.Context, run LoadRun, key string) error {
	content, err := i.objects.Get(ctx, key)
	if err != nil {
		return err
	}
	defer content.Close()

	files, err := extract(key, content)
	if err != nil {
		return err
	}

	for _, file := range files {
		kind, known := recognise(file.name)
		if !known {
			continue
		}

		already, err := i.store.TestResultExists(ctx, run.ID, kind.tool)
		if err != nil {
			return err
		}
		if already {
			continue
		}

		report, err := kind.parse(file.content)
		if err != nil {
			// An unreadable report does not prevent recording the others: a campaign producing four reports
			// must not be lost whole because one of them is truncated.
			i.logger.Error("rapport illisible",
				"run", run.Name, "file", file.name, "error", err)
			continue
		}

		cases := make([]store.TestCase, 0, len(report.Cases))
		for _, testCase := range report.Cases {
			cases = append(cases, store.TestCase{
				Suite: testCase.Suite, Name: testCase.Name, Status: string(testCase.Status),
				DurationMS:     testCase.DurationMS,
				FailureMessage: testCase.Message, FailureTrace: testCase.Trace,
			})
		}

		summary := report.Summary
		if summary == "" {
			summary = results.SummarizeJUnit(report)
		}

		if _, err := i.store.SaveTestResult(ctx, store.TestResult{
			RunID: run.ID, ReleaseID: run.ReleaseID,
			Family: string(kind.family), Tool: kind.tool, Status: report.Status,
			Shape: legacyShape(string(kind.family)), Producer: legacyProducer(kind.tool),
			Total: report.Total, Passed: report.Passed, Failed: report.Failed,
			Skipped: report.Skipped, Flaky: report.Flaky,
			DurationMS: report.DurationMS, Summary: summary, RawObject: key,
		}, cases); err != nil {
			return err
		}

		i.logger.Info("test report recorded",
			"run", run.Name, "tool", kind.tool, "cases", len(cases), "status", report.Status)
	}

	return nil
}

// artifactFile is a file extracted from an object.
type artifactFile struct {
	name    string
	content []byte
}

// extract returns an object's files.
//
// An archive is unpacked, a single file is returned as it is. Both forms coexist because the
// pipelines publish both: a JUnit report whose name is known leaves on its own, a Cypress
// directory leaves as a `tar.gz` — its file names depend on the specs run and cannot be
// presigned one by one.
func extract(key string, source io.Reader) ([]artifactFile, error) {
	if !strings.HasSuffix(key, ".tar.gz") && !strings.HasSuffix(key, ".tgz") {
		content, err := io.ReadAll(io.LimitReader(source, maxArtifactBytes))
		if err != nil {
			return nil, err
		}
		return []artifactFile{{name: key, content: content}}, nil
	}

	gz, err := gzip.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("archive %q illisible: %w", key, err)
	}
	defer gz.Close()

	var files []artifactFile
	reader := tar.NewReader(gz)
	var budget int64 = maxArtifactBytes

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive %q illisible: %w", key, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Only the formats that can be read are loaded into memory: a Cypress archive weighs mostly
		// videos, which it would be absurd to fetch back only to throw away at once.
		if _, known := recognise(header.Name); !known {
			continue
		}

		content, err := io.ReadAll(io.LimitReader(reader, budget))
		if err != nil {
			return nil, err
		}
		budget -= int64(len(content))
		if budget <= 0 {
			return nil, fmt.Errorf("archive %q: decompressed size beyond the limit", key)
		}

		files = append(files, artifactFile{name: header.Name, content: content})
	}

	return files, nil
}

// readLimited reads a stream while bounding what it may occupy in memory.
func readLimited(source io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(source, maxArtifactBytes))
}

// legacyProducer attributes a result read by the ingestion that predates the plugins' interpreters
// to the plugin that now brings its tool. The version says the reading predates it.
func legacyProducer(tool string) string {
	plugin := "legacy"
	switch tool {
	case "phpunit", "phpstan", "cs-fixer":
		plugin = "php"
	case "phpunit-integration", "cypress":
		plugin = "mse"
	case securityTool:
		plugin = "security"
	case "k6":
		plugin = "k6"
	}
	return plugin + "@0.0.0-legacy"
}

// legacyShape is how a result of a family is stored and compared.
func legacyShape(family string) string {
	switch results.Family(family) {
	case results.FamilySecurity:
		return "findings"
	case results.FamilyLoad:
		return "measures"
	}
	return "cases"
}
