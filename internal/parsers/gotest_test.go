package parsers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// A real `go test -json` stream: a passing, a failing and a skipped test, a table with a failing
// subtest, a package that does not build and one with no test file.
func TestAGoTestReportIsReadCaseByCase(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "app", "testdata", "reports", "go-test.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := ParseGoTest(raw)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]parse.GoTestCase{}
	for _, testCase := range report.Cases {
		got[strings.TrimPrefix(testCase.Package, "example.test/sample/")+":"+testCase.Name] = testCase
	}
	expected := map[string]parse.GoTestOutcome{
		"broken:":                  parse.GoTestFailed,
		"ok:TestPasses":            parse.GoTestPassed,
		"ok:TestFails":             parse.GoTestFailed,
		"ok:TestSkips":             parse.GoTestSkipped,
		"ok:TestTable/first":       parse.GoTestPassed,
		"ok:TestTable/second_case": parse.GoTestFailed,
	}
	if len(got) != len(expected) {
		t.Errorf("cases %v", got)
	}
	for key, outcome := range expected {
		if got[key].Outcome != outcome {
			t.Errorf("%s: %q, expected %q", key, got[key].Outcome, outcome)
		}
	}
	if output := got["ok:TestFails"].Output; !strings.Contains(output, "expected 2, got 3") ||
		!strings.Contains(output, "before the failure") || strings.Contains(output, "=== RUN") {
		t.Errorf("output %q", output)
	}
	if output := got["broken:"].Output; !strings.Contains(output, "cannot use \"nope\"") {
		t.Errorf("build failure output %q", output)
	}
}

// A package that dies in a test leaves it failed, not unknown.
func TestATestCutShortByItsPackageFails(t *testing.T) {
	raw := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestPanics"}`,
		`{"Action":"output","Package":"p","Test":"TestPanics","Output":"panic: boom\n"}`,
		`{"Action":"fail","Package":"p","Elapsed":0.1}`,
	}, "\n")
	report, err := ParseGoTest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 || report.Cases[0].Name != "TestPanics" || report.Cases[0].Outcome != parse.GoTestFailed {
		t.Errorf("cases %+v", report.Cases)
	}
}

func TestSomethingElseThanAGoTestReportIsRefused(t *testing.T) {
	if _, err := ParseGoTest([]byte("not json at all\n")); err == nil {
		t.Error("a text report was read as an empty one")
	}
}
