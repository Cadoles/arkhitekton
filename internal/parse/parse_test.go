package parse

import (
	"testing"
)

// The record keeps the class name and the suite path both: which one names the case is the
// interpreter's decision, not the parser's.
func TestAJUnitCaseKeepsEveryCandidateIdentity(t *testing.T) {
	report, err := ParseJUnit([]byte(`<testsuites>
  <testsuite name="tests/Unit" time="1.5">
    <testsuite name="LoginTest.php">
      <testcase name="testA" classname="App\LoginTest" time="0.25"/>
      <testcase name="testB" time="0.5"><error message="boom">trace</error></testcase>
    </testsuite>
  </testsuite>
</testsuites>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 2 || report.DurationMS != 1500 {
		t.Fatalf("report = %+v", report)
	}
	first, second := report.Cases[0], report.Cases[1]
	if first.ClassName != `App\LoginTest` || first.Path != "tests/Unit / LoginTest.php" || first.DurationMS != 250 {
		t.Errorf("first = %+v", first)
	}
	if first.Suite() != `App\LoginTest` || second.Suite() != "tests/Unit / LoginTest.php" {
		t.Errorf("suites = %q, %q", first.Suite(), second.Suite())
	}
	// An error is not a failure: the tool may care, so the record keeps it apart.
	if second.Outcome != JUnitErrored || second.Detail == nil || second.Detail.Text() != "boom" {
		t.Errorf("second = %+v", second)
	}
}

func TestABareJUnitSuiteIsRead(t *testing.T) {
	report, err := ParseJUnit([]byte(`<testsuite name="cypress"><testcase name="a"/></testsuite>`))
	if err != nil || len(report.Cases) != 1 || report.Cases[0].Path != "cypress" {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
}

func TestAMochaTestKeepsItsBlocks(t *testing.T) {
	report, err := ParseMochawesome([]byte(`{"stats":{"duration":1200},"results":[{"file":"login.cy.js",
		"suites":[{"title":"Login","suites":[{"title":"with SSO","tests":[
			{"title":"redirects","fullTitle":"Login with SSO redirects","pass":true,"currentRetry":1}]}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tests) != 1 || report.DurationMS != 1200 {
		t.Fatalf("report = %+v", report)
	}
	test := report.Tests[0]
	if len(test.Suites) != 2 || test.Suite() != "login.cy.js / Login / with SSO" {
		t.Errorf("suite = %q from %+v", test.Suite(), test.Suites)
	}
	if test.Name() != "Login with SSO redirects" || test.CurrentRetry != 1 {
		t.Errorf("test = %+v", test)
	}
}

// PHP serialises an empty map as `[]`: that means no violation, not an unreadable report.
func TestACleanPHPStanReportIsRead(t *testing.T) {
	report, err := ParsePHPStan([]byte(`{"totals":{"errors":0,"file_errors":0},"files":[],"errors":[]}`))
	if err != nil || len(report.Files) != 0 {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
}

func TestPHPStanFilesComeSorted(t *testing.T) {
	report, err := ParsePHPStan([]byte(`{"files":{
		"src/B.php":{"messages":[{"message":"m","line":3,"identifier":"r"}]},
		"src/A.php":{"messages":[{"message":"m","line":7}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 2 || report.Files[0].Path != "src/A.php" || report.Files[1].Messages[0].Line != 3 {
		t.Errorf("files = %+v", report.Files)
	}
}

// One metric in neither of k6's forms must not cost the others.
func TestAnOddK6MetricStaysApart(t *testing.T) {
	report, err := ParseK6([]byte(`{"metrics":{
		"http_reqs":{"count":10,"rate":2},
		"http_req_duration":{"values":{"p(95)":120},"thresholds":{"p(95)<200":{"ok":true}}},
		"odd":"not a metric"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics["http_reqs"].Values["count"] != 10 {
		t.Errorf("flat form = %+v", report.Metrics["http_reqs"])
	}
	duration := report.Metrics["http_req_duration"]
	if duration.Values["p(95)"] != 120 || !duration.Thresholds["p(95)<200"] {
		t.Errorf("structured form = %+v", duration)
	}
	if report.Metrics["odd"].Err == nil {
		t.Error("odd metric read without error")
	}
}

func TestEveryParserIsRegistered(t *testing.T) {
	ids := []ID{Raw, JUnitXML, MochawesomeJSON, PHPStanJSON, CSFixerJSON, K6SummaryJSON,
		SecuritySummaryJSON, SecurityFindingsJSONL}
	for _, id := range ids {
		if _, found := Lookup(id); !found {
			t.Errorf("parser %q not registered", id)
		}
	}

	parser, _ := Lookup(Raw)
	payload, err := parser([]byte("anything"))
	if bytes, ok := payload.(Bytes); err != nil || !ok || string(bytes) != "anything" {
		t.Errorf("raw = %v (%T), err = %v", payload, payload, err)
	}
}
