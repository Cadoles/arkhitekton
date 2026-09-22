package store

import (
	"context"
	"fmt"
	"testing"
)

func testRun(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	var runID int64
	err := db.pool.QueryRow(context.Background(),
		`insert into runs (pipeline_name, pipelinerun_name, namespace, status, started_at)
		 values ('php-static-tests', $1, 'mse-tekton', 'succeeded', now()) returning id`,
		name).Scan(&runID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return runID
}

func saveCases(t *testing.T, db *DB, runID int64, releaseID int64, family string, cases []TestCase) {
	t.Helper()
	failed := 0
	for _, testCase := range cases {
		if testCase.Status == "failed" {
			failed++
		}
	}
	if _, err := db.SaveTestResult(context.Background(), TestResult{
		RunID: runID, ReleaseID: &releaseID, Family: family, Tool: "phpunit",
		Status: "pass", Total: len(cases), Failed: failed,
	}, cases); err != nil {
		t.Fatalf("enregistrement: %v", err)
	}
}

func releaseFor(t *testing.T, db *DB, tag string) int64 {
	t.Helper()
	ctx := context.Background()
	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	id, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: tag, CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
	return id
}

// The view that answers the question actually asked before shipping: what changed? A
// stable counter can hide a resolved failure and a new one.
func TestDiffFamilySeesTheFourKindsOfChange(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	previous := releaseFor(t, db, "1.8.0-rc1")
	current := releaseFor(t, db, "1.8.0-rc2")

	saveCases(t, db, testRun(t, db, "run-prev"), previous, "unit", []TestCase{
		{Suite: "A", Name: "stable", Status: "passed"},
		{Suite: "A", Name: "corrige", Status: "failed"},
		{Suite: "A", Name: "casse", Status: "passed"},
		{Suite: "A", Name: "supprime", Status: "passed"},
	})
	saveCases(t, db, testRun(t, db, "run-curr"), current, "unit", []TestCase{
		{Suite: "A", Name: "stable", Status: "passed"},
		{Suite: "A", Name: "corrige", Status: "passed"},
		{Suite: "A", Name: "casse", Status: "failed"},
		{Suite: "A", Name: "ajoute", Status: "passed"},
	})

	diffs, err := db.DiffFamily(ctx, current, previous, "unit")
	if err != nil {
		t.Fatalf("comparaison: %v", err)
	}

	changes := map[string]string{}
	for _, diff := range diffs {
		changes[diff.Name] = diff.Change
	}

	expected := map[string]string{
		"stable":   ChangeUnchanged,
		"corrige":  ChangeFixed,
		"casse":    ChangeNewFailure,
		"ajoute":   ChangeAdded,
		"supprime": ChangeRemoved,
	}
	for name, change := range expected {
		if changes[name] != change {
			t.Errorf("%q = %q, expected %q", name, changes[name], change)
		}
	}
}

// A flaky test is a failure as far as comparison goes: going from stable to flaky is a
// regression, and hiding it would remove the only signal there is on it.
func TestFlakyCountsAsAFailureInTheDiff(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	previous := releaseFor(t, db, "1.8.0-rc1")
	current := releaseFor(t, db, "1.8.0-rc2")

	saveCases(t, db, testRun(t, db, "p"), previous, "e2e",
		[]TestCase{{Suite: "A", Name: "panier", Status: "passed"}})
	saveCases(t, db, testRun(t, db, "c"), current, "e2e",
		[]TestCase{{Suite: "A", Name: "panier", Status: "flaky"}})

	diffs, err := db.DiffFamily(ctx, current, previous, "e2e")
	if err != nil {
		t.Fatalf("comparaison: %v", err)
	}
	if len(diffs) != 1 || diffs[0].Change != ChangeNewFailure {
		t.Errorf("comparaison = %+v", diffs)
	}
}

// A release may be tested several times: the last run is what describes it.
func TestTheMostRecentReportWins(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	previous := releaseFor(t, db, "1.8.0-rc1")
	current := releaseFor(t, db, "1.8.0-rc2")

	saveCases(t, db, testRun(t, db, "p"), previous, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "failed"}})
	saveCases(t, db, testRun(t, db, "c1"), current, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "failed"}})
	saveCases(t, db, testRun(t, db, "c2"), current, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "passed"}})

	diffs, err := db.DiffFamily(ctx, current, previous, "unit")
	if err != nil {
		t.Fatalf("comparaison: %v", err)
	}
	if len(diffs) != 1 || diffs[0].Change != ChangeFixed {
		t.Errorf("comparaison = %+v", diffs)
	}
}

func TestSaveTestResultIsIdempotentPerTool(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "unit",
		[]TestCase{{Suite: "A", Name: "t", Status: "passed"}})

	exists, err := db.TestResultExists(ctx, runID, "phpunit")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !exists {
		t.Error("the report must be recognised as already recorded")
	}

	if other, _ := db.TestResultExists(ctx, runID, "phpstan"); other {
		t.Error("another tool must not be conflated")
	}
}

// A candidate is compared with the candidate before it, otherwise rc2 would be set
// against a release shipped weeks earlier and everything would look changed.
func TestPreviousReleaseIsTheOneJustBefore(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	first := releaseFor(t, db, "1.8.0-rc1")
	second := releaseFor(t, db, "1.8.0-rc2")
	third := releaseFor(t, db, "1.8.0-rc3")

	previous, err := db.PreviousRelease(ctx, third)
	if err != nil {
		t.Fatalf("previous: %v", err)
	}
	if previous != second {
		t.Errorf("previous = %d, expected %d", previous, second)
	}

	// The first release has nothing to compare against.
	if _, err := db.PreviousRelease(ctx, first); err == nil {
		t.Error("the first release must have no previous one")
	}
}

// Replay starts from `raw_object`: a run whose source object is not recorded cannot be
// replayed, and offering it would give a replay that erases without rebuilding.
func TestReprocessTargetsRequireARawObject(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	withRaw := testRun(t, db, "with-raw")
	withoutRaw := testRun(t, db, "without-raw")

	if _, err := db.SaveTestResult(ctx, TestResult{
		RunID: withRaw, ReleaseID: &releaseID, Family: "unit", Tool: "phpunit",
		Status: "pass", RawObject: "raw/with-raw/junit.xml",
	}, nil); err != nil {
		t.Fatalf("with raw: %v", err)
	}
	if _, err := db.SaveTestResult(ctx, TestResult{
		RunID: withoutRaw, ReleaseID: &releaseID, Family: "unit", Tool: "phpunit",
		Status: "pass",
	}, nil); err != nil {
		t.Fatalf("without raw: %v", err)
	}

	targets, err := db.ListReprocessTargets(ctx, 0)
	if err != nil {
		t.Fatalf("cibles: %v", err)
	}
	if len(targets) != 1 || targets[0].RunName != "with-raw" {
		t.Errorf("cibles = %+v", targets)
	}
	// The tools already projected are returned: knowing what disappears before rebuilding
	// it is the only way to observe that a fixed parser now produces less.
	if len(targets[0].Tools) != 1 || targets[0].Tools[0] != "phpunit" {
		t.Errorf("outils = %v", targets[0].Tools)
	}
}

func TestReprocessTargetsCanBeScopedToARelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	first := releaseFor(t, db, "1.8.0-rc1")
	second := releaseFor(t, db, "1.8.0-rc2")

	for _, pair := range []struct {
		release int64
		run     string
	}{{first, "run-1"}, {second, "run-2"}} {
		if _, err := db.SaveTestResult(ctx, TestResult{
			RunID: testRun(t, db, pair.run), ReleaseID: &pair.release,
			Family: "unit", Tool: "phpunit", Status: "pass",
			RawObject: "raw/" + pair.run + "/junit.xml",
		}, nil); err != nil {
			t.Fatalf("%s: %v", pair.run, err)
		}
	}

	targets, err := db.ListReprocessTargets(ctx, second)
	if err != nil {
		t.Fatalf("cibles: %v", err)
	}
	if len(targets) != 1 || targets[0].RunName != "run-2" {
		t.Errorf("cibles = %+v", targets)
	}
}

// The cases go with their report: they make no sense detached from what produced them.
func TestDeleteTestResultsCascadesToCases(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "unit", []TestCase{
		{Suite: "A", Name: "a", Status: "passed"},
		{Suite: "A", Name: "b", Status: "failed"},
	})

	deleted, err := db.DeleteTestResults(ctx, runID)
	if err != nil {
		t.Fatalf("effacement: %v", err)
	}
	if deleted != 1 {
		t.Errorf("%d report(s) erased", deleted)
	}

	var remaining int
	if err := db.pool.QueryRow(ctx, `select count(*) from test_cases`).Scan(&remaining); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d cases remain after erasing the report", remaining)
	}
}

// reportOf returns a family's report identifier, the only input to ListTestCases.
func reportOf(t *testing.T, db *DB, releaseID int64, family string) int64 {
	t.Helper()
	reports, err := db.ListReleaseTestResults(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("rapports: %v", err)
	}
	for _, report := range reports {
		if report.Family == family {
			return report.ID
		}
	}
	t.Fatalf("no report for family %s", family)
	return 0
}

// manyCases builds n cases named so as to be sortable and recognisable.
func manyCases(n int) []TestCase {
	cases := make([]TestCase, 0, n)
	for i := 0; i < n; i++ {
		cases = append(cases, TestCase{
			Suite:  "Suite",
			Name:   fmt.Sprintf("cas-%04d", i),
			Status: "passed",
		})
	}
	return cases
}

// The total covers the whole set, the page what was asked for. Without it the interface
// cannot know a next page exists.
func TestListTestCasesReturnsAPageAndTheTotal(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", manyCases(1200))
	reportID := reportOf(t, db, releaseID, "e2e")

	page, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if total != 1200 {
		t.Errorf("total %d, expected 1200", total)
	}
	if len(page) != 500 {
		t.Fatalf("%d cases in the page, expected 500", len(page))
	}
	if page[0].Name != "cas-0000" {
		t.Errorf("premier cas %q", page[0].Name)
	}
}

// Pages must neither overlap nor leave gaps: that is the property that makes pagination
// usable, and the one a non-total ordering breaks.
func TestPagesCoverEverythingWithoutOverlapping(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", manyCases(1200))
	reportID := reportOf(t, db, releaseID, "e2e")

	seen := map[string]int{}
	for offset := 0; offset < 1200; offset += 500 {
		page, _, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Offset: offset})
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		for _, testCase := range page {
			seen[testCase.Name]++
		}
	}

	if len(seen) != 1200 {
		t.Errorf("%d distinct cases seen, expected 1200", len(seen))
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("cas %s vu %d fois", name, count)
		}
	}
}

// An offset beyond the total returns an empty page but keeps the total: that is what
// lets the interface go back instead of showing "no case".
func TestAnOffsetTooFarKeepsTheTotal(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", manyCases(10))
	reportID := reportOf(t, db, releaseID, "e2e")

	page, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Offset: 5000})
	if err != nil {
		t.Fatalf("page hors bornes: %v", err)
	}
	if len(page) != 0 {
		t.Errorf("%d cases returned beyond the total", len(page))
	}
	if total != 10 {
		t.Errorf("total %d, expected 10", total)
	}
}

// The filter covers the whole set and not the page, and the total follows. That is the
// point of server-side filtering: filtering the transferred page would answer "no case"
// for a test present two pages further on.
func TestTheFilterCoversEveryPage(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	cases := manyCases(1200)
	// A recognisable case, deliberately placed beyond the first page.
	cases[900].Name = "connexion-oidc"
	saveCases(t, db, runID, releaseID, "e2e", cases)
	reportID := reportOf(t, db, releaseID, "e2e")

	page, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Query: "oidc"})
	if err != nil {
		t.Fatalf("filtre: %v", err)
	}
	if total != 1 {
		t.Fatalf("filtered total %d, expected 1", total)
	}
	if len(page) != 1 || page[0].Name != "connexion-oidc" {
		t.Errorf("filtered page %+v", page)
	}
}

// The filter joins the suite and the name: the user types a fragment without knowing
// which of the two it belongs to.
func TestTheFilterSeesTheSuiteAndTheName(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	releaseID := releaseFor(t, db, "1.8.0-rc1")
	runID := testRun(t, db, "run-1")
	saveCases(t, db, runID, releaseID, "e2e", []TestCase{
		{Suite: "Panier", Name: "adds an item", Status: "passed"},
		{Suite: "Connexion", Name: "refuses an empty password", Status: "failed"},
	})
	reportID := reportOf(t, db, releaseID, "e2e")

	bySuite, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Query: "panier"})
	if err != nil {
		t.Fatalf("filter by suite: %v", err)
	}
	if total != 1 || len(bySuite) != 1 {
		t.Errorf("filter by suite: %d cases, total %d", len(bySuite), total)
	}

	byName, total, err := db.ListTestCases(ctx, reportID, CasePage{Limit: 500, Query: "password"})
	if err != nil {
		t.Fatalf("filter by name: %v", err)
	}
	if total != 1 || len(byName) != 1 {
		t.Errorf("filter by name: %d cases, total %d", len(byName), total)
	}
}

// The question the contract check asks: did this run leave anything at all?
//
// About the whole run and not one tool, because what it observes is a breach of the
// contract — a pipeline announcing results and leaving none produces an empty screen
// with nothing saying why. It must therefore see a result whichever way it came in.
func TestARunWithNoResultAtAllIsRecognised(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	muet := testRun(t, db, "run-muet")
	has, err := db.RunHasResults(ctx, muet)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if has {
		t.Error("a run with no result declares itself provided")
	}

	// One result is enough: the question is not "how many" but "did anything come back". A
	// pipeline publishing one family out of three is another problem, which the score
	// already carries.
	parlant := testRun(t, db, "run-parlant")
	release := releaseFor(t, db, "1.8.0-rc9")
	saveCases(t, db, parlant, release, "unit", []TestCase{
		{Name: "one case", Status: "passed"},
	})

	has, err = db.RunHasResults(ctx, parlant)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !has {
		t.Error("a provided run declares itself silent")
	}

	// And the answer is indeed per run: the neighbouring one stays silent.
	if has, _ := db.RunHasResults(ctx, muet); has {
		t.Error("one run's result spills onto another")
	}
}
